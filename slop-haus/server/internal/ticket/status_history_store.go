package ticket

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// StatusChange is one row of the ticket's status history: the ticket's own
// status, which with several attempts is worked out from all of them (see
// derivedStatus), so a row names no attempt.
type StatusChange struct {
	ID         int64
	FromStatus *Status
	ToStatus   Status
	ChangedAt  time.Time
	// ActorKind is agent, user or system. ActorID is nil for system, for a
	// user move whose route did not know the person, and for a person whose
	// account is deleted (ActorDeleted).
	ActorKind    string
	ActorID      *uuid.UUID
	ActorDeleted bool
	Cause        *StatusCause
}

// ListStatusChanges returns a ticket's status changes, oldest first. A
// ticket that does not exist (or is another tenant's) is ErrNotFound rather
// than an empty history, as ListRevisions does.
func (s *PostgresStore) ListStatusChanges(ctx context.Context, ticketID int64) ([]StatusChange, error) {
	var changes []StatusChange
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		var exists bool
		if err := q.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM tickets WHERE id = $1)`, ticketID).Scan(&exists); err != nil {
			return fmt.Errorf("find ticket: %w", err)
		}
		if !exists {
			return fmt.Errorf("%w: #%d", ErrNotFound, ticketID)
		}
		rows, err := q.Query(ctx, `
			SELECT id, from_status, to_status, changed_at, actor_kind, actor_id, actor_id_deleted, cause
			  FROM ticket_status_changes WHERE ticket_id = $1 ORDER BY id`, ticketID)
		if err != nil {
			return fmt.Errorf("list status changes: %w", err)
		}
		changes, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (StatusChange, error) {
			var c StatusChange
			var cause *string
			err := row.Scan(&c.ID, &c.FromStatus, &c.ToStatus, &c.ChangedAt, &c.ActorKind, &c.ActorID, &c.ActorDeleted, &cause)
			if cause != nil {
				c.Cause = ParseStatusCause(*cause)
			}
			return c, err
		})
		if err != nil {
			return fmt.Errorf("scan status change: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return changes, nil
}
