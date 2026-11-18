package ticket

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// A transition is decided in Go and persisted by the store, inside one
// transaction the store provides under the claim lock (see
// LeaseStore.InTransition, or OnHeldLease for the holder's own). The decision is a pure function of the ticket as
// loaded in that transaction: it refuses, or returns the Change to make.
// What a Change implies -- in what order its writes happen -- is decided
// once, by Change.apply, so no transition can record its comment but forget
// to end the lease.

// Change is everything one transition does: the ticket's own row, and the
// rows that go with it.
type Change struct {
	Ticket TicketWrite
	// Comment is recorded on the ticket, when non-nil.
	Comment *NewComment
	// Handoff is recorded as the holding agent's context, when non-empty.
	Handoff string
	// ReleaseReason ends the held lease with this reason, when non-nil.
	ReleaseReason *string
	// BlockedOn adds a dependency, with the rules every added blocker has
	// (see addBlocker). Added after the lease ends and before the ticket's
	// row is written, so the status the write requeues to sees it.
	BlockedOn *int64
	// BlockedNote is appended to the comment recording BlockedOn, so a
	// release that names a blocker and a reason reads as one comment. When
	// the edge already existed there is no such comment, and the note is
	// posted on its own.
	BlockedNote string
	// Unblock reopens every blocked ticket whose dependencies are now all
	// finished, after the ticket's row is written.
	Unblock bool
	// Revoke takes the ticket from any agent still holding a lease on it,
	// telling the agent the status the ticket landed on: last, so that is
	// the status after the write and anything it unblocked.
	Revoke bool
}

// NewComment is a comment a transition records.
type NewComment struct {
	By   Author
	Body string
	// Attempt, when set, is the attempt the comment is addressed to, already
	// resolved (ResolveAttempt). Without it the comment goes where
	// commentTargetFor says.
	Attempt *AttemptRef
	// IdempotencyKey, when set, names a comment the caller may repeat; see
	// CommentAsUser.
	IdempotencyKey string
}

// TicketWrite is a transition's write to the ticket's own row. Its zero
// value leaves the row as it is, apart from updated_at, which every write
// moves.
type TicketWrite struct {
	Status  Target
	Closing Closing
	// NeedsDiscussion is written when non-nil. Setting it starts every
	// attempt's discussion over; clearing it is the person's word, for all.
	NeedsDiscussion *bool
	// Discussed records that the attempt written has had its discussion, and
	// clears the ticket's needs_discussion once every live attempt has. An
	// agent's way to satisfy the flag: one attempt's question is not its
	// siblings'.
	Discussed bool
	// PRURL is written when non-nil; nil keeps whatever link the ticket has.
	PRURL      *string
	FirstClaim FirstClaim
	Review     ReviewStep
	// KeepsLanding leaves the ticket's landing row to the caller, who removes
	// it in the same transaction and records why (closing a merged change).
	KeepsLanding bool
	// ClearsPostReview forgets any post-merge review of the ticket's change.
	ClearsPostReview bool
	// Cooldown holds the ticket out of the queue, and extends the streak of
	// releases that did. Any other write that sets a status ends both: a
	// person's move, or an exit other than a cooldown release, is the
	// streak's end.
	Cooldown *CooldownWrite
}

// Closing is a transition's effect on closed_at. The zero value leaves it
// as it is.
type Closing int

const (
	closingKept Closing = iota
	closingClosed
	closingReopened
)

// Closes sets closed_at to now.
func Closes() Closing { return closingClosed }

// Reopens clears closed_at.
func Reopens() Closing { return closingReopened }

// ReviewStep is a transition's effect on the review columns. The zero value
// leaves them as they are.
type ReviewStep struct {
	kind reviewStepKind
	by   uuid.UUID
}

type reviewStepKind int

const (
	reviewKept reviewStepKind = iota
	reviewStarted
	reviewApproved
	reviewChangesRequested
	reviewDiscarded
)

// ReviewStarts begins a review cycle: any earlier outcome is cleared, so an
// approval is always of the change as it stands now, and the cycle's start
// is recorded -- see inCurrentReview.
func ReviewStarts() ReviewStep { return ReviewStep{kind: reviewStarted} }

// ReviewDiscards drops the outcome of the latest review without starting a
// new cycle: an approval, or a request for changes, was of a change that is
// no longer the ticket's.
func ReviewDiscards() ReviewStep { return ReviewStep{kind: reviewDiscarded} }

// ReviewApproves records userID's approval as the cycle's outcome.
func ReviewApproves(userID uuid.UUID) ReviewStep {
	return ReviewStep{kind: reviewApproved, by: userID}
}

// ReviewRequestsChanges records a request for changes as the cycle's
// outcome, clearing any approval.
func ReviewRequestsChanges() ReviewStep { return ReviewStep{kind: reviewChangesRequested} }

// Target is the status a transition moves a ticket to: a given one, or
// back to the queue. The zero value leaves the status alone.
type Target struct {
	status  Status
	toQueue bool
}

// To is a move to status.
func To(status Status) Target { return Target{status: status} }

// ToQueue is a move back to the queue: open, or blocked while any
// dependency is unfinished. The store resolves it with the same predicate
// Claim's candidate query uses, so "blocked" has one definition rather than
// a Go one free to drift from the SQL one.
func ToQueue() Target { return Target{toQueue: true} }

// Status is the explicit status moved to, if there is one.
func (t Target) Status() (Status, bool) { return t.status, t.status != "" }

// IsQueue reports whether this is ToQueue.
func (t Target) IsQueue() bool { return t.toQueue }

// FirstClaim is a transition's effect on who may claim a ticket first and
// for how long (resume_agent_id and affinity_until). The zero value leaves
// both as they are.
type FirstClaim struct {
	kind   firstClaimKind
	agent  uuid.UUID
	window time.Duration
}

type firstClaimKind int

const (
	firstClaimKept firstClaimKind = iota
	firstClaimCleared
	firstClaimOwed
	firstClaimWindow
)

// FirstClaimCleared gives nobody first claim.
func FirstClaimCleared() FirstClaim { return FirstClaim{kind: firstClaimCleared} }

// FirstClaimOwedTo records agentID as the one owed the ticket back, with no
// window open yet: that opens when a human answers.
func FirstClaimOwedTo(agentID uuid.UUID) FirstClaim {
	return FirstClaim{kind: firstClaimOwed, agent: agentID}
}

// FirstClaimWindowOf opens the window in which whoever the ticket is owed
// to claims it first, for d from now: a human has answered them. Only a
// worker is given one (see ticketWriteSets). The window
// is timed by the database's clock, which every claim compares it with,
// not by this process's.
func FirstClaimWindowOf(d time.Duration) FirstClaim {
	return FirstClaim{kind: firstClaimWindow, window: d}
}

// TransitionTx is the store's side of a transition: primitives run inside
// one transaction under the claim lock, which decide nothing beyond what
// only SQL can decide atomically.
type TransitionTx interface {
	// Querier is the transaction itself, for a caller that writes rows of its
	// own in it -- see HumanMoves.
	Querier() db.Querier
	// Ticket loads a ticket as this transaction sees it.
	Ticket(ctx context.Context, id int64) (*Ticket, error)
	// ForAttempt is this transaction as a person's decision about one
	// attempt: the ticket it loads shows that attempt's status and the fields
	// that live on it (its PR, its review), the status it writes moves that
	// attempt alone, and a comment it records is that attempt's. The attempt
	// must be of the ticket the transition is for; it is the same transaction
	// and claim lock.
	ForAttempt(ref *AttemptRef) TransitionTx
	// Dequeue removes the ticket's landing row in this transaction; see
	// MergeQueue.
	Dequeue(ctx context.Context, req *DequeueRequest) (bool, error)
	// Tickets loads each of ids that exists, in no particular order, in one
	// query per table rather than one per ticket.
	Tickets(ctx context.Context, ids []int64) ([]Ticket, error)
	// WriteTicket makes w on ticket id.
	WriteTicket(ctx context.Context, id int64, w *TicketWrite) error
	// UpdateFields writes the fields of params other than the status, which
	// only a transition writes; params.Status must be nil. Paths replace
	// the ticket's filed paths; the extended ones an agent was granted stay.
	UpdateFields(ctx context.Context, id int64, params *UpdateParams) error
	// HoldingScope is which of ids hold their scope: in review, or with a
	// live lease.
	HoldingScope(ctx context.Context, ids []int64) (map[int64]bool, error)
	// ScopeConflicts reports, in id order, each of ids that holds its scope
	// and overlaps a scope another ticket holds, with the first such scope.
	// Tickets holding no scope themselves are skipped.
	ScopeConflicts(ctx context.Context, ids []int64) ([]Blocked, error)
	// SetRequiredRoles replaces the ticket's required roles and match mode.
	SetRequiredRoles(ctx context.Context, id int64, req *RoleRequirement) error
	Comment(ctx context.Context, ticketID int64, c *NewComment) (*Comment, error)
	Handoff(ctx context.Context, ticketID int64, agentID uuid.UUID, body string) error
	// EndLease releases a lease and drops any lock it holds.
	EndLease(ctx context.Context, leaseID int64, reason string) error
	// AddBlocker adds a dependency, refusing one that would close a cycle,
	// and reports whether the edge is new. note, when set, is appended to the
	// comment recorded for a new edge.
	AddBlocker(ctx context.Context, by Author, ticketID, dependsOnID int64, note string) (bool, error)
	// UnblockSatisfied reopens every blocked ticket with nothing unfinished
	// left to wait on.
	UnblockSatisfied(ctx context.Context) error
	// CooldownStreak is the ticket's run of back-to-back cooldown releases.
	CooldownStreak(ctx context.Context, ticketID int64) (CooldownStreak, error)
	// RevokeLeases ends every unreleased lease on the ticket as revoked to
	// the status the ticket has now -- see revokeLeaseOn.
	RevokeLeases(ctx context.Context, ticketID int64) error
}

// HeldLease is the lease an agent holds, as OnHeldLease found it under the
// claim lock.
type HeldLease struct {
	ID       int64
	TicketID int64
	AgentID  uuid.UUID
	// Attempt is the number of the attempt the lease is for: what the
	// holder's exit moves.
	Attempt int
}

// apply makes c on ticketID through tx. The order is fixed here rather than
// by each transition: the comment and handoff first, then the lease, then a
// new blocker, the ticket's row -- which may requeue against that blocker --
// whatever the new status unblocks, and last any lease revoked to where the
// ticket landed.
func (c *Change) apply(ctx context.Context, tx TransitionTx, lease *HeldLease, ticketID int64) error {
	if c.Comment != nil {
		if _, err := tx.Comment(ctx, ticketID, c.Comment); err != nil {
			return err
		}
	}
	if lease == nil && (c.Handoff != "" || c.ReleaseReason != nil || c.BlockedOn != nil) {
		// Each is the holding agent's: its context, its lease, its blocker.
		return fmt.Errorf("ticket: a change to #%d by its holder, made without the holder's lease", ticketID)
	}
	if c.Handoff != "" {
		if err := tx.Handoff(ctx, ticketID, lease.AgentID, c.Handoff); err != nil {
			return err
		}
	}
	if c.ReleaseReason != nil {
		if err := tx.EndLease(ctx, lease.ID, *c.ReleaseReason); err != nil {
			return err
		}
	}
	if c.BlockedOn != nil {
		added, err := tx.AddBlocker(ctx, AgentAuthor(lease.AgentID), ticketID, *c.BlockedOn, c.BlockedNote)
		if err != nil {
			return err
		}
		if !added && c.BlockedNote != "" {
			if _, err := tx.Comment(ctx, ticketID, &NewComment{By: AgentAuthor(lease.AgentID), Body: c.BlockedNote}); err != nil {
				return err
			}
		}
	}
	if err := tx.WriteTicket(ctx, ticketID, &c.Ticket); err != nil {
		return err
	}
	if c.Unblock {
		if err := tx.UnblockSatisfied(ctx); err != nil {
			return err
		}
	}
	if c.Revoke {
		return tx.RevokeLeases(ctx, ticketID)
	}
	return nil
}

// TicketChange is one ticket's Change in a batch.
type TicketChange struct {
	TicketID int64
	Change   *Change
}

// moveBarrier is a test-only seam, like claimBarrier. Production leaves it
// nil. It runs inside applyBatch after the batch's first ticket is written,
// so a test can hold the transaction there and show that no claim sees a
// half-applied batch.
var moveBarrier func()

// applyBatch makes several tickets' Changes as one: each ticket's row in the
// order given, then what they imply across tickets once all of them are
// written -- one unblock sweep, then each revocation, to where its ticket
// landed after it. Never between two tickets' writes, so no ticket's change
// sees another's effects. A batch's Changes are writes to the ticket's own
// row and these effects only: a comment, handoff, lease end or blocker is a
// single ticket's transition, made by Change.apply.
func applyBatch(ctx context.Context, tx TransitionTx, batch []TicketChange) error {
	unblock := false
	for i, tc := range batch {
		c := tc.Change
		if c.Comment != nil || c.Handoff != "" || c.ReleaseReason != nil || c.BlockedOn != nil {
			return fmt.Errorf("ticket: a batch change to #%d writes more than the ticket's row", tc.TicketID)
		}
		if err := tx.WriteTicket(ctx, tc.TicketID, &c.Ticket); err != nil {
			return err
		}
		if i == 0 && moveBarrier != nil {
			moveBarrier()
		}
		unblock = unblock || c.Unblock
	}
	if unblock {
		if err := tx.UnblockSatisfied(ctx); err != nil {
			return err
		}
	}
	for _, tc := range batch {
		if tc.Change.Revoke {
			if err := tx.RevokeLeases(ctx, tc.TicketID); err != nil {
				return err
			}
		}
	}
	return nil
}

// heldExit is a transition out of in_progress by the agent holding the
// ticket: it decides on the ticket as loaded under the claim lock.
type heldExit func(held *Ticket, agentID uuid.UUID) (*Change, error)

// releaseExit hands the ticket back. With blockedOn it records that
// dependency first; with a cooldown it holds the ticket out of the queue
// until the cooldown ends, as well.
//
// A cooldown is what a release gets unless it names only a blocker: asked
// for with retry_after, or the policy's minimum, so a ticket released over a
// condition the queue cannot see is not claimed again at once. After
// EscalateAfter such releases in a row, or once the streak has run longer
// than MaxTotal, the ticket goes to discussion instead, with the last reason
// quoted: a precondition that never clears must not cost an agent session
// per check forever. Any other exit resets the streak (see ticketWriteSets).
//
// The reason is told to the next reader as a comment from the releasing
// agent (none when it is blank and there is no cooldown, so a routine
// release posts nothing), and folded into the blocker's comment when there
// is one. The handoff, the state of the work, is saved only when given: an
// omitted one must not replace an earlier, fuller handoff as the latest.
// There is no separate block operation: this is the single exit from
// in_progress that isn't done.
//
// Back to the queue is judged on every blocker the ticket has -- not only
// one named here: a blocker added while the ticket was held leaves it
// blocked too, while a dependency already finished leaves it open and
// immediately claimable.
func releaseExit(p *ReleaseParams, policy CooldownPolicy, streak CooldownStreak, now time.Time, out *ReleaseOutcome) heldExit {
	return func(_ *Ticket, agentID uuid.UUID) (*Change, error) {
		reason := p.Reason
		said := strings.TrimSpace(reason)
		c := &Change{
			Handoff:       p.Handoff,
			ReleaseReason: &reason,
			// A release that would knot two tickets together is refused,
			// not recorded: the agent still holds the lease and can release
			// without a blocked_on, a far better place to be than two
			// tickets nobody can ever claim.
			BlockedOn: p.BlockedOn,
			Ticket:    TicketWrite{Status: ToQueue()},
		}
		d := time.Duration(0)
		if p.RetryAfter > 0 || p.BlockedOn == nil {
			d = policy.clamp(p.RetryAfter)
		}
		note := said
		if d > 0 {
			since := now
			if streak.Since != nil {
				since = *streak.Since
			}
			next := CooldownStreak{Releases: streak.Releases + 1, Since: &since}
			until := now.Add(d)
			if escalates(policy, next, now) {
				cleared := false
				escalated := "cooldown escalated"
				c.ReleaseReason = &escalated
				c.Comment = &NewComment{By: AgentAuthor(agentID), Body: escalationComment(reason, next.Releases, next, now)}
				c.Ticket = TicketWrite{
					Status: To(StatusDiscussion), NeedsDiscussion: &cleared,
					FirstClaim: FirstClaimOwedTo(agentID),
				}
				out.Escalated, out.Checks = true, next.Releases
				return c, nil
			}
			c.Ticket.Status = To(StatusBlocked)
			c.Ticket.Cooldown = &CooldownWrite{Until: until, Streak: next}
			out.RetryAt = &until
			if note != "" {
				note += "\n\n"
			}
			note += cooldownNote(until)
		}
		if note != "" {
			if p.BlockedOn != nil {
				c.BlockedNote = note
			} else {
				c.Comment = &NewComment{By: AgentAuthor(agentID), Body: note}
			}
		}
		return c, nil
	}
}

// doneExit closes the ticket -- and, since a finished ticket may be the last
// thing others were waiting on, unblocks them.
//
// A ticket that still needs discussion, or requires approval and has none,
// is refused before anything is written, so it stays held for the agent to
// discuss or put into review instead. Discussion is checked first: until
// the plan is agreed there is no change whose approval would matter.
// A ticket reopened and finished again without a PR keeps the link it had:
// that change was still merged, and the link is its record.
func doneExit(summary string, prURL *string) heldExit {
	return func(held *Ticket, agentID uuid.UUID) (*Change, error) {
		if err := refuseUndiscussed(held); err != nil {
			return nil, err
		}
		if held.RequiresApproval && held.ApprovedAt == nil {
			return nil, fmt.Errorf("%w (ticket #%d)", ErrNotApproved, held.ID)
		}
		reason := "done"
		c := &Change{
			ReleaseReason: &reason,
			Ticket:        TicketWrite{Status: To(StatusDone), Closing: Closes(), PRURL: prURL},
			Unblock:       true,
		}
		if summary != "" {
			c.Comment = &NewComment{By: AgentAuthor(agentID), Body: summary}
		}
		return c, nil
	}
}

// discussExit hands the ticket back until a human answers: the question as
// the agent's comment, the handoff as its context, and this agent recorded
// as the one owed the answer.
//
// The discussion is recorded here, not when the ticket is claimed: posting
// the plan and asking about it is what the planning pass is for, so the
// claim after the human's answer is a working claim. It is recorded on this
// agent's attempt; the ticket's needs_discussion clears when every live
// attempt has discussed, so a sibling that has not planned yet is still told
// to (see WriteTicket).
func discussExit(question, handoff string) heldExit {
	return func(_ *Ticket, agentID uuid.UUID) (*Change, error) {
		reason := "discussion"
		return &Change{
			Comment:       &NewComment{By: AgentAuthor(agentID), Body: question},
			Handoff:       handoff,
			ReleaseReason: &reason,
			Ticket: TicketWrite{
				Status: To(StatusDiscussion), Discussed: true,
				FirstClaim: FirstClaimOwedTo(agentID),
			},
		}, nil
	}
}

// reviewExit hands the ticket to a human to review its PR: the link
// recorded, the handoff saved, and this agent recorded as the one owed the
// ticket back. Its scopes stay held through the status alone -- see
// activeScopes.
//
// A ticket that still needs discussion is refused, as doneExit refuses it.
func reviewExit(prURL, handoff, voluntaryReason string) heldExit {
	return func(held *Ticket, agentID uuid.UUID) (*Change, error) {
		if err := refuseUndiscussed(held); err != nil {
			return nil, err
		}
		reason := "review"
		c := &Change{
			Handoff:       handoff,
			ReleaseReason: &reason,
			Ticket: TicketWrite{
				Status: To(StatusReview), PRURL: &prURL,
				FirstClaim: FirstClaimOwedTo(agentID), Review: ReviewStarts(),
			},
		}
		if voluntaryReason != "" {
			c.Comment = &NewComment{By: AgentAuthor(agentID), Body: VoluntaryReviewLead + voluntaryReason}
		}
		return c, nil
	}
}

// landExit hands the finished ticket to the server's landing queue: the
// attempt goes into the merge queue with this agent recorded as the one owed
// the ticket (a handback returns it there), and the handoff is saved. The
// status keeps workers from claiming it meanwhile (takeableAttempt).
//
// A ticket that still needs discussion is refused, as doneExit refuses it.
func landExit(handoff string) heldExit {
	return func(held *Ticket, agentID uuid.UUID) (*Change, error) {
		if err := refuseUndiscussed(held); err != nil {
			return nil, err
		}
		reason := "landing"
		return &Change{
			Handoff:       handoff,
			ReleaseReason: &reason,
			Ticket:        TicketWrite{Status: To(StatusInMergeQueue), FirstClaim: FirstClaimOwedTo(agentID)},
		}, nil
	}
}

// refuseUndiscussed is the needs-discussion gate on the exits that treat
// the agent's work as the change the ticket asked for: done and review.
// Release, discuss and prepare stay open, so the agent always has a way out.
func refuseUndiscussed(held *Ticket) error {
	if held.NeedsDiscussion {
		return fmt.Errorf("%w (ticket #%d)", ErrNeedsDiscussion, held.ID)
	}
	return nil
}

// prepareExit takes the ticket out of the queue and into Preparing, with
// the reason as the agent's comment.
//
// Unlike discussExit it records no first claim: whoever picks the ticket up
// after the human marks it ready starts from the ticket, which is the point
// of the human reshaping it.
func prepareExit(reason string) heldExit {
	return func(_ *Ticket, agentID uuid.UUID) (*Change, error) {
		released := "preparing"
		return &Change{
			Comment:       &NewComment{By: AgentAuthor(agentID), Body: reason},
			ReleaseReason: &released,
			Ticket:        TicketWrite{Status: To(StatusPreparing), FirstClaim: FirstClaimCleared()},
		}, nil
	}
}

// ticketTransition is a person's transition of a ticket no agent need hold:
// it decides on the ticket as loaded under the claim lock. A nil Change
// with no error is a decision to write nothing.
type ticketTransition func(t *Ticket) (*Change, error)

// endReview is a human's review returning the ticket to the queue -- open,
// or blocked on unfinished dependencies, as a discussion's answer does --
// with its author holding first claim for affinity, and the review's
// outcome recorded with it.
//
// A ticket not in review is refused with ErrNotInReview, and a review
// submitted before its current cycle began with ErrStaleReview; neither
// writes anything. submittedAt is when a review given elsewhere was made;
// nil for one given here and now.
func endReview(outcome ReviewStep, comment *NewComment, affinity time.Duration, submittedAt *time.Time) ticketTransition {
	return func(t *Ticket) (*Change, error) {
		if t.Status != StatusReview {
			return nil, fmt.Errorf("%w: #%d", ErrNotInReview, t.ID)
		}
		if !inCurrentReview(t, submittedAt) {
			return nil, fmt.Errorf("%w: #%d", ErrStaleReview, t.ID)
		}
		return &Change{
			Comment: comment,
			Ticket: TicketWrite{
				Status: ToQueue(), FirstClaim: FirstClaimWindowOf(affinity), Review: outcome,
			},
		}, nil
	}
}

// approval is userID approving the ticket's change.
func approval(userID uuid.UUID, affinity time.Duration, submittedAt *time.Time) ticketTransition {
	return endReview(ReviewApproves(userID), nil, affinity, submittedAt)
}

// changesRequested is userID sending the ticket back for more work, with
// their comment as what to address.
func changesRequested(userID uuid.UUID, comment string, affinity time.Duration, submittedAt *time.Time) ticketTransition {
	return endReview(ReviewRequestsChanges(), &NewComment{By: UserAuthor(userID), Body: comment}, affinity, submittedAt)
}

// inCurrentReview is whether a review submitted at submittedAt belongs to
// t's current review cycle. No time -- a review given on the board, now --
// always does. GitHub reports times to the second, so the cycle's start is
// truncated to match, or a review submitted in the same second the cycle
// began would read as older than it.
func inCurrentReview(t *Ticket, submittedAt *time.Time) bool {
	if submittedAt == nil || t.ReviewStartedAt == nil {
		return true
	}
	return !t.ReviewStartedAt.Truncate(time.Second).After(*submittedAt)
}

// humanCommentEffect is what a person's comment does to the ticket beyond
// recording it, with needsDiscussion the flag's new value when the comment
// sets it.
//
// A ticket waiting in discussion is answered, whatever the comment does to
// the flag: back to the queue -- never an unconditional open, the same
// requeue a release makes -- with the agent that asked holding first claim
// for affinity. A reply that landed without reopening would leave the
// ticket waiting on a question already answered.
//
// On any other ticket, a comment writing the flag does what the flag's
// write does on any route -- see settingNeedsDiscussion.
func humanCommentEffect(needsDiscussion *bool, affinity time.Duration) ticketTransition {
	return func(t *Ticket) (*Change, error) {
		switch {
		case t.Status == StatusDiscussion:
			return &Change{Ticket: TicketWrite{
				Status: ToQueue(), FirstClaim: FirstClaimWindowOf(affinity), NeedsDiscussion: needsDiscussion,
			}}, nil
		case needsDiscussion != nil:
			return settingNeedsDiscussion(*needsDiscussion)(t)
		}
		return nil, nil
	}
}

// settingNeedsDiscussion is a person writing needs_discussion, by whichever
// route -- a comment carrying it, or the ticket's own edit.
//
// Setting the flag on a ticket an agent is working takes the ticket back
// through the same revocation a human's status move uses: requeued, the
// lease ended, and the agent told to stop. An agent already building never
// reads the flag again, so leaving it the lease would have it build what
// the human has just asked to be planned. Clearing the flag withdraws
// nothing: an agent planning can carry on and ask as it would have.
func settingNeedsDiscussion(needsDiscussion bool) ticketTransition {
	return func(t *Ticket) (*Change, error) {
		c := &Change{Ticket: TicketWrite{NeedsDiscussion: &needsDiscussion}}
		if needsDiscussion && t.Status == StatusInProgress {
			c.Ticket.Status = ToQueue()
			c.Revoke = true
		}
		return c, nil
	}
}

// closeMergedFrom is userID's merge of pr, the ticket's PR, made on the PR
// itself (the trailing part of this comment is about queued). When queued, also takes an open
// ticket: the caller confirms in the same transaction that it is in the merge
// queue.
func closeMergedFrom(hosts *codehost.Registry, pr codehost.ChangeRef, userID uuid.UUID, queued bool) ticketTransition {
	return func(t *Ticket) (*Change, error) {
		if !mergeable(t) && !(queued && t.Status == StatusOpen) {
			return nil, fmt.Errorf("%w: #%d is %s", ErrNotMergeable, t.ID, t.Status)
		}
		if !hasPR(hosts, t, pr) {
			return nil, fmt.Errorf("%w: #%d's PR is no longer %s", ErrNotMergeable, t.ID, hosts.ChangeURL(pr))
		}
		return &Change{
			Comment: &NewComment{By: UserAuthor(userID), Body: "Merged on " + pr.Host + ": " + hosts.ChangeURL(pr)},
			Ticket:  TicketWrite{Status: To(StatusDone), Closing: Closes(), KeepsLanding: queued},
			Unblock: true,
			Revoke:  true,
		}, nil
	}
}

// MergeQueue is the ticket side's view of the landing queue, which owns the
// landings and server_merge_outcomes tables. q is the caller's transaction, so
// the row and the ticket's move stay atomic.
type MergeQueue interface {
	// Dequeue removes the request's ticket's row unless the runner has begun
	// it or a live batch owns it, recording the merge once when the reason
	// says it was made outside the queue. It reports whether a row was removed.
	Dequeue(ctx context.Context, q db.Querier, req *DequeueRequest) (bool, error)
}
