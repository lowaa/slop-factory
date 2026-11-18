// Package ssestream is the auth-bounded, resource-capped plumbing under the
// server's polling event streams: slots that cap how many are open, a
// lifetime tied to the access token that opened one, and the poll/keepalive
// loop. A stream supplies only what one poll does.
package ssestream

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2/sse"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// Options tunes a stream. Zero fields take the defaults given to WithDefaults.
type Options struct {
	// PollInterval is how often a stream polls. Polling rather than
	// LISTEN/NOTIFY: a second's lag costs nobody anything, a connection
	// pinned per open stream does.
	PollInterval time.Duration
	// KeepAlive is the longest the stream goes without writing. huma's sse
	// gives each write sse.WriteTimeout to land and leaves that deadline in
	// place after it, so a stream quiet for longer than that breaks on its
	// next write; a comment in between keeps it open, and lets a dead
	// client's connection be noticed rather than held.
	KeepAlive time.Duration
	// MaxLifetime bounds a stream by the access token that opened it: it
	// ends once the token it authenticated with has expired, so a session
	// signed out or revoked elsewhere stops getting events then. The
	// browser reconnects by itself and is authenticated afresh.
	MaxLifetime time.Duration
	// PerUser and Total cap the streams open at once, per signed-in user
	// and over the whole process: each one polls the database every
	// PollInterval for as long as it is open.
	PerUser int
	Total   int
	// BusyRetry is what a stream refused by a cap tells the browser to wait
	// before trying again.
	BusyRetry time.Duration
	// Clock times the poll, the keepalive and the lifetime; nil is the wall
	// clock.
	Clock clock.Clock
}

// WithDefaults fills o's zero fields from defaults, then KeepAlive and Clock
// from the package's own.
func (o Options) WithDefaults(defaults Options) Options {
	if o.PollInterval <= 0 {
		o.PollInterval = defaults.PollInterval
	}
	if o.KeepAlive <= 0 {
		o.KeepAlive = sse.WriteTimeout / 2
	}
	if o.MaxLifetime <= 0 {
		o.MaxLifetime = defaults.MaxLifetime
	}
	if o.PerUser <= 0 {
		o.PerUser = defaults.PerUser
	}
	if o.Total <= 0 {
		o.Total = defaults.Total
	}
	if o.BusyRetry <= 0 {
		o.BusyRetry = defaults.BusyRetry
	}
	o.Clock = clock.Or(o.Clock)
	return o
}

// Slots counts the streams open per user and in all.
type Slots struct {
	mu      sync.Mutex
	perUser int
	total   int
	open    int
	byUser  map[uuid.UUID]int
}

func NewSlots(perUser, total int) *Slots {
	return &Slots{perUser: perUser, total: total, byUser: map[uuid.UUID]int{}}
}

// Acquire takes a slot for userID, returning the func that gives it back, or
// false when either cap is reached.
func (s *Slots) Acquire(userID uuid.UUID) (release func(), ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open >= s.total || s.byUser[userID] >= s.perUser {
		return nil, false
	}
	s.open++
	s.byUser[userID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.open--
			if s.byUser[userID]--; s.byUser[userID] == 0 {
				delete(s.byUser, userID)
			}
		})
	}, true
}

// Stream is one kind of stream's share of the machinery: its options and the
// slots its open streams count against.
type Stream struct {
	opts  Options
	slots *Slots
	busy  string
}

// New makes a Stream from options already run through WithDefaults. kind
// names it in the comment a refused stream is sent ("too many <kind> streams
// open").
func New(opts Options, kind string) *Stream {
	return &Stream{
		opts:  opts,
		slots: NewSlots(opts.PerUser, opts.Total),
		busy:  "too many " + kind + " streams open",
	}
}

// Clock is the clock the stream's timings run on.
func (s *Stream) Clock() clock.Clock { return s.opts.Clock }

// Open takes a slot for user and bounds ctx by the lifetime of their token.
// When a cap is reached it tells the browser to retry and returns false. The
// response has already started (sse sends its headers before calling the
// handler), so this can't be a 429; the browser treats a stream that ends as
// one to reconnect to, after the retry. Otherwise the caller defers release,
// which also cancels the returned ctx.
func (s *Stream) Open(ctx context.Context, user *authn.User, send sse.Sender) (_ context.Context, release func(), ok bool) {
	releaseSlot, ok := s.slots.Acquire(user.ID)
	if !ok {
		_ = send(sse.Message{Retry: int(s.opts.BusyRetry.Milliseconds()), Comment: s.busy})
		return ctx, nil, false
	}
	ctx, cancel := clock.WithDeadline(ctx, s.opts.Clock, s.Deadline(user, s.opts.Clock.Now()))
	return ctx, func() {
		cancel()
		releaseSlot()
	}, true
}

// Deadline is when the access token that opened the stream expires. No
// floor: a stream opened in a token's last seconds ends with it, and the
// reconnect that follows is refused until the session is refreshed, so
// there is no loop to guard against -- only events past expiry to avoid.
func (s *Stream) Deadline(user *authn.User, now time.Time) time.Time {
	if user.IssuedAt.IsZero() {
		return now.Add(s.opts.MaxLifetime)
	}
	return user.IssuedAt.Add(s.opts.MaxLifetime)
}

// Loop polls until the stream is over. tick does one poll: wrote says it
// wrote an event (which defers the next keepalive), stop ends the stream.
// The first poll is immediate. Loop also ends on ctx.Done(), on closed (nil
// for none) and on a failed keepalive write.
func (s *Stream) Loop(ctx context.Context, send sse.Sender, closed <-chan struct{}, tick func(ctx context.Context) (wrote, stop bool)) {
	lastWrite := s.opts.Clock.Now()
	ticker := s.opts.Clock.NewTicker(s.opts.PollInterval)
	defer ticker.Stop()
	for {
		wrote, stop := tick(ctx)
		if stop {
			return
		}
		if wrote {
			lastWrite = s.opts.Clock.Now()
		}
		if clock.Since(s.opts.Clock, lastWrite) >= s.opts.KeepAlive {
			if err := send.Comment("keepalive"); err != nil {
				return
			}
			lastWrite = s.opts.Clock.Now()
		}

		select {
		case <-ctx.Done():
			return
		case <-closed:
			return
		case <-ticker.C():
		}
	}
}

// ID is an event's SSE `id:` field for a sequence number: empty, so no field
// is written, for one that is not positive, as huma's deprecated
// sse.Message.ID did. A client resumes from the last id it saw, and these
// streams have never sent a zero one.
func ID(seq int64) string {
	if seq <= 0 {
		return ""
	}
	return strconv.FormatInt(seq, 10)
}
