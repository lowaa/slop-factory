package pendingaction

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// MaxBatch bounds how many proposals one approve-all decides, so a request
// cannot hold the board's transitions for long.
const MaxBatch = 100

// MaxReasonBytes bounds a rejection's reason: the proposer reads it back.
const MaxReasonBytes = 1_000

// CauseFor is the cause the status changes an approval makes are recorded
// with, so history shows which proposal a move came from.
func CauseFor(id uuid.UUID) string { return "proposal:" + id.String() }

// AutoCauseFor is CauseFor for an approval made by the approver's standing
// auto-approve choice.
func AutoCauseFor(id uuid.UUID) string { return CauseFor(id) + " auto_approved" }

// StewardCauseFor is CauseFor for an approval the steward made under the
// owner's live-mode settings.
func StewardCauseFor(id uuid.UUID) string { return CauseFor(id) + " steward_approved" }

// Approve applies a pending proposal as the approver would have, in one
// transaction under the claim lock: the same ticket code the board's routes
// run (see ticket.ApproveReview and friends), attributed to the approver.
// Only what the proposal stored is applied; there is nothing to pass in that
// could change it.
//
// The ticket must still be what the proposal was made against -- status,
// revision, PR and review cycle -- and not held by an agent, and the
// proposer must still be an enabled console with a live full-access grant.
// A proposal that fails those is settled as stale or voided, and one the
// ticket's rules refuse as failed, with the reason in its outcome; the
// result is then that proposal and an error wrapping ErrStale, ErrVoided or
// ErrRefused. An approval of a proposal already decided, or expired, is
// ErrNotPending, so a replay or a double click applies once.
//
// The caller has established the approver is a signed-in person of the
// tenant in ctx: this takes no say from anyone else.
func (s *Service) Approve(ctx context.Context, approver *authn.User, id uuid.UUID) (*Action, error) {
	if err := authn.RequirePerson(approver); err != nil {
		return nil, err
	}
	return s.approve(ctx, approver, id, byClick)
}

// approve is Approve, where mode says whether the approval is a click, the
// person's standing choice or the steward's: the checks are the same, only
// the record differs.
func (s *Service) approve(ctx context.Context, approver *authn.User, id uuid.UUID, mode approvalMode) (*Action, error) {
	if s.tickets == nil || s.grants == nil {
		return nil, ErrNotConfigured
	}
	proposal, err := s.store.Get(ctx, id, s.now())
	if err != nil {
		return nil, err
	}
	if err := s.mayDecide(ctx, approver, proposal.Kind); err != nil {
		return nil, err
	}
	if mode != byClick && !proposal.Kind.Spec().AutoApprovable {
		return nil, fmt.Errorf("%w: %s is never auto-approved", ErrNotApprover, proposal.Kind)
	}
	if refused, err := s.prepareApproval(ctx, approver, id, mode); err != nil {
		return refused, err
	}
	var now time.Time
	var settled *Action
	var refusal error
	var approval *ticket.Approval
	// closing is set for a close_merged proposal that passed its checks: its
	// application reads the code host, which a transaction must not wait on,
	// so it is made after this one, and the proposal settles with it.
	var closing *Action
	err = s.tickets.InTransition(ctx, func(tx ticket.TransitionTx) error {
		approval, closing = nil, nil
		q := tx.Querier()
		// Read once the locks are held: waiting for them can outlast the
		// proposal's expiry.
		now = s.now()
		a, err := s.store.LockForDecision(ctx, q, id, now)
		if err != nil {
			return err
		}
		if a.State != StatePending {
			return fmt.Errorf("%w: it is %s", ErrNotPending, a.State)
		}
		// Asked again now the proposal is locked, so a role lost while this
		// waited for the locks is not used. The role lives in another
		// service's table, so this narrows the window rather than closing it.
		if err := s.mayDecide(ctx, approver, a.Kind); err != nil {
			return err
		}
		standing, err := s.store.Standing(ctx, q, a.AgentID)
		if err != nil {
			return err
		}
		state, reason, err := s.recheck(ctx, tx, approver, a, standing)
		if err != nil {
			return err
		}
		if state != "" {
			refusal = refusalFor(state)
			settled, err = s.store.Settle(ctx, q, a.ID, state, reason, approver.ID, now)
			return err
		}
		if a.Kind == KindCloseMerged {
			closing = a
			return nil
		}
		if err := s.apply(ctx, tx, approver.ID, a, standing.Name, mode, func(a *ticket.Approval) { approval = a }); err != nil {
			return &applyError{err}
		}
		settled, err = s.store.Settle(ctx, q, a.ID, StateApproved, mode.outcome(), approver.ID, now)
		return err
	})
	var failed *applyError
	switch {
	case errors.As(err, &failed):
		// The ticket's work was rolled back with the transaction; the
		// refusal is recorded on its own.
		settled, err = s.store.SettleOwn(ctx, id, StateFailed, failed.err.Error(), approver.ID, s.now())
		if err != nil {
			return nil, err
		}
		s.refused(ctx, approver, settled, mode)
		return settled, fmt.Errorf("%w: %w", ErrRefused, failed.err)
	case err != nil:
		return nil, err
	case refusal != nil:
		s.refused(ctx, approver, settled, mode)
		return settled, fmt.Errorf("%w: %s", refusal, settled.Outcome)
	}
	if closing != nil {
		if settled, err = s.closeMerged(ctx, approver, closing); err != nil {
			return settled, err
		}
	}
	// Only now: the transaction that approved the review has committed.
	s.tickets.NotifyApproved(ctx, approval)
	s.record(ctx, audit.ProposalApproved, "proposal", settled.ID, "ticket", settled.ticketID(), "kind", settled.Kind,
		"agent_id", settled.AgentID, "user_id", approver.ID, "tenant", approver.TenantID, "auto_approved", mode != byClick)
	if settled.Kind == KindManifestEdit {
		s.record(ctx, audit.ManifestChanged, "proposal", settled.ID, "agent_id", settled.AgentID, "user_id", approver.ID,
			"tenant", approver.TenantID, "base_revision", settled.Payload.ManifestEdit.BaseRevision)
	}
	// The steward's approval is not the owner's decision, which is what the
	// agreement report compares the steward's verdicts with.
	if mode != bySteward {
		s.recordDecision(ctx, settled, "approved")
	}
	return settled, nil
}

// approvalMode is who an approval is made for: a person's click, their
// standing auto-approve choice, or the steward acting under its live mode.
type approvalMode int

const (
	byClick approvalMode = iota
	byAutoApprove
	bySteward
)

func (m approvalMode) outcome() string {
	switch m {
	case byAutoApprove:
		return OutcomeAutoApproved
	case bySteward:
		return OutcomeStewardApproved
	}
	return ""
}

func (m approvalMode) cause(id uuid.UUID) string {
	switch m {
	case byAutoApprove:
		return AutoCauseFor(id)
	case bySteward:
		return StewardCauseFor(id)
	}
	return CauseFor(id)
}

type applyError struct{ err error }

func (e *applyError) Error() string { return e.err.Error() }
func (e *applyError) Unwrap() error { return e.err }

func refusalFor(state State) error {
	if state == StateVoided {
		return ErrVoided
	}
	return ErrStale
}

func (s *Service) refused(ctx context.Context, approver *authn.User, a *Action, mode approvalMode) {
	s.record(ctx, audit.ProposalRefused, "proposal", a.ID, "ticket", a.ticketID(), "kind", a.Kind,
		"agent_id", a.AgentID, "user_id", approver.ID, "tenant", approver.TenantID, "state", a.State, "outcome", a.Outcome, "auto_approved", mode != byClick)
}

// recheck is everything that may have changed since the proposal was made.
// It returns the state to settle the proposal in, and why, or no state when
// the approval may go ahead.
//
// A ready proposal on a ticket with an edit pending is neither applied nor
// settled: it returns ErrBlocked and stays pending. Applying it first would
// make the edit stale (the status moved) and too late (an agent may claim the
// ticket); applying the edit first would make it stale (the revision moved).
// Deciding the edit, or the proposer's withdrawing it, releases it.
func (s *Service) recheck(ctx context.Context, tx ticket.TransitionTx, approver *authn.User, a *Action, standing *Standing) (State, string, error) {
	if !standing.Proposer || !standing.Enabled {
		return StateVoided, "the agent that proposed it is no longer an enabled console or conductor", nil
	}
	live, err := s.grants.HasFullAccessGrant(ctx, approver.TenantID, a.AgentID)
	if err != nil {
		return "", "", err
	}
	if !live {
		return StateVoided, "the agent that proposed it has no live grant", nil
	}
	if a.TicketID == nil {
		if rechecker, ok := s.tenantAppliers[a.Kind].(TenantRechecker); ok {
			return rechecker.Recheck(ctx, tx, a)
		}
		return "", "", nil
	}
	if a.Attempt != nil {
		// The attempt may have finished or been closed since: there is
		// nothing left to answer or decide on.
		_, err := ticket.ResolveAttempt(ctx, tx.Querier(), a.ticketID(), a.Attempt)
		if errors.Is(err, ticket.ErrAttemptNotLive) || errors.Is(err, ticket.ErrNoSuchAttempt) {
			return StateStale, fmt.Sprintf("attempt %d is no longer live: %s", *a.Attempt, err), nil
		}
		if err != nil {
			return "", "", err
		}
	}
	basis, held, err := s.store.LiveBasis(ctx, tx.Querier(), a.ticketID(), a.Attempt)
	if err != nil {
		return "", "", err
	}
	if held {
		return StateStale, "an agent now holds the ticket", nil
	}
	if reason := a.Basis.differsFrom(basis); reason != "" {
		return StateStale, "the ticket has changed since it was proposed: " + reason, nil
	}
	if a.Kind == KindReady {
		pending, err := s.store.HasPendingEdit(ctx, tx.Querier(), a.ticketID(), s.now())
		if err != nil {
			return "", "", err
		}
		if pending {
			return "", "", ErrBlocked
		}
	}
	if a.Kind == KindEdit {
		live, err := tx.Ticket(ctx, a.ticketID())
		if err != nil {
			return "", "", err
		}
		if reason := a.Payload.Edit.drift(live); reason != "" {
			return StateStale, "the ticket has changed since it was proposed: " + reason, nil
		}
	}
	return "", "", nil
}

func (s *Service) apply(ctx context.Context, tx ticket.TransitionTx, approver uuid.UUID, a *Action, agentName string, mode approvalMode, approved func(*ticket.Approval)) error {
	ctx = ticket.WithCause(ticket.WithUser(ctx, approver), mode.cause(a.ID))
	footer := draftedBy(agentName)
	switch a.Kind {
	case KindDiscussionAnswer:
		_, err := ticket.CommentAsUser(ctx, tx, discussionAnswer(a.ticketID(), a.attemptRef(), approver, a.Payload.Body, agentName, s.tickets.ResumeAffinity()))
		return err
	case KindReady:
		_, err := ticket.MarkTicketReady(ctx, tx, a.ticketID())
		return err
	case KindStatus:
		if _, err := tx.Comment(ctx, a.ticketID(), &ticket.NewComment{
			By: ticket.UserAuthor(approver), Body: a.Payload.Body + footer,
		}); err != nil {
			return err
		}
		_, err := ticket.MoveTicketTo(ctx, tx, a.ticketID(), a.Payload.Status)
		return err
	case KindEdit:
		return s.applyEdit(ctx, tx, approver, a)
	case KindReviewDecision:
		if a.Payload.Decision == DecisionApprove {
			if _, err := tx.Comment(ctx, a.ticketID(), &ticket.NewComment{
				By: ticket.UserAuthor(approver), Body: "Approved." + footer, Attempt: a.attemptRef(),
			}); err != nil {
				return err
			}
			_, approval, err := ticket.ApproveReviewRecorded(ctx, tx, a.ticketID(), a.attemptRef(), approver, s.tickets.ResumeAffinity())
			if err == nil {
				approved(approval)
			}
			return err
		}
		_, err := ticket.RequestReviewChanges(ctx, tx, a.ticketID(), a.attemptRef(), approver, a.Payload.Body+footer, s.tickets.ResumeAffinity())
		return err
	}
	if applier := s.tenantAppliers[a.Kind]; applier != nil && a.Kind.Spec().Subject == SubjectTenant {
		return applier.Apply(ctx, tx, approver, a)
	}
	return fmt.Errorf("unknown kind %q", a.Kind)
}

// mayDecide is the role check a kind puts on whoever approves or rejects it,
// made against the database rather than the session so a demoted owner loses
// it at once. It fails closed: a kind that needs a role this service cannot
// look up is refused.
func (s *Service) mayDecide(ctx context.Context, approver *authn.User, kind Kind) error {
	if kind.Spec().Approver != RoleOwner {
		return nil
	}
	if s.roles == nil {
		return ErrNotApprover
	}
	owner, err := s.roles.IsTenantOwner(ctx, approver.ID, approver.TenantID)
	if err != nil {
		return fmt.Errorf("pending action: read role: %w", err)
	}
	if !owner {
		return ErrNotApprover
	}
	return nil
}

// decidableKinds are the kinds the approver may decide, for a decision made
// over a set of proposals; nil means every kind, including any the table does
// not know, which only a caller who may decide the strictest kind gets.
func (s *Service) decidableKinds(ctx context.Context, approver *authn.User) ([]Kind, error) {
	err := s.mayDecide(ctx, approver, "")
	if err == nil {
		return nil, nil
	}
	if !errors.Is(err, ErrNotApprover) {
		return nil, err
	}
	kinds := []Kind{}
	for _, k := range Kinds {
		if k.Spec().Approver != RoleOwner {
			kinds = append(kinds, k)
		}
	}
	return kinds, nil
}

// draftedBy is the line that keeps who wrote a comment in its history,
// though it posts as the approver.
// discussionAnswer is the comment that answers a discussion as the approver:
// the one place its shape is decided, for an approved proposal and for the
// owner's answer with the steward.
func discussionAnswer(ticketID int64, attempt *ticket.AttemptRef, approver uuid.UUID, body, agentName string, affinity time.Duration) *ticket.HumanComment {
	return &ticket.HumanComment{TicketID: ticketID, UserID: approver, Body: body + draftedBy(agentName), Affinity: affinity, Attempt: attempt}
}

func draftedBy(agentName string) string {
	return "\n\n_Drafted by " + agentName + "._"
}

func (b *Basis) differsFrom(now *Basis) string {
	var what []string
	if b.Status != now.Status {
		what = append(what, fmt.Sprintf("it is now %s, not %s", now.Status, b.Status))
	}
	if b.Revision != now.Revision {
		what = append(what, "its title or text was edited")
	}
	if !equalPtr(b.PRURL, now.PRURL) {
		what = append(what, "its PR changed")
	}
	if (b.ReviewStartedAt == nil) != (now.ReviewStartedAt == nil) ||
		(b.ReviewStartedAt != nil && !b.ReviewStartedAt.Equal(*now.ReviewStartedAt)) {
		what = append(what, "its review started again")
	}
	return strings.Join(what, "; ")
}

func equalPtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Reject ends a pending proposal without applying it; reason is shown to
// its proposer in the outcome.
func (s *Service) Reject(ctx context.Context, approver *authn.User, id uuid.UUID, reason string) (*Action, error) {
	if err := authn.RequirePerson(approver); err != nil {
		return nil, err
	}
	reason, err := cleanReason(reason)
	if err != nil {
		return nil, err
	}
	proposal, err := s.store.Get(ctx, id, s.now())
	if err != nil {
		return nil, err
	}
	if err := s.mayDecide(ctx, approver, proposal.Kind); err != nil {
		return nil, err
	}
	rejected, err := s.store.Reject(ctx, []uuid.UUID{id}, nil, approver.ID, reason, s.now())
	if err != nil {
		return nil, err
	}
	if len(rejected) == 0 {
		found, err := s.store.Get(ctx, id, s.now())
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: it is %s", ErrNotPending, found.State)
	}
	s.rejected(ctx, approver, &rejected[0])
	return &rejected[0], nil
}

// RejectAll rejects the pending proposals named by ids, or every pending one
// when ids is nil, each with the same reason. A caller that shows a filtered
// list names what it shows, so nothing it hides is decided.
func (s *Service) RejectAll(ctx context.Context, approver *authn.User, ids []uuid.UUID, reason string) ([]Action, error) {
	if err := authn.RequirePerson(approver); err != nil {
		return nil, err
	}
	if err := checkBatchIDs(ids); err != nil {
		return nil, err
	}
	reason, err := cleanReason(reason)
	if err != nil {
		return nil, err
	}
	kinds, err := s.decidableKinds(ctx, approver)
	if err != nil {
		return nil, err
	}
	rejected, err := s.store.Reject(ctx, ids, kinds, approver.ID, reason, s.now())
	if err != nil {
		return nil, err
	}
	for i := range rejected {
		s.rejected(ctx, approver, &rejected[i])
	}
	return rejected, nil
}

func (s *Service) rejected(ctx context.Context, approver *authn.User, a *Action) {
	s.recordDecision(ctx, a, "rejected")
	s.record(ctx, audit.ProposalRejected, "proposal", a.ID, "ticket", a.ticketID(), "kind", a.Kind,
		"agent_id", a.AgentID, "user_id", approver.ID, "tenant", approver.TenantID)
}

// cleanReason is the outcome a rejection records: the person's words, or
// that it was rejected.
func cleanReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if len(reason) > MaxReasonBytes {
		return "", invalid("a reason is at most %d bytes", MaxReasonBytes)
	}
	if reason == "" {
		return "rejected", nil
	}
	return "rejected: " + reason, nil
}

// BatchKinds are the kinds approve-all takes: the ones whose effect is
// small and the person has already read in full. A review decision leads to
// a merge and a cancellation loses work, so each of those is approved on its
// own.
var BatchKinds = []Kind{KindReady, KindDiscussionAnswer}

// Result is how one proposal in a batch ended.
type Result struct {
	ID      uuid.UUID
	State   State
	Outcome string
	// Err is why it was refused, when it was.
	Err error
}

// checkBatchIDs bounds a batch that names its proposals.
func checkBatchIDs(ids []uuid.UUID) error {
	if len(ids) > MaxBatch {
		return invalid("a batch names at most %d proposals", MaxBatch)
	}
	return nil
}

// ApproveAll approves every pending proposal of one kind, each as its own
// approval, and reports each one's result. kind must be one of BatchKinds:
// kinds are never mixed. ids narrows it to those proposals; nil is every
// pending proposal of the kind.
func (s *Service) ApproveAll(ctx context.Context, approver *authn.User, kind Kind, ids []uuid.UUID) ([]Result, error) {
	if err := authn.RequirePerson(approver); err != nil {
		return nil, err
	}
	if err := checkBatchIDs(ids); err != nil {
		return nil, err
	}
	if !slices.Contains(BatchKinds, kind) {
		return nil, invalid("%q cannot be approved all at once", kind)
	}
	pending, err := s.List(ctx, &ListFilter{States: []State{StatePending}, Kind: &kind, IDs: ids, Limit: MaxBatch})
	if err != nil {
		return nil, err
	}
	// Oldest first, as a person reading the queue would.
	slices.Reverse(pending)
	results := make([]Result, 0, len(pending))
	for i := range pending {
		a, err := s.Approve(ctx, approver, pending[i].ID)
		r := Result{ID: pending[i].ID, Err: err}
		if a != nil {
			r.State, r.Outcome = a.State, a.Outcome
		}
		if err != nil && a == nil {
			// Decided by someone between the listing and here, or a failure
			// that left the proposal as it was.
			r.State = StatePending
			if found, gerr := s.Get(ctx, pending[i].ID); gerr == nil {
				r.State, r.Outcome = found.State, found.Outcome
			}
		}
		results = append(results, r)
	}
	return results, nil
}
