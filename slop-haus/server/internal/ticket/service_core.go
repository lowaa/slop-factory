package ticket

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Store is the persistence Service needs, split along the two jobs it
// does. Service owns the rules; Store owns the SQL. Both halves are
// declared here rather than beside the Postgres implementation, so the
// dependency points inward -- the domain states what it needs.
type Store interface {
	TicketStore
	LeaseStore
}

// TicketStore is the ticket, comment and dependency surface.
type TicketStore interface {
	CreateTicket(ctx context.Context, params *CreateParams) (*Ticket, error)
	GetTicket(ctx context.Context, id int64) (*Ticket, error)
	// ListAttempts is the ticket's attempts by number, for its detail.
	ListAttempts(ctx context.Context, ticketID int64) ([]Attempt, error)
	// SetAttemptVariant routes the ticket's attempt to a variant version, or
	// clears it with nil, until the attempt's first lease.
	// A nil attempt is the ticket's promoted (else only) attempt.
	SetAttemptVariant(ctx context.Context, ticketID int64, attempt *int, versionID *uuid.UUID) (*AttemptVariantChange, error)
	// AddAttempts adds one attempt per entry, all or none (see
	// PostgresStore.AddAttempts).
	AddAttempts(ctx context.Context, ticketID int64, versionIDs []*uuid.UUID, replay bool, batchID *uuid.UUID) ([]*AttemptAdded, error)
	// CloseAttempt closes the ticket's numbered attempt (see
	// PostgresStore.CloseAttempt).
	CloseAttempt(ctx context.Context, ticketID int64, number int) (*AttemptClosed, error)
	// PromoteAttempt makes the numbered attempt the promoted one (see
	// PostgresStore.PromoteAttempt).
	PromoteAttempt(ctx context.Context, ticketID int64, number int, userID uuid.UUID, affinity time.Duration) (*AttemptPromoted, error)
	// PinBaseCommit stores the ticket's base commit unless it has one, and
	// returns the stored value (see PostgresStore.PinBaseCommit).
	PinBaseCommit(ctx context.Context, ticketID int64, sha string) (string, error)
	// AttemptBranchState and MarkBranchCreated are the record of slop-haus
	// having made an attempt's branch (see Service.EnsureAttemptBranch).
	AttemptBranchState(ctx context.Context, ticketID int64, number int) (*AttemptBranchState, error)
	MarkBranchCreated(ctx context.Context, ticketID int64, number int) error
	ListTickets(ctx context.Context, filter *ListFilter) ([]Ticket, error)
	// CountTickets counts the matching tickets by status.
	CountTickets(ctx context.Context, filter *ListFilter) (map[Status]int, error)
	// SetPriority sets every one of ids to priority, or none of them.
	SetPriority(ctx context.Context, ids []int64, priority model.Priority) ([]Ticket, error)
	// MarkPostReviewed records a person having looked over a done ticket's
	// change, with their comment if they left one.
	MarkPostReviewed(ctx context.Context, ticketID int64, userID uuid.UUID, comment string) (*Ticket, error)
	// EditText changes a ticket's title and/or body, recording the change
	// as a revision; EditHeld does the same to the ticket agentID holds.
	EditText(ctx context.Context, id int64, editor Author, params *EditParams) (*EditResult, error)
	EditHeld(ctx context.Context, agentID uuid.UUID, params *EditParams) (*EditResult, error)
	ListRevisions(ctx context.Context, ticketID int64) ([]Revision, error)
	ListStatusChanges(ctx context.Context, ticketID int64) ([]StatusChange, error)

	AddComment(ctx context.Context, ticketID int64, by Author, body string) (*Comment, error)
	// ListComments and ListAgentContexts are the board's: every attempt's.
	// An agent's reads go through ConversationOf and the *In variants.
	ListComments(ctx context.Context, ticketID int64) ([]Comment, error)
	ListAgentContexts(ctx context.Context, ticketID int64) ([]AgentContext, error)
	ListCommentsIn(ctx context.Context, ticketID int64, conv Conversation) ([]Comment, error)
	ListAgentContextsIn(ctx context.Context, ticketID int64, conv Conversation) ([]AgentContext, error)
	ConversationOf(ctx context.Context, ticketID int64, agentID uuid.UUID) (Conversation, error)
	// HeldAttempt is the attempt agentID holds a live lease on ticketID for;
	// ErrNoLease when it holds none.
	HeldAttempt(ctx context.Context, agentID uuid.UUID, ticketID int64) (*AttemptRef, error)
	TicketSeenBy(ctx context.Context, ticketID int64, conv Conversation) (*Ticket, error)
	// Snapshot is the ticket as its original attempt first saw it, which a
	// replay reads in place of the live ticket.
	Snapshot(ctx context.Context, ticketID int64) (*Snapshot, error)
	// ListExternal is what intake sources sent about the ticket, oldest
	// first.
	ListExternal(ctx context.Context, ticketID int64) ([]ExternalEvent, error)

	// AddDependency rejects an edge that would close a cycle, which it
	// can only decide atomically with the write -- see PostgresStore.
	AddDependency(ctx context.Context, by Author, ticketID, dependsOnID int64) (*DependencyChange, error)
	RemoveDependency(ctx context.Context, by Author, ticketID, dependsOnID int64) (*DependencyChange, error)
	// LoadGraph reads the tickets a GraphQuery names, and what they need to
	// be drawn truthfully, in one snapshot rather than a query per node.
	LoadGraph(ctx context.Context, query *GraphQuery) (*GraphInput, error)
}

// LeaseStore is the claim path and everything hanging off a held lease.
type LeaseStore interface {
	Claim(ctx context.Context, agentID uuid.UUID, opts *ClaimOptions, ttl time.Duration, configs ConfigResolver) (*ClaimResult, error)
	Peek(ctx context.Context, agentID uuid.UUID, only *int64) (*ClaimResult, error)
	// QueueBlockage is Peek asked of every idle agent: see QueueBlockage.
	QueueBlockage(ctx context.Context) (*QueueBlockage, error)
	Heartbeat(ctx context.Context, agentID uuid.UUID, ttl time.Duration) error
	ServerStore
	// RecentRevocation is the context for an agent that holds nothing
	// because a human ended its latest lease within ttl, once the notice has
	// been delivered; nil when there is none. It changes nothing.
	RecentRevocation(ctx context.Context, agentID uuid.UUID, ttl time.Duration) (*RevocationContext, error)
	// InTransition runs fn in one transaction under the claim lock: the
	// service loads, decides and writes in it, never checking in one
	// transaction and writing in another.
	InTransition(ctx context.Context, fn func(tx TransitionTx) error) error
	// OnHeldLease is InTransition on the lease agentID holds, looked up only
	// once the claim lock is held; ErrNoLease when there is none.
	OnHeldLease(ctx context.Context, agentID uuid.UUID, fn func(tx TransitionTx, lease *HeldLease) error) error
	// ExtendScope adds paths to the held ticket. check is given the scopes the
	// ticket already has, inside the transaction, and refuses the extension by
	// returning an error.
	ExtendScope(ctx context.Context, agentID uuid.UUID, paths []model.Path, check func(held []string) error) (*ExtendResult, error)
	// NarrowScope removes paths from the held ticket and keeps the given
	// sub-paths of them, in the transaction that checks both.
	NarrowScope(ctx context.Context, agentID uuid.UUID, remove, keep []string) (*NarrowResult, error)
	AcquireLock(ctx context.Context, agentID uuid.UUID, name string) error
	// AwaitLock is AcquireLock that reserves the lock rather than taking it.
	AwaitLock(ctx context.Context, agentID uuid.UUID, name string) error
	ReleaseLock(ctx context.Context, agentID uuid.UUID) error
	LockState(ctx context.Context, name string) (*LockState, error)
	// LockNames is every lock the tenant has a holder or a queue on.
	LockNames(ctx context.Context) ([]string, error)
	ForceReleaseLock(ctx context.Context, name string) (*LockHolder, error)
	DropLockWaiter(ctx context.Context, name string, agentID uuid.UUID) error
	LeaseFor(ctx context.Context, agentID uuid.UUID) (*Lease, error)
	// LeaseEnd is what became of a lease, nil while it is held; read-only.
	LeaseEnd(ctx context.Context, leaseID int64) (*LeaseEnd, error)
	// WorkerSnapshot is the agent's latest lease with its ticket and attempt;
	// ErrNoLease when it never held one. Read-only.
	WorkerSnapshot(ctx context.Context, agentID uuid.UUID) (*WorkerSnapshot, error)
	// HeldScopes is every path of every scope that is held now: see
	// activeScopes, with each ticket's title and status.
	HeldScopes(ctx context.Context) ([]HeldScope, error)
	ReleaseLeasesFor(ctx context.Context, agentID uuid.UUID, reason string, acquiredBy time.Time) error
}

// CreateParams holds the fields a new ticket is filed with. A struct rather
// than a long positional argument list, so adding a field later doesn't
// churn every call site.
type CreateParams struct {
	Title string
	Body  string
	// Priority defaults to PriorityNormal when empty.
	Priority model.Priority
	// Paths are the scopes this ticket intends to write, already
	// normalized by Service. The human supplies these when filing; agents
	// narrow or extend them later.
	Paths []model.Path
	// CreatedByAgentID and CreatedByUserID are each nil when the ticket was
	// not filed by that kind of caller; at most one may be set.
	CreatedByAgentID *uuid.UUID
	CreatedByUserID  *uuid.UUID
	// NeedsDiscussion asks the first agent to claim it to plan and ask
	// before building.
	NeedsDiscussion bool
	// RequiresApproval holds the ticket's PR for a human's approval before
	// it merges.
	RequiresApproval bool
	// Status is preparing or open. Empty takes the default for whoever is
	// filing it -- see defaultCreateStatus.
	Status Status
	// BlockedOn are tickets the new one waits on, added with it in one
	// transaction: a ticket filed as open is never briefly claimable
	// without them. An open ticket with an unfinished one starts blocked.
	BlockedOn []int64
	// Roles are what the claiming agent will need. An agent may file a
	// ticket requiring roles it doesn't hold itself: adding a restriction
	// never widens what anyone can claim.
	Roles RoleRequirement
	// Intake marks a ticket an intake source filed; only ApplyIntake sets
	// it.
	Intake *model.IntakeRef
	// BaseRef is a full or short commit hash, or a tag, the ticket's work is
	// to start from instead of the default branch's head. Service.Create
	// resolves it in the ticket's repository; a branch is refused.
	BaseRef string
	// SkipBaseTag leaves the protective tag off when BaseRef is a commit hash.
	SkipBaseTag bool

	// baseCommit is BaseRef as Service.Create resolved it.
	baseCommit string
}

// UpdateParams carries only the fields a caller wants changed; nil means
// "leave alone", which is what lets the board patch a status without
// having to resend the whole ticket.
//
// No title or body: those change only through Edit and EditHeld, which
// record every change in the ticket's history. A second route that wrote
// them without a revision would make the history a sample, not a record.
type UpdateParams struct {
	Priority *model.Priority
	Status   *Status
	// NeedsDiscussion lets the human send a ticket back for another
	// planning pass, or drop the request for one. Setting it takes the
	// ticket from any agent working it, as the comment route does -- see
	// settingNeedsDiscussion.
	NeedsDiscussion *bool
	// RequiresApproval turns the human approval gate on or off.
	RequiresApproval *bool
	// Paths replaces the ticket's whole scope set when non-nil.
	Paths []model.Path
	// BaseRef re-points the base of a ticket nobody has claimed yet, to a
	// commit hash or tag; empty clears it back to the default branch's head.
	BaseRef *string

	// baseRef is BaseRef as Service.Update resolved it.
	baseRef *baseRefWrite
}

// HumanComment is a comment by the signed-in user.
type HumanComment struct {
	TicketID int64
	UserID   uuid.UUID
	Body     string
	// Affinity is how long an asking agent keeps first claim when the
	// comment answers its discussion.
	Affinity time.Duration
	// NeedsDiscussion, when non-nil, is written to the ticket with the
	// comment, atomically. true also takes the ticket from any agent
	// working it: the flag is only read at claim time, so an agent already
	// building would never see it.
	NeedsDiscussion *bool
	// Attempt is the attempt the person addressed the comment to, if they
	// named one, as ResolveAttempt returned it. Without it the comment goes
	// where commentTargetFor says.
	Attempt *AttemptRef
	// IdempotencyKey, when set, makes the comment repeatable: a second call
	// with the key returns the comment the first wrote and does nothing else
	// (not even the answer's effect on the ticket), so a caller that cannot
	// tell whether its post landed can post again.
	IdempotencyKey string
}

// HumanCommentResult is the comment and, when the flag was written with
// it, the ticket as that same transaction left it.
type HumanCommentResult struct {
	Comment *Comment
	// Ticket is nil unless HumanComment.NeedsDiscussion was set.
	Ticket *Ticket
}

type ListFilter struct {
	// Statuses limits the result; empty means every status.
	Statuses []Status
	// IDs limits the result to these tickets; empty means every ticket.
	IDs []int64
	// AwaitingPostReview limits the result to done tickets that merged
	// without a person's approval and that nobody has marked reviewed
	// since, most recently closed first. See Service.MarkPostReviewed.
	AwaitingPostReview bool
	// Blocked limits the result to tickets with an unfinished blocker -- the
	// claim path's rule, so the list agrees with what cannot be claimed.
	Blocked bool
	// PRLink narrows the result to tickets whose pr_url may be the change
	// with this canonical link (codehost.Registry.ChangeURL). A prefix
	// match only -- see Service.ticketForPR, which makes it exact.
	PRLink string
	// Text narrows the result to tickets containing every word of it,
	// case-insensitively, each anywhere in the title, the body, a comment
	// or a handoff. See searchTerms.
	Text string
	// TextSkipsAttempts leaves attempts' own comments and handoffs out of the
	// words Text matches, for a search made for an agent.
	TextSkipsAttempts bool
	// Priorities limits the result; empty means every priority.
	Priorities []model.Priority
	// Closed, Created and Updated bound closed_at, created_at and
	// updated_at.
	Closed, Created, Updated TimeRange
	// AttemptCards lists attempts, not tickets: one row per attempt in one of
	// the work statuses (in progress, discussion, review), each in its own
	// attempt's status and not the ticket's. Statuses then names the work
	// statuses to list, every one when empty, and any other is invalid. See
	// attempt_cards.go.
	AttemptCards bool
	// Sort orders the result and the cursor; nil is the list's default
	// (see defaultSort).
	Sort *Sort
	// After resumes a paged list after the ticket a previous page ended on;
	// see ListPage.
	After *PageCursor
	// Limit caps how many tickets come back, in the list's order; zero
	// means all of them.
	Limit int
	// Summary leaves out each ticket's body, which a list of cards does
	// not render. The ticket itself is GET /tickets/{id}.
	Summary bool
}

type Service struct {
	store  Store
	logger *slog.Logger
	// bulkMax is the most tickets one bulk action may name.
	bulkMax int
	// replayBatchMax is the most attempts one replay batch may add.
	replayBatchMax int
	// affinity is the window an asking agent keeps first claim for; see
	// DefaultResumeAffinity.
	affinity time.Duration
	// configs is nil unless WithConfigResolver set it.
	configs ConfigResolver
	// workspace is nil unless WithWorkspace set it.
	workspace Workspace
	// setup is nil unless WithSetupCheck set it.
	setup SetupCheck
	// hosts reads the links agents report and names the changes a webhook
	// reports; empty unless WithCodeHosts set it, so no link is a change.
	hosts *codehost.Registry
	// bases names the branch a ticket merges into; see NewBaseResolver.
	bases *BaseResolver
	// owners and events may be nil: see WithOwnerResolver and WithEvents.
	owners OwnerResolver
	events *audit.Recorder
	// tenantOwners and userNames may be nil: see WithTenantOwners and
	// WithUserNames.
	tenantOwners TenantOwners
	ownerGate    *authn.OwnerGate
	userNames    UserNames
	// cooldown bounds what a release holds a ticket out of the queue for.
	cooldown CooldownPolicy
	// now replaces the database's clock for cooldowns when set; see
	// cooldownNow.
	now func() time.Time
	// approvalHook is nil unless WithApprovalHook set it.
	approvalHook  ApprovalHook
	approvalCalls sync.WaitGroup
	// discussionHook is nil unless WithDiscussionHook set it.
	discussionHook  DiscussionHook
	discussionCalls sync.WaitGroup
}

// DefaultBulkMax is how many tickets a bulk action may name unless
// WithBulkMax says otherwise.
const DefaultBulkMax = 100

// ServiceOption configures a Service beyond what every caller needs.
type ServiceOption func(*Service)

// WithOwnerResolver sets who owns an owner-bound session. Without one every
// agent follows the worker rule.
func WithOwnerResolver(r OwnerResolver) ServiceOption {
	return func(s *Service) { s.owners = r }
}

// WithConfigResolver makes every lease record the configuration served, and
// keeps an agent whose session runs another harness than its variant's from
// claiming (see VariantClaimError). Without it nothing is recorded or
// checked.
func WithConfigResolver(r ConfigResolver) ServiceOption {
	return func(s *Service) { s.configs = r }
}

// WithCodeHosts says which code hosts' changes the service understands: a
// reported pr_url is checked against the ticket's repo for them, and a
// review or merge on one finds its ticket.
func WithCodeHosts(hosts *codehost.Registry) ServiceOption {
	return func(s *Service) { s.hosts = hosts }
}

// WithBulkMax caps how many tickets one bulk action may name. A bulk move
// holds the claim lock for the whole batch, blocking every agent's claim
// until it commits, so the cap bounds how long that is.
func WithBulkMax(n int) ServiceOption {
	return func(s *Service) { s.bulkMax = n }
}

// WithReplayBatchMax caps how many attempts one replay batch may add: every
// attempt is a worker run that costs money, so the cap bounds one click.
func WithReplayBatchMax(n int) ServiceOption {
	return func(s *Service) { s.replayBatchMax = n }
}

// WithResumeAffinity sets the resume-affinity window. Zero opens none, so a
// test can watch the window lapse without waiting for it.
func WithResumeAffinity(d time.Duration) ServiceOption {
	return func(s *Service) { s.affinity = d }
}

// WithCooldownPolicy sets what a release may hold a ticket out of the queue
// for. Without it a release requeues at once.
func WithCooldownPolicy(p CooldownPolicy) ServiceOption {
	return func(s *Service) { s.cooldown = p }
}

// WithClock replaces the clock cooldowns are timed by, for tests.
func WithClock(now func() time.Time) ServiceOption {
	return func(s *Service) { s.now = now }
}

// ResumeAffinity is the window in force.
func (s *Service) ResumeAffinity() time.Duration { return s.affinity }

func NewService(store Store, logHandler slog.Handler, opts ...ServiceOption) *Service {
	s := &Service{store: store, logger: slog.New(logHandler), bulkMax: DefaultBulkMax, replayBatchMax: DefaultReplayBatchMax, affinity: DefaultResumeAffinity, hosts: codehost.NewRegistry()}
	for _, opt := range opts {
		opt(s)
	}
	if n, ok := store.(discussionNotifier); ok && s.discussionHook != nil {
		n.onDiscussion(func(ctx context.Context, entries []*DiscussionEntry) {
			for _, e := range entries {
				s.notifyDiscussion(ctx, e)
			}
		})
	}
	s.bases = NewBaseResolver(DefaultBaseBranch, s.logger)
	s.ownerGate = authn.NewOwnerGate(s.tenantOwners, authn.WithRefusal(ErrNotTenantOwner))
	return s
}

func (s *Service) Create(ctx context.Context, params *CreateParams) (*Ticket, error) {
	if err := checkCreate(params); err != nil {
		return nil, err
	}
	if err := s.checkOneRepo(ctx, scopePaths(params.Paths)); err != nil {
		return nil, err
	}
	var base *resolvedBase
	if params.BaseRef = strings.TrimSpace(params.BaseRef); params.BaseRef != "" {
		var err error
		if base, err = s.resolveBaseRef(ctx, &Ticket{Paths: params.Paths}, params.BaseRef); err != nil {
			return nil, err
		}
		params.BaseRef, params.baseCommit = base.ref, base.sha
	}
	t, err := s.store.CreateTicket(ctx, params)
	if err != nil || base == nil || base.kind != codehost.RefCommit || params.SkipBaseTag {
		return t, err
	}
	if w := s.protectBase(ctx, t.ID, base); w != "" {
		t.Warnings = append(t.Warnings, w)
	}
	return t, nil
}

// checkCreate validates params and fills in what they leave to defaults.
func checkCreate(params *CreateParams) error {
	if !validTitle(params.Title) {
		return ErrEmptyTitle
	}
	if params.CreatedByAgentID != nil && params.CreatedByUserID != nil {
		return ErrMultipleCreators
	}
	if params.Priority == "" {
		params.Priority = model.PriorityNormal
	}
	if !params.Priority.Valid() {
		return fmt.Errorf("%w: priority %q", ErrInvalidEnum, params.Priority)
	}
	if params.Status == "" {
		params.Status = defaultCreateStatus(params)
	}
	if !validAtCreation(params.Status) {
		return fmt.Errorf("%w: a new ticket is preparing or open, not %q", ErrInvalidEnum, params.Status)
	}

	normalized, err := normalizePaths(params.Paths)
	if err != nil {
		return err
	}
	params.Paths = normalized

	// Each blocker is recorded as a comment by whoever filed the ticket, so
	// a ticket with no recorded creator cannot be filed with any.
	if len(params.BlockedOn) > 0 && params.CreatedByAgentID == nil && params.CreatedByUserID == nil {
		return ErrInvalidAuthor
	}
	params.BlockedOn = uniqueOf(params.BlockedOn)
	return params.Roles.clean()
}

// SetRequiredRoles replaces the roles a ticket requires, and how many of
// them the claiming agent needs. It can loosen a restriction, so it is a
// person's call: who must be a signed-in person, whatever transport asks.
// It affects future claims only; a lease already held stays.
func (s *Service) SetRequiredRoles(ctx context.Context, who *authn.User, ticketID int64, req *RoleRequirement) (*Ticket, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	if err := req.clean(); err != nil {
		return nil, err
	}
	err := s.store.InTransition(ctx, func(tx TransitionTx) error {
		return tx.SetRequiredRoles(ctx, ticketID, req)
	})
	if err != nil {
		return nil, err
	}
	return s.store.GetTicket(ctx, ticketID)
}

// defaultCreateStatus is where a ticket filed without an explicit status
// starts.
//
// An agent's ticket starts in Preparing, so a human sees it before any
// agent works it: otherwise the follow-ups one agent spins off are picked up
// and built by others without a person ever having looked. An agent asked
// specifically to file something as open says so explicitly.
//
// A person's defaults to open, which is what the board's REST route has
// always done; the board itself always says which it wants.
func defaultCreateStatus(params *CreateParams) Status {
	if params.CreatedByAgentID != nil {
		return StatusPreparing
	}
	return StatusOpen
}

func (s *Service) Get(ctx context.Context, id int64) (*Ticket, error) {
	return s.store.GetTicket(ctx, id)
}

// Validate refuses a filter the store cannot answer cheaply or meaningfully:
// an unknown status or priority, or search text over its bounds. Other
// stores that filter by ticket call it before using Predicate.
func (f *ListFilter) Validate() error {
	for _, st := range f.Statuses {
		if !st.Valid() {
			return fmt.Errorf("%w: status %q", ErrInvalidEnum, st)
		}
	}
	for _, p := range f.Priorities {
		if !p.Valid() {
			return fmt.Errorf("%w: priority %q", ErrInvalidEnum, p)
		}
	}
	if f.AttemptCards {
		for _, st := range f.Statuses {
			if !st.IsWork() {
				return fmt.Errorf("%w: attempt cards are of the work statuses, not %q", ErrInvalidPage, st)
			}
		}
	}
	return validSearchText(f.Text)
}

func (s *Service) List(ctx context.Context, filter *ListFilter) ([]Ticket, error) {
	if filter == nil {
		filter = &ListFilter{}
	}
	if err := filter.Validate(); err != nil {
		return nil, err
	}
	if filter.Limit < 0 {
		return nil, fmt.Errorf("%w: limit %d", ErrInvalidSearch, filter.Limit)
	}
	return s.store.ListTickets(ctx, filter)
}

func (s *Service) Update(ctx context.Context, id int64, params *UpdateParams) (*Ticket, error) {
	if err := s.PrepareUpdate(ctx, params); err != nil {
		return nil, err
	}
	base, err := s.prepareBaseRefEdit(ctx, id, params)
	if err != nil {
		return nil, err
	}
	// Every edit is a transition, under the claim lock, even one that moves
	// nothing: see UpdateInTx.
	err = s.store.InTransition(ctx, func(tx TransitionTx) error { return UpdateInTx(ctx, tx, id, params) })
	if err != nil {
		return nil, err
	}
	t, err := s.store.GetTicket(ctx, id)
	if err != nil || base == nil || base.kind != codehost.RefCommit {
		return t, err
	}
	if w := s.protectBase(ctx, id, base); w != "" {
		t.Warnings = append(t.Warnings, w)
	}
	return t, nil
}

// InTransition runs fn in one transaction under the claim lock, for a caller
// that composes the human's moves (see human_tx.go) with writes of its own:
// they commit or roll back together.
func (s *Service) InTransition(ctx context.Context, fn func(tx TransitionTx) error) error {
	return s.store.InTransition(ctx, fn)
}

// MarkReady is the human's Ready on a Preparing ticket: it enters the queue
// as open, or blocked while any dependency is unfinished -- the same rule a
// human's answer to a discussion uses. The only way out of Preparing into
// the queue; see Status.humanMove.
func (s *Service) MarkReady(ctx context.Context, id int64) (*Ticket, error) {
	var ready *Ticket
	err := s.store.InTransition(ctx, func(tx TransitionTx) error {
		var err error
		ready, err = MarkTicketReady(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.noteBehind(ctx, ready)
	return ready, nil
}

// AddComment records a comment. One from the signed-in user is also the
// answer to a discussion the ticket may be waiting on, and puts it back on
// the queue -- see humanCommentEffect. An agent's comment never does: an
// agent asking a follow-up is still waiting on the human.
func (s *Service) AddComment(ctx context.Context, ticketID int64, author Author, body string) (*Comment, error) {
	if body == "" {
		return nil, ErrEmptyComment
	}
	if !author.valid() {
		return nil, ErrInvalidAuthor
	}
	if author.UserID != nil {
		result, err := s.addHumanComment(ctx, &HumanComment{
			TicketID: ticketID, UserID: *author.UserID, Body: body, Affinity: s.affinity,
		})
		if err != nil {
			return nil, err
		}
		return result.Comment, nil
	}
	return s.store.AddComment(ctx, ticketID, Author{AgentID: author.AgentID}, body)
}

// PostComment is the signed-in user's comment as hc describes it, with what it
// implies (see CommentAsUser). AddComment and CommentSettingNeedsDiscussion
// are its two common shapes.
func (s *Service) PostComment(ctx context.Context, hc *HumanComment) (*HumanCommentResult, error) {
	if hc.Body == "" {
		return nil, ErrEmptyComment
	}
	hc.Affinity = s.affinity
	return s.addHumanComment(ctx, hc)
}

// commentBarrier is a test-only seam, like claimBarrier. Production leaves
// it nil. It runs inside addHumanComment between the comment's insert and
// the needs_discussion write, so a test can hold the transaction there and
// show that no claim sees the one without the other.
var commentBarrier func()

// addHumanComment records the signed-in user's comment and makes what it
// implies -- see humanCommentEffect -- in one transaction under the claim
// lock, because the status it may write is what Claim reads, and because
// the answer and the reopen must not come apart.
func (s *Service) addHumanComment(ctx context.Context, hc *HumanComment) (*HumanCommentResult, error) {
	var result *HumanCommentResult
	err := s.store.InTransition(ctx, func(tx TransitionTx) error {
		var err error
		result, err = CommentAsUser(ctx, tx, hc)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ListAgentContexts returns a ticket's handoffs, oldest first. The last is
// the one to resume from; earlier ones are history.
func (s *Service) ListAgentContexts(ctx context.Context, ticketID int64) ([]AgentContext, error) {
	return s.store.ListAgentContexts(ctx, ticketID)
}

func (s *Service) ListComments(ctx context.Context, ticketID int64) ([]Comment, error) {
	return s.store.ListComments(ctx, ticketID)
}
