package ticket

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

var (
	// ErrClosePromoted is a close of the promoted attempt: it is the ticket's
	// own work, and only promoting another takes that from it.
	ErrClosePromoted = errors.New("ticket: the promoted attempt cannot be closed")
	// ErrAttemptClosed is a close of an attempt that is closed already.
	ErrAttemptClosed = errors.New("ticket: the attempt is already closed")
	// ErrAttemptDone is a close of an attempt that has finished.
	ErrAttemptDone = errors.New("ticket: the attempt is done and cannot be closed")
)

// AttemptClosed is what closing an attempt did, for the audit record.
type AttemptClosed struct {
	Number int
	// From is the status the attempt was in.
	From string
	// LeaseRevoked is whether an agent held it and was told to stop.
	LeaseRevoked bool
}

// CloseAttempt closes the numbered attempt of the ticket on a person's behalf.
func (s *Service) CloseAttempt(ctx context.Context, userID uuid.UUID, ticketID int64, number int) (*AttemptClosed, error) {
	closed, err := s.store.CloseAttempt(WithUser(ctx, userID), ticketID, number)
	if err != nil {
		return nil, err
	}
	// After the commit, so a refused close leaves no record of one.
	if s.events != nil {
		tenant, _ := db.TenantFrom(ctx)
		s.events.Record(ctx, audit.AttemptClosed,
			"user_id", userID, "tenant", tenant, "ticket", ticketID, "attempt", closed.Number,
			"from_status", closed.From, "lease_revoked", closed.LeaseRevoked)
	}
	return closed, nil
}

// CloseAttempt takes the claim lock, as every claim does, then the ticket's
// row, then its attempts' (the lock order AttemptRef names), so a claim either
// took the attempt first, and its lease is ended here, or finds it closed.
// Any lease on it ends through endLease, which drops its integration lock,
// and its agent's next heartbeat is told (LeaseRevokedError). The ticket's
// status is worked out again from the attempts left; the promoted attempt and
// a sibling's discussion are not touched.
func (s *PostgresStore) CloseAttempt(ctx context.Context, ticketID int64, number int) (*AttemptClosed, error) {
	var closed *AttemptClosed
	err := s.inTxSettling(ctx, func(q db.Querier) error {
		closed = nil
		if err := db.TakeAdvisoryLock(ctx, q, claimAdvisoryKey); err != nil {
			return err
		}
		if err := q.QueryRow(ctx, `SELECT id FROM tickets WHERE id = $1 FOR UPDATE`, ticketID).Scan(&ticketID); errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: #%d", ErrNotFound, ticketID)
		} else if err != nil {
			return fmt.Errorf("lock ticket: %w", err)
		}
		states, err := lockAttempts(ctx, q, ticketID)
		if err != nil {
			return err
		}
		var target *attemptStart
		for i := range states {
			if states[i].number == number {
				target = &states[i]
			}
		}
		ref := &AttemptRef{TicketID: ticketID, Number: number}
		switch {
		case target == nil:
			return fmt.Errorf("#%d has no attempt %d: %w", ticketID, number, ErrNoSuchAttempt)
		case target.promoted:
			return fmt.Errorf("%s: %w", ref, ErrClosePromoted)
		case AttemptStatus(target.status) == AttemptStatusClosed:
			return fmt.Errorf("%s: %w", ref, ErrAttemptClosed)
		case AttemptStatus(target.status) == AttemptStatusDone:
			return fmt.Errorf("%s: %w", ref, ErrAttemptDone)
		}

		var held bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM leases WHERE ticket_id = $1 AND attempt_number = $2 AND released_at IS NULL)`,
			ticketID, number).Scan(&held); err != nil {
			return fmt.Errorf("look for the attempt's lease: %w", err)
		}
		if err := revokeAttemptLease(ctx, q, ref); err != nil {
			return err
		}
		if err := userMover(userFrom(ctx), "closed by person").set(ctx, q); err != nil {
			return err
		}
		// The resume fields go too: nothing is owed to an agent for a closed
		// attempt.
		if err := writeAttempt(ctx, q, ref, &setList{
			clauses: []string{"status = 'closed'", "closed_at = now()", "updated_at = now()", "resume_agent_id = NULL", "affinity_until = NULL"}, first: 3,
		}); err != nil {
			return err
		}
		// A person's preparing and parked are not the attempts' to move.
		if _, err := deriveStatus(ctx, q, "t.id = $1 AND "+inQueueLifecycle, ticketID); err != nil {
			return err
		}
		closed = &AttemptClosed{Number: number, From: target.status, LeaseRevoked: held}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return closed, nil
}
