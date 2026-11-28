package tuidriver_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/tuidriver"
)

// faultyHost lets a test fail, or reshape, single host calls while the rest
// go to tmux. A nil hook passes the call through.
type faultyHost struct {
	*sessionhost.Tmux
	setTag  func(ctx context.Context) error
	status  func(ctx context.Context) error
	kill    func(ctx context.Context) error
	capture func(ctx context.Context, real func() (string, error)) (string, error)
}

func (h *faultyHost) SetTag(ctx context.Context, session, name, value string) error {
	if h.setTag != nil {
		if err := h.setTag(ctx); err != nil {
			return err
		}
	}
	return h.Tmux.SetTag(ctx, session, name, value)
}

func (h *faultyHost) Status(ctx context.Context, session string) (*sessionhost.Status, error) {
	if h.status != nil {
		if err := h.status(ctx); err != nil {
			return nil, err
		}
	}
	return h.Tmux.Status(ctx, session)
}

func (h *faultyHost) Kill(ctx context.Context, session string) error {
	if h.kill != nil {
		if err := h.kill(ctx); err != nil {
			return err
		}
	}
	return h.Tmux.Kill(ctx, session)
}

func (h *faultyHost) Capture(ctx context.Context, session string) (string, error) {
	real := func() (string, error) { return h.Tmux.Capture(ctx, session) }
	if h.capture != nil {
		return h.capture(ctx, real)
	}
	return real()
}

// records hands every log record to a test without ever blocking the kit.
type records struct{ ch chan slog.Record }

func newRecords() *records { return &records{ch: make(chan slog.Record, 64)} }

func (l *records) Enabled(context.Context, slog.Level) bool { return true }
func (l *records) WithAttrs([]slog.Attr) slog.Handler       { return l }
func (l *records) WithGroup(string) slog.Handler            { return l }
func (l *records) Handle(_ context.Context, r slog.Record) error {
	select {
	case l.ch <- r:
	default:
	}
	return nil
}

// await is the err attribute of the next record logged with msg.
func (l *records) await(ctx context.Context, t *testing.T, msg string) string {
	t.Helper()
	for {
		select {
		case r := <-l.ch:
			if r.Message != msg {
				continue
			}
			var got string
			r.Attrs(func(a slog.Attr) bool {
				if a.Key == "err" {
					got = a.Value.String()
				}
				return true
			})
			return got
		case <-ctx.Done():
			require.FailNow(t, "timed out", "waiting for the log %q", msg)
		}
	}
}

type markedKey struct{}

// marked is a context only the test's own calls carry, to tell them from
// the kit's background ones.
func marked(ctx context.Context) context.Context { return context.WithValue(ctx, markedKey{}, true) }

func isMarked(ctx context.Context) bool { return ctx.Value(markedKey{}) != nil }

func TestAFailedTagOfTheConversationIDIsLoggedNotFatal(t *testing.T) {
	t.Parallel()
	r := start(t)
	ref := r.conv.Ref()
	require.NoError(t, r.conv.Stop(r.ctx, false))
	next(r.ctx, t, r.conv, harness.Exited)

	logs := newRecords()
	r.Harness.Host = &faultyHost{Tmux: r.Host, setTag: func(context.Context) error { return errors.New("tmux set-option failed") }}
	r.Harness.Logger = slog.New(logs)
	// The resumed id is tagged at launch.
	resumed := r.launch(t, &ref)
	assert.Equal(t, "tmux set-option failed", logs.await(r.ctx, t, "tuidriver: recording the conversation id"))
	assert.Equal(t, ref, resumed.Ref(), "the conversation carries on")
	tag, err := r.Host.Tag(r.ctx, fake.SessionName("a1"), tuidriver.ConversationTag)
	require.NoError(t, err)
	assert.Empty(t, tag, "nothing was recorded")
}

func TestAFailedScreenReadChangesNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	var failing atomic.Bool
	read := make(chan struct{}, 8)
	r.Harness.Host = &faultyHost{Tmux: r.Host, capture: func(ctx context.Context, real func() (string, error)) (string, error) {
		if failing.Load() && !isMarked(ctx) {
			read <- struct{}{}
			return "", errors.New("tmux capture-pane failed")
		}
		return real()
	}}
	r.conv = r.launch(t, nil)
	r.do(t, fake.Command{Cmd: fake.CmdQuietCancel, On: true})
	r.inTurn(t)
	interrupted := make(chan error, 1)
	go func() { interrupted <- r.conv.Interrupt(r.ctx) }()
	r.await(t, fake.RepInterrupt)
	r.idleOnScreen(t)

	failing.Store(true)
	// The second failed read returns only once the first has been handled.
	for range 2 {
		r.clk.Advance(poll)
		<-read
	}
	select {
	case ev := <-r.conv.Events():
		require.Failf(t, "an event off a failed read", "%+v", ev)
	default:
	}
	failing.Store(false)
	r.clk.Advance(poll)
	assert.Equal(t, harness.TurnInterrupted, next(r.ctx, t, r.conv, harness.TurnEnded).Outcome)
	require.NoError(t, <-interrupted)
}

// A hook is the better witness than the screen for a while after it: a
// screen that shows the turn over, read just after the turn's hook, ends
// nothing until the hook's trust has run out.
func TestTheScreenIsNotTrustedJustAfterAHook(t *testing.T) {
	t.Parallel()
	const trust = 10 * poll
	r := newRigTrusting(t, trust)
	read := make(chan struct{}, 8)
	r.Harness.Host = &faultyHost{Tmux: r.Host, capture: func(ctx context.Context, real func() (string, error)) (string, error) {
		if !isMarked(ctx) {
			read <- struct{}{}
		}
		return real()
	}}
	r.conv = r.launch(t, nil)
	r.do(t, fake.Command{Cmd: fake.CmdQuietCancel, On: true})
	r.inTurn(t)
	interrupted := make(chan error, 1)
	go func() { interrupted <- r.conv.Interrupt(r.ctx) }()
	r.await(t, fake.RepInterrupt)
	r.idleOnScreen(t)
	for len(read) > 0 {
		<-read
	}

	// The second read starts only once the first has been handled.
	for range 2 {
		r.clk.Advance(poll)
		<-read
	}
	select {
	case ev := <-r.conv.Events():
		require.Failf(t, "an event off a screen within the hook's trust", "%+v", ev)
	default:
	}
	r.clk.Advance(trust)
	assert.Equal(t, harness.TurnInterrupted, next(r.ctx, t, r.conv, harness.TurnEnded).Outcome)
	require.NoError(t, <-interrupted)
}

// A hook that lands after the conversation let go has nobody to tell: it is
// answered, and its events dropped rather than sent on a closed channel.
func TestAHookAfterCloseIsAnsweredAndItsEventsDropped(t *testing.T) {
	t.Parallel()
	r := start(t)
	require.NoError(t, r.conv.Close())
	payload, err := json.Marshal(&fake.HookPayload{})
	require.NoError(t, err)

	reply, err := r.conv.Hook(r.ctx, fake.HookTurnStart, payload)

	require.NoError(t, err)
	assert.NotEmpty(t, reply)
	_, open := <-r.conv.Events()
	assert.False(t, open, "closed, and nothing was sent on it")
}

func TestAMessageIsNotTypedWhenTheScreenCannotBeReadFirst(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	captureErr := errors.New("tmux capture-pane failed")
	r.Harness.Host = &faultyHost{Tmux: r.Host, capture: func(ctx context.Context, real func() (string, error)) (string, error) {
		if isMarked(ctx) {
			return "", captureErr
		}
		return real()
	}}
	r.conv = r.launch(t, nil)

	assert.ErrorIs(t, r.conv.Send(marked(r.ctx), msg("lost")), captureErr)
	require.NoError(t, r.conv.Send(r.ctx, msg("after")))
	// Nothing was pasted for the failed send: the first paste is the later one.
	assert.Equal(t, pointer("after"), r.await(t, fake.RepPaste).Text)
}

func TestSendFailsWhenTheSessionStatusCannotBeRead(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	statusErr := errors.New("tmux display-message failed")
	r.Harness.Host = &faultyHost{Tmux: r.Host, status: func(ctx context.Context) error {
		if isMarked(ctx) {
			return statusErr
		}
		return nil
	}}
	r.conv = r.launch(t, nil)

	assert.ErrorIs(t, r.conv.Send(marked(r.ctx), msg("lost")), statusErr)
	require.NoError(t, r.conv.Send(r.ctx, msg("after")))
	// Nothing was typed for the failed send: the first paste is the later one.
	assert.Equal(t, pointer("after"), r.await(t, fake.RepPaste).Text)
}

func TestAPasteIsAwaitedUntilItShows(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	var looks atomic.Int32
	r.Harness.Host = &faultyHost{Tmux: r.Host, capture: func(ctx context.Context, real func() (string, error)) (string, error) {
		// The look before the paste, then one that doesn't show it yet.
		if isMarked(ctx) && looks.Add(1) == 2 {
			return "", nil
		}
		return real()
	}}
	r.conv = r.launch(t, nil)

	require.NoError(t, r.conv.Send(marked(r.ctx), msg("hello")))
	assert.GreaterOrEqual(t, looks.Load(), int32(3), "looked again after the paste didn't show")
	assert.Equal(t, pointer("hello"), r.await(t, fake.RepUser).Text)
}

func TestADeadSessionThatCannotBeRemovedStillReportsItsExit(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	logs := newRecords()
	var failed atomic.Bool
	r.Harness.Logger = slog.New(logs)
	r.Harness.Host = &faultyHost{Tmux: r.Host, kill: func(context.Context) error {
		if failed.CompareAndSwap(false, true) {
			return errors.New("tmux kill-session failed")
		}
		return nil
	}}
	r.conv = r.launch(t, nil)

	r.do(t, fake.Command{Cmd: fake.CmdExit, Code: 3})
	assert.Equal(t, "tmux kill-session failed", logs.await(r.ctx, t, "tuidriver: removing the dead session"))
	ev := next(r.ctx, t, r.conv, harness.Exited)
	assert.Equal(t, 3, ev.ExitCode)
	assert.ErrorContains(t, ev.Err, "exited with status 3")
}

func TestAStopForcedWhileClosingReportsTheClose(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	entered, release := make(chan struct{}), make(chan struct{})
	r.Harness.Host = &faultyHost{Tmux: r.Host, kill: func(context.Context) error {
		close(entered)
		<-release
		return nil
	}}
	r.conv = r.launch(t, nil)

	stopped := make(chan error, 1)
	go func() { stopped <- r.conv.Stop(r.ctx, true) }()
	<-entered
	require.NoError(t, r.conv.Close())
	close(release)
	// The kill went through, but the exit it waits for can't be reported
	// to a conversation that has let go.
	assert.ErrorIs(t, <-stopped, tuidriver.ErrClosed)
}

func TestAWaitEndedByCloseOrExit(t *testing.T) {
	t.Parallel()
	r := start(t)
	assert.ErrorIs(t, r.conv.Ended(), tuidriver.ErrClosed)
	assert.ErrorIs(t, tuidriver.Stopped(tuidriver.ErrClosed), tuidriver.ErrClosed)

	require.NoError(t, r.conv.Stop(r.ctx, false))
	<-r.conv.Exited()
	assert.ErrorIs(t, r.conv.Ended(), tuidriver.ErrExited)
	assert.NoError(t, tuidriver.Stopped(r.conv.Ended()), "an exit is what Stop wanted")
}
