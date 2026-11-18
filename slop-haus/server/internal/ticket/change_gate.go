package ticket

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// AttemptReview is a held attempt's own PR link and review outcome. The
// merge gate reads these from the attempt the lease is on: the Ticket's
// fields of the same names are the promoted attempt's, which is another
// attempt's whenever the holder is a sibling.
type AttemptReview struct {
	PRURL              *string
	ApprovedAt         *time.Time
	ApprovedByUserID   *uuid.UUID
	ChangesRequestedAt *time.Time
	ReviewStartedAt    *time.Time
}

// ChangeGate is what the merge gate needs to know about an agent's standing:
// the ticket it holds, the attempt its lease is on and whether it holds the
// integration lock.
type ChangeGate struct {
	// Ticket's approval and PR fields are the promoted attempt's; the gate
	// decides on Review.
	Ticket *Ticket
	// Attempt is the attempt the agent's lease is on, which its change and
	// its merge are of.
	Attempt AttemptRef
	Review  AttemptReview
	// Promoted is whether the attempt is the ticket's promoted one. Only a
	// promoted attempt's change may reach the ticket's base branch.
	Promoted bool
	// TicketPromoted is whether any attempt of the ticket is promoted: an
	// unpromoted attempt's change is then one not to merge, as the ticket's
	// choice is made.
	TicketPromoted bool
	// HoldsLock is whether this agent, on this lease, holds the integration
	// lock. A lock whose lease has ended is gone with it, so a stale agent
	// reads false.
	HoldsLock bool
	// HeldLock is that lock's name, "" when HoldsLock is false. The merge gate
	// holds it to the lock of the branch the change merges into.
	HeldLock string
}

// ChangeGateFor reads the held ticket and the lock together, under the claim
// lock that every approval, status change and lock release also takes, so
// the three facts are from one moment rather than three reads a human's
// action could fall between. The gate's decision still cannot be atomic with
// the merge on the host, which is a network call; the head SHA is what makes
// that last step safe, and the window left is a human revoking approval or
// the lock within one call.
func (s *Service) ChangeGateFor(ctx context.Context, agentID uuid.UUID) (*ChangeGate, error) {
	var gate ChangeGate
	err := s.store.OnHeldLease(ctx, agentID, func(tx TransitionTx, lease *HeldLease) error {
		held, err := tx.Ticket(ctx, lease.TicketID)
		if err != nil {
			return err
		}
		gate.Ticket = held
		gate.Attempt = AttemptRef{TicketID: lease.TicketID, Number: lease.Attempt}
		err = tx.Querier().QueryRow(ctx, `
			SELECT pr_url, approved_at, approved_by_user_id, changes_requested_at, review_started_at,
			       promoted_at IS NOT NULL
			  FROM attempts WHERE ticket_id = $1 AND number = $2`, lease.TicketID, lease.Attempt).
			Scan(&gate.Review.PRURL, &gate.Review.ApprovedAt, &gate.Review.ApprovedByUserID,
				&gate.Review.ChangesRequestedAt, &gate.Review.ReviewStartedAt, &gate.Promoted)
		if err != nil {
			return fmt.Errorf("read attempt %d: %w", lease.Attempt, err)
		}
		if err := tx.Querier().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM attempts WHERE ticket_id = $1 AND promoted_at IS NOT NULL)`,
			lease.TicketID).Scan(&gate.TicketPromoted); err != nil {
			return fmt.Errorf("read the ticket's promoted attempt: %w", err)
		}
		err = tx.Querier().QueryRow(ctx, `
			SELECT COALESCE((SELECT name FROM locks WHERE holder_agent_id = $1 AND holder_lease_id = $2), '')`,
			agentID, lease.ID).Scan(&gate.HeldLock)
		gate.HoldsLock = gate.HeldLock != ""
		if err != nil {
			return fmt.Errorf("read the integration lock: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &gate, nil
}

// RecordChange puts the link of the change slop-haus opened for the held
// ticket on the ticket, as pr_url, so everything that reads a ticket's PR
// (the webhook, the lock queue, ticket_finish) finds it as it would one the
// agent reported.
//
// newChange is a change the ticket did not have before: any review outcome
// the ticket has was of some other change, so it is dropped, and a ticket
// that requires approval needs a new review. An approval is always of the
// change as it stands now.
//
// within, when set, runs in the same transaction, under the same claim lock,
// with that transaction's querier: it is how the record of the change is
// written together with the link and the dropped review, all of it or none.
func (s *Service) RecordChange(ctx context.Context, agentID uuid.UUID, prURL string, newChange bool, within func(q db.Querier) error) error {
	link, err := parsePRURL(prURL)
	if err != nil {
		return err
	}
	if link == nil {
		return ErrPRURLRequired
	}
	return s.store.OnHeldLease(ctx, agentID, func(tx TransitionTx, lease *HeldLease) error {
		w := &TicketWrite{PRURL: link}
		if newChange {
			w.Review = ReviewDiscards()
		}
		if err := tx.WriteTicket(ctx, lease.TicketID, w); err != nil {
			return err
		}
		if within == nil {
			return nil
		}
		return within(tx.Querier())
	})
}
