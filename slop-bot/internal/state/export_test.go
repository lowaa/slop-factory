package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
)

// SetBeforeWait and SetBeforeRead install Follow's test hooks. Call them
// before any Follow.
func SetBeforeWait(e *Events, f func()) { e.beforeWait = f }

func SetBeforeRead(e *Events, f func()) { e.beforeRead = f }

// PushBack queues ticket behind everything waiting in pool, in the back
// lane. A ticket already waiting in pool keeps its place; one waiting in
// the other pool moves.
func (q *Queue) PushBack(ctx context.Context, pool Pool, ticket ident.TicketID) error {
	return q.push(ctx, pool, ticket, laneBack)
}

// Pop takes the ticket at the front of pool. ok is false when pool is
// empty.
func (q *Queue) Pop(ctx context.Context, pool Pool) (ticket ident.TicketID, ok bool, err error) {
	var id string
	err = q.db.NewRaw(`DELETE FROM queue WHERE ticket_id = (
			SELECT ticket_id FROM queue WHERE pool = ? ORDER BY lane, position LIMIT 1
		) RETURNING ticket_id`, string(pool)).Scan(ctx, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("state: popping the %s queue: %w", pool, err)
	}
	return ident.TicketID(id), true, nil
}
