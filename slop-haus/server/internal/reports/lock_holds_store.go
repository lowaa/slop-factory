package reports

import (
	"context"
	"fmt"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// A hold overlaps the period if it began before its end and has not been
// released before its start. A wait is part of the hold that ended it, so one
// that began before the period but ended in it is read through that hold.
const queryLockHolds = `
SELECT name, ticket_id, queued_at, acquired_at, released_at
  FROM lock_holds
 WHERE acquired_at < $2 AND (released_at IS NULL OR released_at > $1)
 ORDER BY acquired_at, id`

// LockHistoryRows reads the holds a lock history over [from, to) is computed
// from.
func (s *PostgresStore) LockHistoryRows(ctx context.Context, from, to time.Time) (*LockHistoryInput, error) {
	in := &LockHistoryInput{From: from, To: to}
	err := s.readTx(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, queryLockHolds, from, to)
		if err != nil {
			return fmt.Errorf("reports: lock holds: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var h LockHold
			if err := rows.Scan(&h.Name, &h.TicketID, &h.QueuedAt, &h.AcquiredAt, &h.ReleasedAt); err != nil {
				return fmt.Errorf("reports: scan lock hold: %w", err)
			}
			in.Holds = append(in.Holds, h)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return in, nil
}
