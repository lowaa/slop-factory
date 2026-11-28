package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	clock "github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/sqlitex"
)

// QueuedTicket is a ticket waiting for a slot in its pool.
type QueuedTicket struct {
	Ticket ident.TicketID
	Pool   Pool
	// Front is set for a ticket queued with PushFront.
	Front      bool
	EnqueuedAt time.Time
}

// The lanes of a pool, in the order they are served.
const (
	laneFront = 0
	laneBack  = 1
)

type queueRow struct {
	bun.BaseModel `bun:"table:queue"`

	TicketID   string `bun:"ticket_id,pk"`
	Pool       string `bun:"pool"`
	Lane       int    `bun:"lane"`
	Position   int64  `bun:"position"`
	EnqueuedAt int64  `bun:"enqueued_at"`
}

// Queue holds the tickets whose agents wait for a slot, per pool, in order,
// so a restart serves them in the same order. A ticket waits in at most one
// place. The scheduler queues only to the front lane; the back lane, for
// new work, is kept for the tests of the lane order (export_test.go).
type Queue struct {
	db    *sqlitex.DB
	clock clock.Clock
}

// PushFront queues ticket ahead of pool's new work, behind tickets pushed
// to the front before it: an answered agent resumes before tickets not yet
// started, so the delay between the answer and its next turn stays short.
// A ticket waiting at the back moves up; one already at the front stays.
func (q *Queue) PushFront(ctx context.Context, pool Pool, ticket ident.TicketID) error {
	return q.push(ctx, pool, ticket, laneFront)
}

// List returns pool's waiting tickets, in the order they are served: the
// front lane first, then first come first served.
func (q *Queue) List(ctx context.Context, pool Pool) ([]QueuedTicket, error) {
	var rows []queueRow
	if err := q.db.NewSelect().Model(&rows).Where("pool = ?", string(pool)).Order("lane", "position").Scan(ctx); err != nil {
		return nil, fmt.Errorf("state: listing the %s queue: %w", pool, err)
	}
	out := make([]QueuedTicket, len(rows))
	for i, r := range rows {
		out[i] = QueuedTicket{
			Ticket: ident.TicketID(r.TicketID), Pool: Pool(r.Pool), Front: r.Lane == laneFront,
			EnqueuedAt: fromMillis(r.EnqueuedAt),
		}
	}
	return out, nil
}

// Remove takes ticket out of the queue, e.g. once it stops being eligible.
// It reports whether the ticket was queued.
func (q *Queue) Remove(ctx context.Context, ticket ident.TicketID) (bool, error) {
	res, err := q.db.NewDelete().Model((*queueRow)(nil)).Where("ticket_id = ?", string(ticket)).Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("state: removing %s from the queue: %w", ticket, err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (q *Queue) push(ctx context.Context, pool Pool, ticket ident.TicketID, lane int) error {
	return q.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var cur queueRow
		err := tx.NewSelect().Model(&cur).Where("ticket_id = ?", string(ticket)).Scan(ctx)
		switch {
		case err == nil && cur.Pool == string(pool) && cur.Lane <= lane:
			// Already waiting here, as far forward or further.
			return nil
		case err == nil:
			// Moving pools keeps a front-lane place: an answered agent
			// whose ticket turns urgent still goes ahead of new work.
			lane = min(lane, cur.Lane)
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("state: reading queue entry of %s: %w", ticket, err)
		}
		if _, err := tx.NewDelete().Model((*queueRow)(nil)).Where("ticket_id = ?", string(ticket)).Exec(ctx); err != nil {
			return fmt.Errorf("state: requeueing %s: %w", ticket, err)
		}
		_, err = tx.NewRaw(`INSERT INTO queue (ticket_id, pool, lane, position, enqueued_at)
			SELECT ?, ?, ?, coalesce(max(position), 0) + 1, ? FROM queue`,
			string(ticket), string(pool), lane, toMillis(q.clock.Now())).Exec(ctx)
		if err != nil {
			return fmt.Errorf("state: queueing %s in %s: %w", ticket, pool, err)
		}
		return nil
	})
}
