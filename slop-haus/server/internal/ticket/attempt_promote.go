package ticket

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

var (
	// ErrReplayNotPromotable is a promotion of a replay, which only ever runs
	// the ticket again and never takes its place.
	ErrReplayNotPromotable = errors.New("ticket: a replay cannot be promoted")
	// ErrPromoteTicketFinished is a promotion on a ticket that is done or
	// cancelled: its work has landed or is abandoned.
	ErrPromoteTicketFinished = errors.New("ticket: a done or cancelled ticket cannot promote an attempt")
	// ErrPromoteMerging is a promotion that would take the promotion from the
	// attempt that is merging. Choosing differently is only before the merge.
	ErrPromoteMerging = errors.New("ticket: the promoted attempt holds the integration lock or is merging, so promotion cannot move now")
	// ErrPromotionBlocked is the attempt's change not being fit to reach the
	// base branch, as the change flow judged it (for example, its branch took
	// in main). The change package wraps it.
	ErrPromotionBlocked = errors.New("ticket: the attempt cannot be promoted")
	// ErrPromotionUnverified is the code host not being able to say whether it
	// can. Nothing is let through on a guess; the call is safe to repeat.
	ErrPromotionUnverified = errors.New("ticket: the attempt's change could not be checked for promotion")
)

// promoteBarrier is a test-only seam, like addBarrier. Production leaves it
// nil. It is called inside PromoteAttempt's transaction once the attempts are
// locked, so a test can hold one promotion there and show a competing claim,
// lock or promotion waits for it.
var promoteBarrier func()

// AttemptPromoted is what promoting an attempt did, for the audit record and
// for the change effects that follow it.
type AttemptPromoted struct {
	Number int
	// From is the status the attempt was in.
	From string
	// Demoted is the attempt that lost the promotion, or 0 for none.
	Demoted int
	// Already is the attempt having been promoted before the call: nothing
	// was written. It is success, not a refusal, so repeating the request is
	// how a promotion whose changes could not be carried to the code host is
	// carried again.
	Already bool
	// Approved is whether the attempt was awaiting the choice and went back
	// to its agent to merge.
	Approved bool
	// Approval is the approval the promotion recorded, when Approved. The
	// service reports it to the approval hook once the promotion commits.
	Approval *Approval
	// Affinity is what became of the approved attempt's reservation for its
	// agent, for the audit line: "reserved", or "skipped:busy" when the agent
	// holds a lease on another ticket, "skipped:unavailable" for a disabled
	// or non-worker agent. Empty when nothing was approved.
	Affinity string
}

// PromoteAttempt makes the numbered attempt the ticket's promoted one, on a
// person's behalf. The attempt's agent, when it was waiting for the choice, is
// sent back to merge as an approval does it.
func (s *Service) PromoteAttempt(ctx context.Context, userID uuid.UUID, ticketID int64, number int) (*AttemptPromoted, error) {
	promoted, err := s.store.PromoteAttempt(WithUser(ctx, userID), ticketID, number, userID, s.affinity)
	if err != nil {
		return nil, err
	}
	if promoted.Approval != nil {
		s.NotifyApproved(ctx, promoted.Approval)
	}
	// After the commit, so a refused promotion leaves no record of one; a
	// repeat that wrote nothing is not a promotion.
	if s.events != nil && !promoted.Already {
		tenant, _ := db.TenantFrom(ctx)
		s.events.Record(ctx, audit.AttemptPromoted,
			"user_id", userID, "tenant", tenant, "ticket", ticketID, "attempt", promoted.Number,
			"from_status", promoted.From, "demoted_attempt", promoted.Demoted, "approved", promoted.Approved, "affinity", promoted.Affinity)
	}
	return promoted, nil
}

// PromoteAttempt takes the claim lock, as every claim does, then the ticket's
// row, then its attempts' (the lock order AttemptRef names), so a claim, a lock
// request or another promotion either happens first and is seen here, or finds
// the promotion made. The previous promotion is cleared before the new one is
// set, in the same transaction, so the one-promoted index holds throughout.
//
// Scope follows promotion: the promoted attempt holds the ticket's, every
// other live attempt holds none (attempts.shadow). An attempt that was
// awaiting the choice (in review, unpromoted) is approved by the same code a
// person's approval runs, so it goes back to its agent by resume affinity.
func (s *PostgresStore) PromoteAttempt(ctx context.Context, ticketID int64, number int, userID uuid.UUID, affinity time.Duration) (*AttemptPromoted, error) {
	var out *AttemptPromoted
	shared, err := s.sharedPaths(ctx)
	if err != nil {
		return nil, err
	}
	err = s.db.InTx(ctx, func(q db.Querier) error {
		out = nil
		if err := db.TakeAdvisoryLock(ctx, q, claimAdvisoryKey); err != nil {
			return err
		}
		var status Status
		err := q.QueryRow(ctx, `SELECT status FROM tickets WHERE id = $1 FOR UPDATE`, ticketID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: #%d", ErrNotFound, ticketID)
		}
		if err != nil {
			return fmt.Errorf("lock ticket: %w", err)
		}
		states, err := lockAttempts(ctx, q, ticketID)
		if err != nil {
			return err
		}
		if promoteBarrier != nil {
			promoteBarrier()
		}
		var target, previous *attemptStart
		for i := range states {
			if states[i].number == number {
				target = &states[i]
			}
			if states[i].promoted {
				previous = &states[i]
			}
		}
		ref := &AttemptRef{TicketID: ticketID, Number: number}
		switch {
		case target == nil:
			return fmt.Errorf("#%d has no attempt %d: %w", ticketID, number, ErrNoSuchAttempt)
		case target.replay:
			return fmt.Errorf("%s: %w", ref, ErrReplayNotPromotable)
		case status == StatusDone || status == StatusCancelled:
			return fmt.Errorf("ticket #%d is %s: %w", ticketID, status, ErrPromoteTicketFinished)
		case AttemptStatus(target.status) == AttemptStatusClosed:
			return fmt.Errorf("%s: %w", ref, ErrAttemptClosed)
		case AttemptStatus(target.status) == AttemptStatusDone:
			return fmt.Errorf("%s: %w", ref, ErrAttemptDone)
		}
		if target.promoted {
			out = &AttemptPromoted{Number: number, From: target.status, Already: true}
			return nil
		}
		if previous != nil {
			if err := refuseWhileMerging(ctx, q, ticketID, previous.number); err != nil {
				return err
			}
		}

		if err := userMover(userFrom(ctx), "attempt promoted").set(ctx, q); err != nil {
			return err
		}
		out = &AttemptPromoted{Number: number, From: target.status}
		if previous != nil {
			out.Demoted = previous.number
			if err := writeAttempt(ctx, q, &AttemptRef{TicketID: ticketID, Number: previous.number}, &setList{
				clauses: []string{"promoted_at = NULL", "promoted_by_user_id = NULL", "promoted_by_user_id_deleted = false", "updated_at = now()"}, first: 3,
			}); err != nil {
				return err
			}
		}
		// Scope: only the promoted attempt holds the ticket's, and the check
		// constraint wants an attempt not to be a shadow when it is promoted, so
		// this precedes the promotion. Set for every
		// live attempt, so a sibling that was a normal unpromoted attempt
		// (added while the promoted one had not started) stops holding it too.
		if _, err := q.Exec(ctx, `
			UPDATE attempts SET shadow = (number <> $2 OR replay)
			 WHERE ticket_id = $1 AND status IN `+AttemptLiveStatusesSQL, ticketID, number); err != nil {
			return fmt.Errorf("move the ticket's scope to attempt %d: %w", number, err)
		}
		if err := writeAttempt(ctx, q, ref, &setList{
			clauses: []string{"promoted_at = now()", "promoted_by_user_id = $3", "promoted_by_user_id_deleted = false", "updated_at = now()"},
			args:    []any{userID}, first: 3,
		}); err != nil {
			return err
		}
		if target.status == "review" {
			_, approval, err := ApproveReviewRecorded(ctx, &pgTransition{q: q, shared: shared, queue: s.mergeQueue}, ticketID, ref, userID, affinity)
			if err != nil {
				return err
			}
			out.Approved, out.Approval = true, approval
			if out.Affinity, err = affinityOutcome(ctx, q, ref); err != nil {
				return err
			}
		}
		// A person's preparing and parked are not the attempts' to move.
		if _, err := deriveStatus(ctx, q, "t.id = $1 AND "+inQueueLifecycle, ticketID); err != nil {
			return err
		}
		// A running sibling held no scope, so it may now hold files another
		// ticket took meanwhile.
		conflicts, err := scopeConflicts(ctx, q, []int64{ticketID}, shared)
		if err != nil {
			return err
		}
		if len(conflicts) > 0 {
			return &ScopeConflictError{Blocked: conflicts[0]}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// refuseWhileMerging is ErrPromoteMerging when the attempt holds the
// integration lock or has a merge in flight. A merge needs the lock, and both
// are read under the claim lock every lock request takes, so the answer holds
// until this transaction ends. The merge claim lives with the change flow's
// table; only this one column of it is read here.
func refuseWhileMerging(ctx context.Context, q db.Querier, ticketID int64, number int) error {
	var busy bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM locks k JOIN leases l ON l.id = k.holder_lease_id
		                WHERE l.ticket_id = $1 AND l.attempt_number = $2)
		    OR EXISTS (SELECT 1 FROM ticket_changes c
		                WHERE c.ticket_id = $1 AND c.attempt_number = $2 AND c.superseded_at IS NULL AND c.merged_at IS NULL
		                  AND c.merging_since > now() - interval '5 minutes')`, ticketID, number).Scan(&busy); err != nil {
		return fmt.Errorf("look for the promoted attempt's merge: %w", err)
	}
	if busy {
		return fmt.Errorf("%s: %w", &AttemptRef{TicketID: ticketID, Number: number}, ErrPromoteMerging)
	}
	return nil
}

// affinityOutcome says whether the approved attempt kept its first-claim
// reservation for its agent, and if not why. A busy agent is not reserved for
// (see idleWorkerClause), so any slot can claim the attempt at once.
func affinityOutcome(ctx context.Context, q db.Querier, ref *AttemptRef) (string, error) {
	var reserved, busy bool
	if err := q.QueryRow(ctx, `
		SELECT pa.affinity_until IS NOT NULL,
		       EXISTS (SELECT 1 FROM leases l WHERE l.agent_id = pa.resume_agent_id AND `+activeLeaseClause+`)
		  FROM attempts pa WHERE pa.ticket_id = $1 AND pa.number = $2`, ref.TicketID, ref.Number).Scan(&reserved, &busy); err != nil {
		return "", fmt.Errorf("read the attempt's reservation: %w", err)
	}
	switch {
	case reserved:
		return "reserved", nil
	case busy:
		return "skipped:busy", nil
	}
	return "skipped:unavailable", nil
}
