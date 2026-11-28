package state

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/uptrace/bun"

	clock "github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/sqlitex"
)

// EventType names what happened. The emitters own the values.
type EventType string

// Event is one entry in the ordered event log.
type Event struct {
	// Seq orders the log, and is a follower's cursor. It only grows, and is
	// never reused, even once pruning deletes rows.
	Seq  int64
	At   time.Time
	Type EventType
	// Agent and Ticket are the subject, either or both, when there is one.
	Agent   ident.AgentID
	Ticket  ident.TicketID
	Payload json.RawMessage
}

// NewEvent is what Append needs.
type NewEvent struct {
	Type    EventType
	Agent   ident.AgentID
	Ticket  ident.TicketID
	Payload json.RawMessage
}

// DefaultEventLimit is a page of events when a read's limit is 0.
const DefaultEventLimit = 500

type eventRow struct {
	bun.BaseModel `bun:"table:events"`

	Seq      int64  `bun:"seq,pk,autoincrement"`
	At       int64  `bun:"at"`
	Type     string `bun:"type"`
	AgentID  string `bun:"agent_id"`
	TicketID string `bun:"ticket_id"`
	Payload  string `bun:"payload"`
}

func (r *eventRow) event() Event {
	return Event{
		Seq: r.Seq, At: fromMillis(r.At), Type: EventType(r.Type),
		Agent: ident.AgentID(r.AgentID), Ticket: ident.TicketID(r.TicketID), Payload: json.RawMessage(r.Payload),
	}
}

// Events is the event log behind `slop-bot events --follow`. Followers
// block in Follow until Append commits something past their cursor, with no
// polling: tests synchronise on it.
type Events struct {
	db    *sqlitex.DB
	clock clock.Clock

	mu sync.Mutex
	// changed is closed, and replaced, by every Append once it has
	// committed. A follower takes it before reading, so an append that
	// commits after the read closes the channel the follower waits on.
	changed   chan struct{}
	closed    chan struct{}
	closeOnce sync.Once

	// The hooks, set only by tests, pin Follow at its two race windows.
	// beforeRead runs once Follow has checked for Close, before it reads:
	// where a Close can land. beforeWait runs between finding nothing and
	// waiting: where an append must not get lost.
	beforeRead func()
	beforeWait func()
}

func newEvents(db *sqlitex.DB, clk clock.Clock) *Events {
	return &Events{db: db, clock: clk, changed: make(chan struct{}), closed: make(chan struct{})}
}

// Append adds an event to the end of the log, wakes the followers, and
// returns its seq.
//
// Seq order is commit order because SQLite has one writer at a time: the
// row is numbered inside its write transaction, so no follower can see seq
// n+1 committed while n is still to come.
func (e *Events) Append(ctx context.Context, n *NewEvent) (int64, error) {
	payload := "{}"
	if len(n.Payload) > 0 {
		payload = string(n.Payload)
	}
	row := &eventRow{
		At: toMillis(e.clock.Now()), Type: string(n.Type),
		AgentID: string(n.Agent), TicketID: string(n.Ticket), Payload: payload,
	}
	if _, err := e.db.NewInsert().Model(row).Exec(ctx); err != nil {
		return 0, fmt.Errorf("state: appending %s event: %w", n.Type, err)
	}
	e.mu.Lock()
	close(e.changed)
	e.changed = make(chan struct{})
	e.mu.Unlock()
	return row.Seq, nil
}

// Latest is the highest seq written, or 0 when there are none: where a
// follower that wants only what happens from now on starts.
func (e *Events) Latest(ctx context.Context) (int64, error) {
	var seq int64
	if err := e.db.NewRaw("SELECT coalesce(max(seq), 0) FROM events").Scan(ctx, &seq); err != nil {
		return 0, fmt.Errorf("state: reading the latest event: %w", err)
	}
	return seq, nil
}

// ReadAfter returns up to limit events with seq above after, in order. A
// limit of 0 means DefaultEventLimit.
func (e *Events) ReadAfter(ctx context.Context, after int64, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = DefaultEventLimit
	}
	var rows []eventRow
	if err := e.db.NewSelect().Model(&rows).Where("seq > ?", after).Order("seq").Limit(int64(limit)).Scan(ctx); err != nil {
		return nil, fmt.Errorf("state: reading events after %d: %w", after, err)
	}
	out := make([]Event, len(rows))
	for i := range rows {
		out[i] = rows[i].event()
	}
	return out, nil
}

// Follow is ReadAfter that waits: it returns as soon as there is at least
// one event past after. A follower resumes from the last Seq it saw. It
// returns ctx's error when ctx ends, and ErrClosed once Close is called.
func (e *Events) Follow(ctx context.Context, after int64, limit int) ([]Event, error) {
	for {
		select {
		case <-e.closed:
			return nil, ErrClosed
		default:
		}
		e.mu.Lock()
		changed := e.changed
		e.mu.Unlock()

		if e.beforeRead != nil {
			e.beforeRead()
		}
		evs, err := e.ReadAfter(ctx, after, limit)
		if err != nil {
			// State.Close closes the database just after waking followers,
			// so a read in flight then fails with the driver's error.
			select {
			case <-e.closed:
				return nil, ErrClosed
			default:
			}
			return nil, err
		}
		if len(evs) > 0 {
			return evs, nil
		}
		if e.beforeWait != nil {
			e.beforeWait()
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-e.closed:
			return nil, ErrClosed
		}
	}
}

// Close wakes every follower with ErrClosed; Follow returns it from then on.
func (e *Events) Close() {
	e.closeOnce.Do(func() { close(e.closed) })
}
