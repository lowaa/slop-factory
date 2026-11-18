package ticket

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Discuss stops work on the agent's held ticket until a human answers the
// question. The handoff is required, not optional: a ticket-worker's own
// context is discarded the moment it returns, so the handoff is the only
// thing the next agent -- including this one, later -- will have.
func (s *Service) Discuss(ctx context.Context, agentID uuid.UUID, question, handoff string) (*Ticket, error) {
	if strings.TrimSpace(question) == "" {
		return nil, ErrEmptyQuestion
	}
	if strings.TrimSpace(handoff) == "" {
		return nil, ErrEmptyHandoff
	}
	// The hook is told by the store once the exit commits, as it is for every
	// route into discussion.
	return s.exitHeld(ctx, agentID, discussExit(question, handoff))
}

// Prepare moves the agent's own held ticket back to Preparing, releasing
// it, for a human to reshape. Only the ticket the agent holds: moving one it
// doesn't would let one agent pull queued work out from under the rest of
// the fleet. The reason is required for the same reason Discuss requires a
// question -- a ticket must never leave the queue silently.
func (s *Service) Prepare(ctx context.Context, agentID uuid.UUID, reason string) (*Ticket, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, ErrEmptyReason
	}
	return s.exitHeld(ctx, agentID, prepareExit(reason))
}

// -- Leases --

// Claim hands the agent the highest-priority claimable ticket whose scopes
// collide with nothing currently held, or reports what blocked it. Pass
// only to request one specific ticket; the same dependency and conflict
// rules apply either way, so there is one code path and one set of
// invariants.
//
// The fleet-wide processing switch is honoured here, but checked by the
// store inside the claim transaction rather than by this method first --
// a check out here is a time-of-check/time-of-use gap that hands out one
// more ticket to an agent just as the human pauses the fleet. See
// PostgresStore.Claim.
//
// This is the single place a lease is granted -- REST /api/claims and the
// ticket_next tool both arrive through it -- so gating it is what makes
// "no new work starts" a guarantee rather than a tendency. A directed
// claim (only != nil) is refused too: the switch means the fleet starts
// nothing, and a refusal naming the reason beats a human wondering why
// their hand-picked ticket went nowhere.
func (s *Service) Claim(ctx context.Context, agentID uuid.UUID, only *int64) (*ClaimResult, error) {
	return s.ClaimWith(ctx, agentID, &ClaimOptions{Only: only})
}

// ClaimWith is Claim with the options a REST claim can carry.
func (s *Service) ClaimWith(ctx context.Context, agentID uuid.UUID, opts *ClaimOptions) (*ClaimResult, error) {
	if opts == nil {
		opts = &ClaimOptions{}
	}
	if opts.Harness != "" && !validHarness(opts.Harness) {
		return nil, ErrInvalidHarness
	}
	opts = opts.reported()
	if s.setup != nil {
		if gap := s.setup.SetupGap(ctx); gap != "" {
			return nil, &SetupIncompleteError{Gap: gap}
		}
	}
	result, err := s.store.Claim(ctx, agentID, opts, DefaultLeaseTTL, s.configs)
	if err != nil || result == nil || result.Ticket == nil {
		return result, err
	}
	// After the claim has committed, so the claim lock is not held across
	// the code host read.
	result.Ticket = s.PinBase(ctx, result.Ticket)
	if result.Attempt != nil {
		result.Branch = s.EnsureAttemptBranch(ctx, result.Ticket, result.Attempt.Number)
	}
	return result, nil
}

// Heartbeat extends the agent's lease, returning ErrNoLease when the lease
// has been released or expired from under it.
func (s *Service) Heartbeat(ctx context.Context, agentID uuid.UUID) error {
	return s.store.Heartbeat(ctx, agentID, DefaultLeaseTTL)
}

// Release hands the agent's ticket back to the queue, recording blockedOn
// as a dependency first when it is set.
func (s *Service) Release(ctx context.Context, agentID uuid.UUID, reason string, blockedOn *int64) (*Ticket, error) {
	out, err := s.ReleaseWith(ctx, agentID, ReleaseParams{Reason: reason, BlockedOn: blockedOn})
	if err != nil {
		return nil, err
	}
	return out.Ticket, nil
}

// Done closes the agent's ticket. The PR link is validated before anything
// is written, so a bad link leaves the ticket held for the agent to retry.
func (s *Service) Done(ctx context.Context, agentID uuid.UUID, params *DoneParams) (*Ticket, error) {
	prURL, err := parsePRURL(params.PRURL)
	if err != nil {
		return nil, err
	}
	t, err := s.exitHeld(ctx, agentID, s.inTicketsRepo(ctx, prURL, doneExit(params.Summary, prURL)))
	if errors.Is(err, ErrNoLease) && prURL != nil {
		if closed := s.closedThrough(ctx, agentID, *prURL); closed != nil {
			return closed, nil
		}
	}
	return t, err
}

// closedThrough is the ticket the agent's latest lease was on when that
// ticket is done through prURL and the lease is over: a change_merge closed
// it already, so the closing call an older briefing still asks for has
// nothing left to do. Nil for anything else, including a lookup that fails.
func (s *Service) closedThrough(ctx context.Context, agentID uuid.UUID, prURL string) *Ticket {
	snap, err := s.store.WorkerSnapshot(ctx, agentID)
	if err != nil || snap.Active || snap.TicketStatus != StatusDone {
		return nil
	}
	t, err := s.store.GetTicket(ctx, snap.TicketID)
	if err != nil || t.PRURL == nil || *t.PRURL != prURL {
		return nil
	}
	return t
}

// exitHeld makes one of the agent's exits from the ticket it holds, and
// answers with the ticket as the exit left it.
//
// Load, decide and write happen in the one transaction OnHeldLease opens
// under the claim lock: the status and lease an exit writes are what Claim
// reads, and its decision -- Done's approval check -- must be about the
// ticket it writes, not one a human has changed since. All of it or none of
// it: a question posted without the release would leave an agent holding a
// ticket it has stopped working, and a release without the handoff would
// send the next agent in blind.
func (s *Service) exitHeld(ctx context.Context, agentID uuid.UUID, exit heldExit) (*Ticket, error) {
	return s.exitHeldWithin(ctx, agentID, exit, nil)
}

// exitHeldWithin is exitHeld that also runs within, when set, in the same
// transaction once the exit is applied: it is how something recorded about
// the exit lands with it or not at all.
func (s *Service) exitHeldWithin(ctx context.Context, agentID uuid.UUID, exit heldExit, within func(q db.Querier) error) (*Ticket, error) {
	return s.exitHeldChosen(ctx, agentID, func(TransitionTx, *HeldLease) heldExit { return exit }, within)
}

// exitHeldChosen is exitHeldWithin for an exit that depends on state read in
// the exit's own transaction, under the same lock, such as the lease's attempt.
func (s *Service) exitHeldChosen(ctx context.Context, agentID uuid.UUID, choose func(tx TransitionTx, lease *HeldLease) heldExit, within func(q db.Querier) error) (*Ticket, error) {
	var left *Ticket
	err := s.store.OnHeldLease(ctx, agentID, func(tx TransitionTx, lease *HeldLease) error {
		var err error
		left, err = decideAndApply(ctx, tx, lease, lease.TicketID, func(held *Ticket) (*Change, error) {
			return choose(tx, lease)(held, agentID)
		})
		if err != nil || within == nil {
			return err
		}
		return within(tx.Querier())
	})
	if err != nil {
		return nil, err
	}
	return left, nil
}

// transition makes a person's transition of ticketID, and answers with the
// ticket as it left it.
//
// Load, decide and write happen in the one transaction InTransition opens
// under the claim lock, for the reasons exitHeld gives: the status written
// is what Claim reads, and the decision must be about the ticket written --
// an approval must not land on a ticket an agent has claimed since, nor a
// merge close one it is merging.
func (s *Service) transition(ctx context.Context, ticketID int64, decide ticketTransition) (*Ticket, error) {
	return s.transitionThen(ctx, ticketID, decide, nil)
}

// transitionThen is transition that also runs after in the same transaction,
// once the change is made, for what must be read back from it.
func (s *Service) transitionThen(ctx context.Context, ticketID int64, decide ticketTransition, after func(tx TransitionTx) error) (*Ticket, error) {
	var left *Ticket
	err := s.store.InTransition(ctx, func(tx TransitionTx) error {
		var err error
		if left, err = decideAndApply(ctx, tx, nil, ticketID, decide); err != nil {
			return err
		}
		if after == nil {
			return nil
		}
		return after(tx)
	})
	if err != nil {
		return nil, err
	}
	return left, nil
}

// decideBarrier is a test-only seam, like claimBarrier. Production leaves it
// nil. It runs inside decideAndApply between loading the ticket and deciding
// on it, so a test can hold a transition there -- Done having read
// requires_approval, say -- and show that no edit lands in between.
var decideBarrier func()

// decideAndApply loads ticketID in tx, decides on it, and makes the Change
// decided -- with lease the holder's when an agent makes it -- returning the
// ticket as tx then sees it. A refusal returns before anything is written.
func decideAndApply(ctx context.Context, tx TransitionTx, lease *HeldLease, ticketID int64, decide ticketTransition) (*Ticket, error) {
	current, err := tx.Ticket(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	if decideBarrier != nil {
		decideBarrier()
	}
	change, err := decide(current)
	if err != nil {
		return nil, err
	}
	if change == nil {
		return current, nil
	}
	if err := change.apply(ctx, tx, lease, ticketID); err != nil {
		return nil, err
	}
	return tx.Ticket(ctx, ticketID)
}

// RecentRevocation is the read-only context behind a bare ErrNoLease.
func (s *Service) RecentRevocation(ctx context.Context, agentID uuid.UUID) (*RevocationContext, error) {
	return s.store.RecentRevocation(ctx, agentID, DefaultLeaseTTL)
}

func (s *Service) LeaseFor(ctx context.Context, agentID uuid.UUID) (*Lease, error) {
	return s.store.LeaseFor(ctx, agentID)
}

// LeaseEnd is what became of a lease, or nil while it is still held. A
// read: it extends nothing and takes no revocation notice.
func (s *Service) LeaseEnd(ctx context.Context, leaseID int64) (*LeaseEnd, error) {
	return s.store.LeaseEnd(ctx, leaseID)
}

// WorkerSnapshot is where the agent's latest lease stands, held or ended.
func (s *Service) WorkerSnapshot(ctx context.Context, agentID uuid.UUID) (*WorkerSnapshot, error) {
	return s.store.WorkerSnapshot(ctx, agentID)
}

// ReleaseLeasesFor hands back what an agent held at acquiredBy. Called when
// an agent is disabled, with the time it was.
func (s *Service) ReleaseLeasesFor(ctx context.Context, agentID uuid.UUID, reason string, acquiredBy time.Time) error {
	return s.store.ReleaseLeasesFor(ctx, agentID, reason, acquiredBy)
}

// Peek reports what Claim would grant, without granting it. The Stop hook
// uses this: it must not assign work.
//
// Deliberately not gated by the processing switch. It is read-only, and
// the hook needs the true state of the queue to log a scope deadlock even
// while processing is off -- the switch decides what the hook does with
// the answer, not whether it may ask.
func (s *Service) Peek(ctx context.Context, agentID uuid.UUID, only *int64) (*ClaimResult, error) {
	return s.store.Peek(ctx, agentID, only)
}
