package tuidriver_test

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost/tmuxtest"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/tuidriver"
)

func TestAResumedConversationIsAdoptedWithItsID(t *testing.T) {
	t.Parallel()
	r := start(t)
	ref := r.conv.Ref()
	require.NoError(t, r.conv.Stop(r.ctx, false))
	next(r.ctx, t, r.conv, harness.Exited)

	// The harness repeats the id it resumed, so no hook brings a new one.
	resumed := r.launch(t, &ref)
	require.NoError(t, resumed.Close())
	convs, err := r.Harness.Adopt(r.ctx)
	require.NoError(t, err)
	require.Len(t, convs, 1)
	t.Cleanup(func() { _ = convs[0].Stop(context.Background(), true) })
	assert.Equal(t, ref, convs[0].Ref())
}

func TestStopDoesNotTypeWhileAPersonIsAttached(t *testing.T) {
	t.Parallel()
	r := start(t)
	detach := tmuxtest.Attach(r.ctx, t, r.Host, fake.SessionName("a1"))
	assert.ErrorIs(t, r.conv.Stop(r.ctx, false), tuidriver.ErrAttached)
	detach()
	require.NoError(t, r.conv.Stop(r.ctx, false))
	assert.Equal(t, 0, next(r.ctx, t, r.conv, harness.Exited).ExitCode)
}

func TestAClosedConversationNoLongerDrives(t *testing.T) {
	t.Parallel()
	r := start(t)
	require.NoError(t, r.conv.Close())
	assert.ErrorIs(t, r.conv.Send(r.ctx, msg("hello")), tuidriver.ErrClosed)
	assert.ErrorIs(t, r.conv.Interrupt(r.ctx), tuidriver.ErrClosed)
	assert.ErrorIs(t, r.conv.Stop(r.ctx, true), tuidriver.ErrClosed)
	st, err := r.Host.Status(r.ctx, fake.SessionName("a1"))
	require.NoError(t, err)
	assert.False(t, st.Dead, "the session lives on")
}

// flakyHost fails Status, or Paste, while armed, as when tmux can't be run.
type flakyHost struct {
	*sessionhost.Tmux
	fail      atomic.Int32
	failed    chan struct{}
	failPaste atomic.Int32
}

func (h *flakyHost) Paste(ctx context.Context, session, text string) error {
	if h.failPaste.Add(-1) >= 0 {
		return errors.New("fork/exec tmux: resource temporarily unavailable")
	}
	return h.Tmux.Paste(ctx, session, text)
}

// logged signals each time the kit logs msg: the one place a held message's
// requeue shows.
type logged struct {
	msg string
	ch  chan struct{}
}

func (l *logged) Enabled(context.Context, slog.Level) bool { return true }
func (l *logged) WithAttrs([]slog.Attr) slog.Handler       { return l }
func (l *logged) WithGroup(string) slog.Handler            { return l }
func (l *logged) Handle(_ context.Context, r slog.Record) error {
	if r.Message == l.msg {
		l.ch <- struct{}{}
	}
	return nil
}

func TestAHeldMessageIsRetriedOnAnIdleAgent(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	host := &flakyHost{Tmux: r.Host, failed: make(chan struct{}, 1)}
	held := &logged{msg: "tuidriver: holding messages", ch: make(chan struct{}, 1)}
	r.Harness.Host = host
	r.Harness.Logger = slog.New(held)
	r.conv = r.launch(t, nil)

	r.inTurn(t)
	require.NoError(t, r.conv.Send(r.ctx, msg("held")))
	host.failPaste.Store(1)
	// A cancelled turn can't carry it, so it is typed once idle: that paste
	// fails, nothing was typed, and it is held.
	require.NoError(t, r.conv.Interrupt(r.ctx))
	<-held.ch
	// No turn will end on an idle agent: the next screen poll retries.
	r.clk.Advance(poll)
	assert.Equal(t, pointer("held"), r.await(t, fake.RepUser).Text)
}

func (h *flakyHost) Status(ctx context.Context, session string) (*sessionhost.Status, error) {
	if h.fail.Add(-1) >= 0 {
		h.failed <- struct{}{}
		return nil, errors.New("fork/exec tmux: resource temporarily unavailable")
	}
	return h.Tmux.Status(ctx, session)
}

func TestATmuxFailureIsNotAnExit(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	host := &flakyHost{Tmux: r.Host, failed: make(chan struct{}, 1)}
	r.Harness.Host = host
	r.conv = r.launch(t, nil)

	host.fail.Store(1)
	<-host.failed // the exit watcher's next look
	backingOff := make(chan struct{})
	go func() {
		r.clk.BlockUntil(2) // the screen ticker, and the back-off
		close(backingOff)
	}()
	select {
	case ev := <-r.conv.Events():
		require.Failf(t, "an event for a failed status", "%+v", ev)
	case <-backingOff:
	}
	r.clk.Advance(poll)
	require.NoError(t, r.conv.Send(r.ctx, msg("still here")))
	assert.Equal(t, pointer("still here"), r.await(t, fake.RepUser).Text)
}
