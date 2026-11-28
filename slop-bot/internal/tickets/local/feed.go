package local

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

// feedBatch bounds what the follower holds in memory however far behind
// the consumer is: the rest stays in the events table.
const feedBatch = 256

// feedRetry is how long the follower waits to read the log again after a
// failed read, if no write wakes it first.
const feedRetry = time.Second

// feed follows the events table and sends each row on events, blocking on
// the consumer (see tickets.TicketingSystem for why it blocks).
type feed struct {
	s      *System
	events chan tickets.TicketEvent

	mu      sync.Mutex
	started bool
	stopped bool
	cancel  context.CancelFunc
	// done is closed once the follower has exited and closed events.
	done chan struct{}
}

func newFeed(s *System) *feed {
	// Unbuffered: the follower's position is exactly what the consumer has
	// taken, which keeps "blocked on the consumer" literal.
	return &feed{s: s, events: make(chan tickets.TicketEvent), done: make(chan struct{})}
}

// Events implements tickets.TicketingSystem.
func (s *System) Events() <-chan tickets.TicketEvent { return s.feed.events }

// Start implements tickets.TicketingSystem.
func (s *System) Start(ctx context.Context, after tickets.Cursor) error {
	seq, err := decodeCursor(after)
	if err != nil {
		return err
	}
	// A cursor past the end came from another database. Following it would
	// silently skip that many events here.
	var last int64
	if err := s.db.NewRaw("SELECT coalesce(max(seq), 0) FROM events").Scan(ctx, &last); err != nil {
		return fmt.Errorf("local: reading the feed's end: %w", err)
	}
	if seq > last {
		return fmt.Errorf("%w: %s is past this feed's end", tickets.ErrInvalidCursor, after)
	}
	f := s.feed
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopped {
		return tickets.ErrStopped
	}
	if f.started {
		return tickets.ErrAlreadyStarted
	}
	// Not Start's ctx: that bounds the setup, and the feed runs until Stop.
	runCtx, cancel := context.WithCancel(context.Background())
	f.started, f.cancel = true, cancel
	go f.follow(runCtx, seq)
	return nil
}

// Stop implements tickets.TicketingSystem.
func (s *System) Stop(ctx context.Context) error {
	f := s.feed
	f.mu.Lock()
	if !f.stopped {
		f.stopped = true
		if f.started {
			f.cancel()
		} else {
			close(f.events)
			close(f.done)
		}
	}
	f.mu.Unlock()
	select {
	case <-f.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("local: waiting for the feed to stop: %w", ctx.Err())
	}
}

func (f *feed) follow(ctx context.Context, after int64) {
	defer close(f.done)
	defer close(f.events)
	for {
		// Taken before the read, so a write committing after the read
		// still wakes this loop.
		wake := f.s.wake.wait()
		batch, err := f.s.eventsAfter(ctx, after)
		if ctx.Err() != nil {
			return
		}
		var retry <-chan time.Time
		if err != nil {
			f.s.log.ErrorContext(ctx, "reading the ticket feed", "after", after, "error", err)
			retry = f.s.clock.After(feedRetry)
		}
		for i := range batch {
			select {
			case f.events <- batch[i].toEvent():
				after = batch[i].Seq
			case <-ctx.Done():
				return
			}
		}
		if len(batch) == feedBatch {
			continue
		}
		select {
		case <-wake:
		case <-retry:
		case <-ctx.Done():
			return
		}
	}
}

func (s *System) eventsAfter(ctx context.Context, after int64) ([]eventRow, error) {
	var rows []eventRow
	err := s.db.NewSelect().Model(&rows).Where("seq > ?", after).Order("seq").Limit(feedBatch).Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("local: reading events after %d: %w", after, err)
	}
	return rows, nil
}
