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
	adopted, err := r.Harness.AdoptAgent(r.ctx, "a1", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = adopted.Stop(context.Background(), true) })
	assert.Equal(t, ref, adopted.Ref())
}

// A session whose id tag is missing (the tag write failed, and it is only
// retried on a hook) is adopted with the id the caller has on record, and
// its tag repaired; with no id from either, adoption fails rather than
// hand back a conversation with none, which the caller would store over
// the real one.
func TestAnUntaggedSessionIsAdoptedWithTheKnownID(t *testing.T) {
	t.Parallel()
	r := start(t)
	ref := r.conv.Ref()
	require.NotEmpty(t, ref.ID)
	require.NoError(t, r.conv.Close())
	session := fake.SessionName("a1")
	// The tuidriver's tag for the conversation id.
	const tag = "slop-bot-conversation"
	require.NoError(t, r.Host.SetTag(r.ctx, session, tag, ""))

	_, err := r.Harness.AdoptAgent(r.ctx, "a1", "")
	require.Error(t, err, "no id from the session or the caller")

	adopted, err := r.Harness.AdoptAgent(r.ctx, "a1", ref.ID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = adopted.Stop(context.Background(), true) })
	assert.Equal(t, ref, adopted.Ref())
	got, err := r.Host.Tag(r.ctx, session, tag)
	require.NoError(t, err)
	assert.Equal(t, ref.ID, got, "the tag is written again")
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
	failTag   atomic.Bool
	// holdTag, when set, holds the next tag write: it closes entered, then
	// waits for release.
	holdTag atomic.Pointer[heldTag]
}

type heldTag struct{ entered, release chan struct{} }

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

func (h *flakyHost) SetTag(ctx context.Context, session, name, value string) error {
	if held := h.holdTag.Swap(nil); held != nil {
		close(held.entered)
		<-held.release
	}
	if h.failTag.Load() {
		return errors.New("fork/exec tmux: resource temporarily unavailable")
	}
	return h.Tmux.SetTag(ctx, session, name, value)
}

// capturingHub is the rig's hub, keeping the handler a session is routed
// to, so a test can deliver a hook as one in flight would: after Close.
type capturingHub struct {
	tuidriver.HookHub
	handler atomic.Pointer[tuidriver.HookHandler]
}

func (h *capturingHub) Route(session string, handler tuidriver.HookHandler) (func(), error) {
	h.handler.Store(&handler)
	return h.HookHub.Route(session, handler)
}

// Close lets go of the session: a hook still in flight as it runs (one
// retrying a tag write that failed, say) writes nothing to the session
// afterwards. Here the late hook would retag a session a test, or the
// next daemon, had already read the tag of.
func TestNoHookWritesToTheSessionAfterClose(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	host := &flakyHost{Tmux: r.Host, failed: make(chan struct{}, 1)}
	host.failTag.Store(true)
	hub := &capturingHub{HookHub: r.Harness.Hub}
	r.Harness.Host, r.Harness.Hub = host, hub
	r.conv = r.launch(t, nil)
	id := r.conv.Ref().ID
	require.NotEmpty(t, id)
	require.NoError(t, r.conv.Close())
	host.failTag.Store(false)

	payload, err := json.Marshal(&fake.HookPayload{ConversationID: id, Text: "late"})
	require.NoError(t, err)
	_, _ = (*hub.handler.Load())(r.ctx, fake.HookText, payload)

	got, err := r.Host.Tag(r.ctx, fake.SessionName("a1"), "slop-bot-conversation")
	require.NoError(t, err)
	assert.Empty(t, got, "nothing is written to a session Close let go of")
}

// A hook that has taken held messages for its reply and is still
// answering when the session dies may never get them to the harness: the
// exit puts them back, Undelivered, for the next session.
func TestMessagesAHookWasCarryingAtTheExitAreUndelivered(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	host := &flakyHost{Tmux: r.Host, failed: make(chan struct{}, 1)}
	// Every tag write fails, so every hook retries it: where this one is
	// held, after it has taken the message.
	host.failTag.Store(true)
	hub := &capturingHub{HookHub: r.Harness.Hub}
	r.Harness.Host, r.Harness.Hub = host, hub
	r.conv = r.launch(t, nil)
	r.inTurn(t)
	require.NoError(t, r.conv.Send(r.ctx, msg("carried")))

	held := &heldTag{entered: make(chan struct{}), release: make(chan struct{})}
	host.holdTag.Store(held)
	payload, err := json.Marshal(&fake.HookPayload{ConversationID: r.conv.Ref().ID})
	require.NoError(t, err)
	answered := make(chan struct{})
	go func() {
		defer close(answered)
		_, _ = (*hub.handler.Load())(r.ctx, fake.HookStep, payload)
	}()
	<-held.entered
	require.NoError(t, r.Host.Kill(r.ctx, fake.SessionName("a1")))
	next(r.ctx, t, r.conv, harness.Exited)

	var texts []string
	for _, m := range r.conv.Undelivered() {
		texts = append(texts, m.Text)
	}
	assert.Equal(t, []string{"carried"}, texts)
	close(held.release)
	<-answered
	assert.Len(t, r.conv.Undelivered(), 1, "the late answer doesn't take it back")
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
