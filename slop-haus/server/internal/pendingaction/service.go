package pendingaction

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Store is the proposals' persistence. Every method runs for the tenant in
// ctx, which the database enforces by row level security as well.
type Store interface {
	// Propose runs one transaction: it locks the proposer and the ticket,
	// refuses a proposer that is not an enabled console and a ticket an agent
	// holds, reads the ticket's Basis and hands it to check, supersedes the
	// ticket's pending proposal of the same kind, enforces the proposer's
	// limits, and inserts. check is how the rules of the action, which the
	// store knows nothing of, see the ticket as it stands in that transaction.
	Propose(ctx context.Context, in *NewAction, check func(*Basis) error) (*Action, error)
	Get(ctx context.Context, id uuid.UUID, now time.Time) (*Action, error)
	List(ctx context.Context, filter *ListFilter, now time.Time) ([]Action, error)
	// Withdraw ends the agent's own pending proposal.
	Withdraw(ctx context.Context, agentID, id uuid.UUID, now time.Time) (*Action, error)
	// VoidAgent ends every pending proposal of the agent.
	VoidAgent(ctx context.Context, agentID uuid.UUID, reason string, now time.Time) (int64, error)
	// ExpireDue records the expiry of every pending proposal past it.
	ExpireDue(ctx context.Context, now time.Time) (int64, error)

	// The rest are a person's decision. The first four run in the
	// transaction the caller holds (see ticket.Service.InTransition), so
	// settling a proposal and applying it commit together.

	// LockForDecision reads a proposal and holds it, after the ticket it is
	// about: the order Propose takes them in, so neither waits on the other
	// while holding what it wants. ErrNotFound for another tenant's.
	LockForDecision(ctx context.Context, q db.Querier, id uuid.UUID, now time.Time) (*Action, error)
	// Standing is the proposer as it is now.
	Standing(ctx context.Context, q db.Querier, agentID uuid.UUID) (*Standing, error)
	// LiveBasis is the ticket as it is now, and whether an agent holds it;
	// for a proposal about an attempt (non-nil), that attempt as it is now
	// and whether an agent holds it.
	LiveBasis(ctx context.Context, q db.Querier, ticketID int64, attempt *int) (*Basis, bool, error)
	// HasPendingEdit says whether an edit proposal on the ticket is still
	// pending (not expired), whoever proposed it.
	HasPendingEdit(ctx context.Context, q db.Querier, ticketID int64, now time.Time) (bool, error)
	// Settle ends a pending proposal in state.
	Settle(ctx context.Context, q db.Querier, id uuid.UUID, state State, outcome string, by uuid.UUID, now time.Time) (*Action, error)
	// SettleOwn is Settle in a transaction of its own, for a proposal whose
	// application was rolled back; it settles nothing already decided.
	SettleOwn(ctx context.Context, id uuid.UUID, state State, outcome string, by uuid.UUID, now time.Time) (*Action, error)
	// Reject ends pending proposals as rejected: those named, or every one
	// when ids is nil (an empty, non-nil ids names none), of the kinds given
	// (nil: any). It returns those it rejected.
	Reject(ctx context.Context, ids []uuid.UUID, kinds []Kind, by uuid.UUID, reason string, now time.Time) ([]Action, error)
	// Describe says what proposals are about, for showing them.
	Describe(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]*Describing, error)
}

// Standing is a proposer's state at the time of a decision.
type Standing struct {
	Name string
	// Proposer is a console, or a conductor: the agents that may propose.
	Proposer bool
	Enabled  bool
}

// Describing is what a proposal is about, for showing it.
type Describing struct {
	AgentName string
	// The ticket's title and status are empty for a proposal with no ticket.
	TicketTitle  string
	TicketStatus ticket.Status
	// Live is the ticket's Basis now, and Held whether an agent holds it.
	Live Basis
	Held bool
}

// NewAction is a proposal as the store inserts it.
type NewAction struct {
	ID      uuid.UUID
	AgentID uuid.UUID
	// TicketID is nil for a kind about the tenant.
	TicketID *int64
	Kind     Kind
	Payload  Payload
	// Attempt is the attempt number the proposer named, for a kind that is
	// about an attempt; the store resolves it (ticket.ResolveAttempt).
	Attempt            *int
	CreatedAt          time.Time
	ExpiresAt          time.Time
	MaxPendingPerAgent int
	RatePerMinute      int
}

type Service struct {
	store  Store
	cfg    Config
	logger *slog.Logger
	// now is swappable so tests can age proposals without sleeping.
	now     func() time.Time
	tickets Transitions
	grants  Grants
	events  *audit.Recorder
	auto    *autoApproval
	// stewardOwners names who the steward's approvals are made as.
	stewardOwners TenantOwners
	roles         Roles
	// tenantAppliers make the change a tenant kind's approval stands for.
	tenantAppliers map[Kind]TenantApplier
	manifests      ManifestEditor
	decisions      DecisionRecorder
	assessments    Assessments
	drafts         Drafts
	closer         MergeCloser
	// mergeFactsDeadline bounds the code host calls of one listing.
	mergeFactsDeadline time.Duration
}

// DecisionRecorder is told what a person (or the proposer, for a withdrawal)
// did with a proposal, so the steward's assessment of it can be compared with
// the outcome. decision is one of "approved", "rejected" or "withdrawn". It
// runs after the decision has committed and its failure never undoes it.
type DecisionRecorder interface {
	RecordDecision(ctx context.Context, id uuid.UUID, decision string, at time.Time) error
}

// WithDecisionRecorder reports each decision to rec.
func WithDecisionRecorder(rec DecisionRecorder) Option {
	return func(s *Service) { s.decisions = rec }
}

// AssessmentView is the steward's verdict on a proposal, as the approvals
// view shows it. Strings, so this package need not import the steward's.
type AssessmentView struct {
	Category string
	Tier     string
	Verdict  string
	Mode     string
	// Reason is the steward's own words: untrusted, show as text.
	Reason string
	// Evidence is what the server's checks found.
	Evidence []AssessmentEvidence
	// VetoMinutes is the veto window for a veto-tier verdict, 0 otherwise.
	VetoMinutes int
}

type AssessmentEvidence struct {
	Check  string
	OK     bool
	Ref    string
	Detail string
}

// Assessments reads the steward's assessments of proposals; a proposal it
// never assessed is absent from the result.
type Assessments interface {
	Assessments(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]*AssessmentView, error)
}

// WithAssessments shows the steward's assessment on each proposal it has one
// for. A failure to read them leaves the views without: the label is advice.
func WithAssessments(a Assessments) Option {
	return func(s *Service) { s.assessments = a }
}

func (s *Service) recordDecision(ctx context.Context, a *Action, decision string) {
	if s.decisions == nil {
		return
	}
	at := s.now()
	if a.DecidedAt != nil {
		at = *a.DecidedAt
	}
	if err := s.decisions.RecordDecision(ctx, a.ID, decision, at); err != nil {
		s.logger.WarnContext(ctx, "pending action: record decision", "proposal", a.ID, "error", err)
	}
}

// Roles says what role a person holds in a tenant, for the kinds that only an
// owner may decide.
type Roles interface {
	IsTenantOwner(ctx context.Context, userID, tenantID uuid.UUID) (bool, error)
}

// TenantApplier makes the change a tenant kind's proposal asks for, in the
// transaction that settles the proposal, as the approver.
type TenantApplier interface {
	Apply(ctx context.Context, tx ticket.TransitionTx, approver uuid.UUID, a *Action) error
}

// TenantRechecker is a TenantApplier that also says, under the approval's
// locks and before Apply, that the proposal has gone stale.
type TenantRechecker interface {
	Recheck(ctx context.Context, tx ticket.TransitionTx, a *Action) (State, string, error)
}

// WithRoles lets kinds that need an owner be decided. Without it they cannot
// be, so a Service built without it fails closed.
func WithRoles(roles Roles) Option {
	return func(s *Service) { s.roles = roles }
}

// WithTenantApplier sets what approving a tenant kind does. A tenant kind
// without one cannot be approved.
func WithTenantApplier(kind Kind, applier TenantApplier) Option {
	return func(s *Service) {
		if s.tenantAppliers == nil {
			s.tenantAppliers = map[Kind]TenantApplier{}
		}
		s.tenantAppliers[kind] = applier
	}
}

// Transitions is the ticket service's transaction, in which an approval
// makes the person's move and settles the proposal together.
type Transitions interface {
	InTransition(ctx context.Context, fn func(tx ticket.TransitionTx) error) error
	// ResumeAffinity is how long an asking agent keeps first claim once a
	// person has answered.
	ResumeAffinity() time.Duration
	// NotifyApproved reports an approval made in a transaction that has
	// since committed -- see ticket.ApproveReviewRecorded.
	NotifyApproved(ctx context.Context, a *ticket.Approval)
	// Get reads a ticket, to show what an edit would change.
	Get(ctx context.Context, id int64) (*ticket.Ticket, error)
	// PrepareUpdate is the check a ticket edit meets before its transaction,
	// normalising its paths in place -- see ticket.Service.PrepareUpdate.
	PrepareUpdate(ctx context.Context, params *ticket.UpdateParams) error
	// ScopeOverlapsWith is what the ticket's scopes would overlap were its
	// paths these.
	ScopeOverlapsWith(ctx context.Context, id int64, paths []model.Path) ([]ticket.ScopeOverlap, error)
}

// Grants says whether an agent can still act: approving refuses a proposer
// whose credentials have gone, even where nothing voided its proposals
// (a grant revoked by token reuse, say).
type Grants interface {
	HasFullAccessGrant(ctx context.Context, tenantID, agentID uuid.UUID) (bool, error)
}

// Option configures a Service.
type Option func(*Service)

// WithApproval is what deciding a proposal needs: without it Approve is
// refused, so a Service built without it fails closed.
func WithApproval(tickets Transitions, grants Grants) Option {
	return func(s *Service) { s.tickets, s.grants = tickets, grants }
}

// WithClock replaces the time source, so a test of a booted server can age
// proposals past their expiry without waiting.
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// WithEvents records proposals' security events.
func WithEvents(events *audit.Recorder) Option {
	return func(s *Service) { s.events = events }
}

func NewService(store Store, cfg Config, logHandler slog.Handler, opts ...Option) *Service {
	s := &Service{store: store, cfg: cfg.withDefaults(), logger: slog.New(logHandler), now: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Service) record(ctx context.Context, action audit.Action, attrs ...any) {
	if s.events != nil {
		s.events.Record(ctx, action, attrs...)
	}
}

// Propose records a console's proposal. Nothing about the ticket changes,
// unless the person the proposer acts for has turned on auto-approve for its
// kind (see WithAutoApprove): then it is applied at once and the returned
// action says so.
//
// It is refused when the proposer is not an enabled console, the ticket is
// not in the caller's tenant or is held by an agent, the action is one the
// ticket's status rules would refuse a person, or a limit is hit. A pending
// proposal of the same kind on the same ticket is superseded by this one.
func (s *Service) Propose(ctx context.Context, params *ProposeParams) (*Action, error) {
	payload, err := normalise(params.Kind, params.Payload)
	if err != nil {
		return nil, err
	}
	var ticketID *int64
	switch params.Kind.Spec().Subject {
	case SubjectTicket:
		if params.TicketID <= 0 {
			return nil, invalid("a %s proposal needs a ticket", params.Kind)
		}
		ticketID = &params.TicketID
	default:
		if params.TicketID != 0 {
			return nil, invalid("a %s proposal is about the tenant, not a ticket", params.Kind)
		}
	}
	if params.Kind == KindManifestEdit {
		if err := s.checkManifestBase(ctx, payload); err != nil {
			return nil, err
		}
	}
	if params.Kind == KindEdit {
		if payload, err = s.prepareEdit(ctx, params.TicketID, payload); err != nil {
			return nil, err
		}
	}
	now := s.now()
	created, err := s.store.Propose(ctx, &NewAction{
		ID:                 uuid.New(),
		AgentID:            params.AgentID,
		TicketID:           ticketID,
		Kind:               params.Kind,
		Payload:            payload,
		Attempt:            params.Attempt,
		CreatedAt:          now,
		ExpiresAt:          now.Add(s.cfg.TTL),
		MaxPendingPerAgent: s.cfg.MaxPendingPerAgent,
		RatePerMinute:      s.cfg.RatePerMinute,
	}, func(b *Basis) error { return checkAgainstTicket(params.Kind, payload, b) })
	if err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "pending action proposed",
		"id", created.ID, "ticket", created.ticketID(), "kind", created.Kind, "agent", created.AgentID)
	s.record(ctx, audit.ProposalCreated, "proposal", created.ID, "ticket", created.ticketID(), "kind", created.Kind,
		"agent_id", created.AgentID)
	return s.autoApprove(ctx, created), nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Action, error) {
	return s.store.Get(ctx, id, s.now())
}

func (s *Service) List(ctx context.Context, filter *ListFilter) ([]Action, error) {
	if filter == nil {
		filter = &ListFilter{}
	}
	for _, st := range filter.States {
		if !st.valid() {
			return nil, fmt.Errorf("%w: unknown state %q", ErrInvalid, st)
		}
	}
	if filter.Limit < 0 {
		return nil, fmt.Errorf("%w: limit %d", ErrInvalid, filter.Limit)
	}
	if filter.Ticket != nil {
		if err := filter.Ticket.Validate(); err != nil {
			return nil, err
		}
	}
	return s.store.List(ctx, filter, s.now())
}

// Withdraw is the proposer taking its own pending proposal back. Another
// agent's, or one already decided or expired, is refused.
func (s *Service) Withdraw(ctx context.Context, agentID, id uuid.UUID) (*Action, error) {
	withdrawn, err := s.store.Withdraw(ctx, agentID, id, s.now())
	if err != nil {
		return nil, err
	}
	s.recordDecision(ctx, withdrawn, "withdrawn")
	return withdrawn, nil
}

// VoidAgent ends every pending proposal of an agent that has stopped being
// an enabled console -- disabled, no grants left -- so nothing it
// proposed can be approved afterwards.
func (s *Service) VoidAgent(ctx context.Context, agentID uuid.UUID, reason string) (int64, error) {
	n, err := s.store.VoidAgent(ctx, agentID, reason, s.now())
	if err == nil && n > 0 {
		s.logger.InfoContext(ctx, "pending actions voided", "agent", agentID, "count", n, "reason", reason)
	}
	return n, err
}

// ExpireDue records the expiry of every proposal past it. Reads already
// treat those as expired; recording frees the ticket's slot and starts
// their retention.
func (s *Service) ExpireDue(ctx context.Context) (int64, error) {
	return s.store.ExpireDue(ctx, s.now())
}
