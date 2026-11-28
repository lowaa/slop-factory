package local_test

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets/conformance"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets/local"
)

var t0 = time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC)

// backend is one database file; each Open is a new System over it, as a
// restarted daemon would open it.
type backend struct {
	path  string
	cfg   tickets.Config
	clock clock.Clock
}

func newBackend(t *testing.T, cfg *tickets.Config) *backend {
	return &backend{path: filepath.Join(t.TempDir(), "tickets.db"), cfg: *cfg, clock: clock.NewFake(t0)}
}

func (b *backend) Open(t *testing.T) tickets.TicketingSystem { return b.open(t) }

func (b *backend) open(t *testing.T) *local.System {
	t.Helper()
	s, err := local.Open(context.Background(), &local.Options{
		Path: b.path, Config: b.cfg, Logger: slog.New(slog.DiscardHandler), Clock: b.clock,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestConformance(t *testing.T) {
	t.Parallel()
	conformance.Run(t, func(t *testing.T, cfg *tickets.Config) conformance.Backend { return newBackend(t, cfg) })
}

func TestOpenNeedsALoggerAndAClock(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "x.db")
	_, err := local.Open(context.Background(), &local.Options{Path: path, Clock: clock.Real{}})
	require.Error(t, err)
	_, err = local.Open(context.Background(), &local.Options{Path: path, Logger: slog.New(slog.DiscardHandler)})
	require.Error(t, err)
}

// Timestamps come from the injected clock, not the wall.
func TestTimestampsAreTheClocks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := clock.NewFake(t0)
	b := &backend{path: filepath.Join(t.TempDir(), "x.db"), clock: fake}
	s := b.open(t)

	created, err := s.Create(ctx, ident.UserPrincipal(), &tickets.NewTicket{Title: "x"})
	require.NoError(t, err)
	assert.True(t, created.CreatedAt.Equal(t0))
	assert.True(t, created.UpdatedAt.Equal(t0))

	fake.Advance(time.Hour)
	updated, err := s.Update(ctx, ident.UserPrincipal(), created.ID, &tickets.TicketChange{Title: ptr("y")})
	require.NoError(t, err)
	assert.True(t, updated.CreatedAt.Equal(t0))
	assert.True(t, updated.UpdatedAt.Equal(t0.Add(time.Hour)))
	comment, err := s.Comment(ctx, ident.UserPrincipal(), created.ID, "hi")
	require.NoError(t, err)
	assert.True(t, comment.CreatedAt.Equal(t0.Add(time.Hour)))

	require.NoError(t, s.Start(ctx, ""))
	ev := <-s.Events()
	assert.True(t, ev.At.Equal(t0))
}

// The tickets survive a restart: they are in the file, not the System.
func TestTicketsSurviveAReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := &backend{path: filepath.Join(t.TempDir(), "x.db"), clock: clock.NewFake(t0)}
	first := b.open(t)
	work, err := first.Create(ctx, ident.UserPrincipal(), &tickets.NewTicket{Title: "kept"})
	require.NoError(t, err)
	require.NoError(t, first.Close())

	got, err := b.open(t).Get(ctx, work.ID)
	require.NoError(t, err)
	assert.Equal(t, work, got)
}

// A cursor past the end of this feed came from another database (or a
// restored backup); following it would silently skip that many events.
func TestStartRefusesACursorPastTheEnd(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	other := newBackend(t, &tickets.Config{}).open(t)
	for range 3 {
		_, err := other.Create(ctx, ident.UserPrincipal(), &tickets.NewTicket{Title: "x"})
		require.NoError(t, err)
	}
	require.NoError(t, other.Start(ctx, ""))
	var last tickets.TicketEvent
	for range 3 {
		last = <-other.Events()
	}

	fresh := newBackend(t, &tickets.Config{}).open(t)
	require.ErrorIs(t, fresh.Start(ctx, last.Cursor), tickets.ErrInvalidCursor)
	for _, c := range []tickets.Cursor{"-1", "12", "0000000000000000000x"} {
		require.ErrorIs(t, fresh.Start(ctx, c), tickets.ErrInvalidCursor, "%q", c)
	}
	require.NoError(t, fresh.Start(ctx, ""), "a refused Start doesn't count as one")
}

// A write by another process to the same file is in the log, and reaches
// a follower at its next wake-up. Two Systems on one file stand in for the
// two processes: their wake-ups are separate.
func TestAnotherProcesssWriteIsDeliveredAtTheNextWakeUp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := newBackend(t, &tickets.Config{})
	daemon, other := b.open(t), b.open(t)
	require.NoError(t, daemon.Start(ctx, ""))

	foreign, err := other.Create(ctx, ident.UserPrincipal(), &tickets.NewTicket{Title: "from elsewhere"})
	require.NoError(t, err)
	own, err := daemon.Create(ctx, ident.UserPrincipal(), &tickets.NewTicket{Title: "own"})
	require.NoError(t, err)

	assert.Equal(t, foreign.ID, (<-daemon.Events()).Ticket, "delivered in log order")
	assert.Equal(t, own.ID, (<-daemon.Events()).Ticket)
}

func TestCloseStopsARunningFeed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := newBackend(t, &tickets.Config{})
	s := b.open(t)
	_, err := s.Create(ctx, ident.UserPrincipal(), &tickets.NewTicket{Title: "unread"})
	require.NoError(t, err)
	require.NoError(t, s.Start(ctx, ""))
	require.NoError(t, s.Close())
	for range s.Events() {
		// Closed by Close, whatever it held.
	}
}

func ptr[T any](v T) *T { return &v }
