package ticket

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// SetPriority sets every ticket in ids to priority, in one transaction, or
// refuses the whole batch with a *BatchRefusedError naming the tickets not
// found.
//
// No claim lock, like the single PATCH's priority write: a claim reads
// priority only to order what it may take, so one ordering by the old
// priorities is as good as one by the new -- and a batch holding the lock
// would stall every agent's claim for nothing. The single UPDATE is atomic
// on its own.
func (s *PostgresStore) SetPriority(ctx context.Context, ids []int64, priority model.Priority) ([]Ticket, error) {
	var updated []Ticket
	err := s.db.InTx(ctx, func(tx db.Querier) error {
		// Locked in id order before the write, as a move writes them (see
		// Move.decide);
		// an UPDATE alone locks in whatever order its plan visits rows.
		rows, err := tx.Query(ctx, `
			SELECT id FROM tickets WHERE id = ANY($1) ORDER BY id FOR UPDATE`, ids)
		if err != nil {
			return fmt.Errorf("lock tickets: %w", err)
		}
		found, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return fmt.Errorf("scan locked ids: %w", err)
		}
		if refusals := notFound(slices.Sorted(slices.Values(ids)), found); len(refusals) > 0 {
			return &BatchRefusedError{Refusals: refusals}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE tickets SET priority = $2, updated_at = now() WHERE id = ANY($1)`,
			ids, string(priority)); err != nil {
			return fmt.Errorf("set priority: %w", err)
		}
		updated, err = loadTickets(ctx, tx, ids)
		return err
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// loadTickets reads each of ids inside the transaction that changed them,
// in the order ids names them -- in one query per table rather than per
// ticket, since the batch still holds the claim lock while it reads.
func loadTickets(ctx context.Context, q db.Querier, ids []int64) ([]Ticket, error) {
	listed, err := listTickets(ctx, q, &ListFilter{IDs: ids})
	if err != nil {
		return nil, err
	}
	return inOrder(ids, listed)
}
