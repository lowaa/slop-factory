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
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake/faketest"
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
	// No turn will end on an idle agent: the next screen poll retries. The
	// fake draws its cancel only once the hook that reported it returns, and
	// on the fake clock the poll's trust in that hook lapses at once: polled
	// before the redraw, the screen shows the turn still running, and the
	// poll starts it again.
	r.idleOnScreen(t)
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

// A harness that names its conversation only after a first prompt (agy)
// launches with one, and is ready once that turn ends: the turn is its
// start-up, not the core's.
func TestWorkBeforeReadyIsNotReported(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	base := r.Harness.Profile()
	p := *base
	p.ParseHook = func(event string, payload []byte) (*tuidriver.HookCall, error) {
		call, err := base.ParseHook(event, payload)
		if err != nil {
			return nil, err
		}
		switch event {
		case fake.HookSessionStart:
			call.Events = nil
		case fake.HookTurnEnd:
			call.Events = append(call.Events, harness.Event{Kind: harness.Ready})
		}
		return call, nil
	}
	type started struct {
		conv *tuidriver.Conversation
		err  error
	}
	done := make(chan started, 1)
	go func() {
		conv, err := tuidriver.Start(r.ctx, &tuidriver.Options{
			Profile: &p, Host: r.Host, Hub: r.Hub, Session: fake.SessionName("a1"),
			Pointer: faketest.Pointer, Clock: r.clk, PollInterval: poll, ConfirmTimeout: confirm,
		}, faketest.Workspace(t, "a1"), nil)
		done <- started{conv, err}
	}()
	_, _, err := r.Puppet.Started(r.ctx)
	require.NoError(t, err)
	r.fake = r.Puppet.Session()
	r.do(t, fake.Command{Cmd: fake.CmdTurnStart})
	r.do(t, fake.Command{Cmd: fake.CmdToolStart, Tool: "boot"})
	r.do(t, fake.Command{Cmd: fake.CmdToolEnd, Tool: "boot"})
	r.do(t, fake.Command{Cmd: fake.CmdTurnEnd})
	s := <-done
	require.NoError(t, s.err)
	t.Cleanup(func() { _ = s.conv.Stop(context.Background(), true) })
	assert.Equal(t, harness.Ready, (<-s.conv.Events()).Kind, "the first event")
	assert.NotEmpty(t, s.conv.Ref().ID)
}

// laggingHost shows the busy footer while lag is set, as agy's screen does
// while its Stop hook runs, and counts what is typed meanwhile.
type laggingHost struct {
	*sessionhost.Tmux
	lag    atomic.Bool
	typed  atomic.Int32
	lagged chan struct{}
	// shown, when set, is the screen shown while lagging instead.
	shown string
}

func (h *laggingHost) Capture(ctx context.Context, session string) (string, error) {
	if h.lag.Load() {
		select {
		case h.lagged <- struct{}{}:
		default:
		}
		if h.shown != "" {
			return h.shown, nil
		}
		return "[fake] busy (esc to cancel)", nil
	}
	return h.Tmux.Capture(ctx, session)
}

func (h *laggingHost) SendText(ctx context.Context, session, text string) error {
	if h.lag.Load() {
		h.typed.Add(1)
	}
	return h.Tmux.SendText(ctx, session, text)
}

func (h *laggingHost) SendKeys(ctx context.Context, session string, keys ...string) error {
	if h.lag.Load() {
		h.typed.Add(1)
	}
	return h.Tmux.SendKeys(ctx, session, keys...)
}

// A turn's end can reach the kit, by hook, before the TUI has finished it on
// screen: the quit waits for the idle prompt, where a TUI acts on it.
func TestStopQuitsOnceTheScreenIsIdle(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	host := &laggingHost{Tmux: r.Host, lagged: make(chan struct{}, 1)}
	r.Harness.Host = host
	r.conv = r.launch(t, nil)

	host.lag.Store(true)
	stopped := make(chan error, 1)
	go func() { stopped <- r.conv.Stop(r.ctx, false) }()
	// Twice: a quit typed after the first look would be counted by now.
	for range 2 {
		select {
		case <-host.lagged:
		case err := <-stopped:
			require.Failf(t, "Stop didn't wait for the screen", "returned %v", err)
		}
	}
	assert.Zero(t, host.typed.Load(), "nothing typed over a busy screen")
	host.lag.Store(false)
	require.NoError(t, <-stopped)
	assert.Equal(t, 0, next(r.ctx, t, r.conv, harness.Exited).ExitCode)
}

// Close lets go of the session for Adopt: a Stop waiting on the screen then
// types nothing into it.
func TestCloseDuringStopsWaitTypesNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	host := &laggingHost{Tmux: r.Host, lagged: make(chan struct{}, 1)}
	r.Harness.Host = host
	r.conv = r.launch(t, nil)

	host.lag.Store(true)
	stopped := make(chan error, 1)
	go func() { stopped <- r.conv.Stop(r.ctx, false) }()
	<-host.lagged
	require.NoError(t, r.conv.Close())
	assert.ErrorIs(t, <-stopped, tuidriver.ErrClosed)
	assert.Zero(t, host.typed.Load())
	st, err := r.Host.Status(r.ctx, fake.SessionName("a1"))
	require.NoError(t, err)
	assert.False(t, st.Dead, "the session lives on")
}

// A cancel can hide the idle footer (agy moves a queued message into the
// prompt): the cancel on screen still ends the turn.
func TestACancelWithoutAnIdleFooterEndsTheTurn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	host := &laggingHost{Tmux: r.Host, lagged: make(chan struct{}, 1), shown: "interrupted\n> a queued message\n"}
	r.Harness.Host = host
	r.conv = r.launch(t, nil)
	r.do(t, fake.Command{Cmd: fake.CmdQuietCancel, On: true})
	r.inTurn(t)

	interrupted := make(chan error, 1)
	go func() { interrupted <- r.conv.Interrupt(r.ctx) }()
	r.await(t, fake.RepInterrupt)
	host.lag.Store(true)
	r.clk.Advance(poll)
	assert.Equal(t, harness.TurnInterrupted, next(r.ctx, t, r.conv, harness.TurnEnded).Outcome)
	require.NoError(t, <-interrupted)
}
