package ticket

import (
	"context"
	"fmt"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// SettledHook is told, after a transaction commits, which tickets it moved
// (their status, or one of their attempts'). It is called with no lock held.
type SettledHook func(ctx context.Context, ticketIDs []int64)

// inTxSettling is InTx that calls the settled hook for every ticket the
// transaction moved. What moved is read from the status history the database
// records on every change, so no path that writes a status can forget to say
// so: the history's newest ids are noted first and everything after them is
// this transaction's (or a concurrent one's, which only costs a redundant
// check). Without a hook it is InTx.
func (s *PostgresStore) inTxSettling(ctx context.Context, fn func(q db.Querier) error) error {
	if s.settled == nil && s.entered == nil {
		return s.db.InTx(ctx, fn)
	}
	var moved []int64
	var opened []*DiscussionEntry
	err := s.db.InTx(ctx, func(q db.Querier) error {
		moved, opened = nil, nil
		var ticketMark, attemptMark int64
		if err := q.QueryRow(ctx, `
			SELECT COALESCE((SELECT max(id) FROM ticket_status_changes), 0), COALESCE((SELECT max(id) FROM attempt_status_changes), 0)`).
			Scan(&ticketMark, &attemptMark); err != nil {
			return fmt.Errorf("note the status history: %w", err)
		}
		if err := fn(q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, `
			SELECT ticket_id FROM ticket_status_changes WHERE id > $1
			UNION
			SELECT ticket_id FROM attempt_status_changes WHERE id > $2
			ORDER BY 1`, ticketMark, attemptMark)
		if err != nil {
			return fmt.Errorf("read the status history: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("read the status history: %w", err)
			}
			moved = append(moved, id)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		opened, err = discussionsOpenedSince(ctx, q, attemptMark)
		return err
	})
	if err != nil {
		return err
	}
	if s.settled != nil && len(moved) > 0 {
		s.settled(ctx, moved)
	}
	if s.entered != nil && len(opened) > 0 {
		s.entered(ctx, opened)
	}
	return nil
}

// discussionsOpenedSince is the rounds of the attempts that moved into
// discussion after the history mark. Like the settled read it may include a
// concurrent transaction's, which the hook tolerates: it keys on the round.
func discussionsOpenedSince(ctx context.Context, q db.Querier, attemptMark int64) ([]*DiscussionEntry, error) {
	rows, err := q.Query(ctx, `
		SELECT DISTINCT a.ticket_id, a.number, a.discussion_round
		  FROM attempt_status_changes c
		  JOIN attempts a ON a.tenant_id = c.tenant_id AND a.ticket_id = c.ticket_id AND a.number = c.attempt_number
		 WHERE c.id > $1 AND c.to_status = 'discussion' AND a.status = 'discussion' AND a.discussion_round > 0
		 ORDER BY 1, 2, 3`, attemptMark)
	if err != nil {
		return nil, fmt.Errorf("read the discussions opened: %w", err)
	}
	defer rows.Close()
	var out []*DiscussionEntry
	for rows.Next() {
		e := &DiscussionEntry{}
		if err := rows.Scan(&e.TicketID, &e.Attempt, &e.Round); err != nil {
			return nil, fmt.Errorf("read the discussions opened: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
