package tuidriver_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake/faketest"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost/tmuxtest"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/tuidriver"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
)

func TestMain(m *testing.M) { faketest.Main(m) }

const (
	poll    = time.Second
	confirm = 10 * time.Second
)

var t0 = time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC)

type rig struct {
	*faketest.TUI
	clk  *clock.Fake
	ctx  context.Context
	conv *tuidriver.Conversation
	fake *fake.Session
}

// start runs fake/tui on a fake clock, so the screen poll and the confirm
// timeouts move only when the test advances them.
func start(t *testing.T) *rig {
	t.Helper()
	r := newRig(t)
	r.conv = r.launch(t, nil)
	return r
}

// newRig is start without the launch, for a test that changes the rig
// first.
func newRig(t *testing.T) *rig {
	t.Helper()
	clk := clock.NewFake(t0)
	r := &rig{TUI: faketest.NewTUI(t, &faketest.TUIOptions{Clock: clk, PollInterval: poll, ConfirmTimeout: confirm}), clk: clk}
	ctx, cancel := context.WithTimeout(context.Background(), faketest.Timeout)
	t.Cleanup(cancel)
	r.ctx = ctx
	return r
}

func (r *rig) launch(t *testing.T, resume *harness.ConversationRef) *tuidriver.Conversation {
	t.Helper()
	conv, err := r.Harness.Start(r.ctx, faketest.Workspace(t, "a1"), resume)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conv.Stop(context.Background(), true) })
	_, _, err = r.Puppet.Started(r.ctx)
	require.NoError(t, err)
	r.fake = r.Puppet.Session()
	tc := conv.(*tuidriver.Conversation)
	next(r.ctx, t, tc, harness.Ready)
	return tc
}

func (r *rig) do(t *testing.T, c fake.Command) {
	t.Helper()
	_, err := r.fake.Do(r.ctx, c)
	require.NoError(t, err)
}

func (r *rig) await(t *testing.T, rep string) *fake.Report {
	t.Helper()
	got, err := r.fake.Await(r.ctx, rep)
	require.NoError(t, err)
	return got
}

// idleOnScreen waits for the fake's screen to show it idle. The screen
// changes with no event to wait for, so this waits on the tmux state
// itself, bounded.
func (r *rig) idleOnScreen(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		s, err := r.Host.Capture(r.ctx, fake.SessionName("a1"))
		return err == nil && strings.Contains(s, "[fake] idle")
	}, faketest.Timeout, 5*time.Millisecond)
}

// inTurn starts a turn, as the harness would on a prompt.
func (r *rig) inTurn(t *testing.T) {
	t.Helper()
	r.do(t, fake.Command{Cmd: fake.CmdTurnStart})
	next(r.ctx, t, r.conv, harness.TurnStarted)
}

func next(ctx context.Context, t *testing.T, conv harness.Conversation, kind harness.EventKind) harness.Event {
	t.Helper()
	for {
		select {
		case ev, ok := <-conv.Events():
			require.True(t, ok, "events closed waiting for %s", kind)
			if ev.Kind == kind {
				return ev
			}
		case <-ctx.Done():
			require.FailNow(t, "timed out", "waiting for %s", kind)
		}
	}
}

func msg(text string) *harness.Message {
	return &harness.Message{From: ident.UserPrincipal(), Text: text}
}

func pointer(texts ...string) string {
	msgs := make([]*harness.Message, len(texts))
	for i, s := range texts {
		msgs[i] = msg(s)
	}
	return faketest.Pointer(msgs)
}

func TestAPasteIsSubmittedOnceAndConfirmed(t *testing.T) {
	t.Parallel()
	r := start(t)
	// Two lines: a paste that weren't bracketed would submit at the first.
	require.NoError(t, r.conv.Send(r.ctx, msg("first line\nsecond line")))
	want := pointer("first line\nsecond line")
	assert.Equal(t, want, r.await(t, fake.RepPaste).Text)
	key := r.await(t, fake.RepKey)
	assert.False(t, key.Dropped)
	user := r.await(t, fake.RepUser)
	assert.Equal(t, &fake.Report{Rep: fake.RepUser, Text: want, Via: fake.ViaKeys}, user)
}

func TestASwallowedSubmitIsRetried(t *testing.T) {
	t.Parallel()
	r := start(t)
	r.do(t, fake.Command{Cmd: fake.CmdDropSubmits, Count: 1})
	sent := make(chan error, 1)
	go func() { sent <- r.conv.Send(r.ctx, msg("hello")) }()

	assert.True(t, r.await(t, fake.RepKey).Dropped)
	// The screen ticker, and the confirm timer the swallowed Enter started.
	r.clk.BlockUntil(2)
	select {
	case err := <-sent:
		require.Failf(t, "Send returned", "before the confirm timeout, with %v", err)
	default:
	}
	r.clk.Advance(confirm)
	assert.False(t, r.await(t, fake.RepKey).Dropped, "the retry")
	assert.Equal(t, pointer("hello"), r.await(t, fake.RepUser).Text)
	require.NoError(t, <-sent)
}

func TestASubmitNeverConfirmedFails(t *testing.T) {
	t.Parallel()
	r := start(t)
	r.do(t, fake.Command{Cmd: fake.CmdDropSubmits, Count: 2})
	sent := make(chan error, 1)
	go func() { sent <- r.conv.Send(r.ctx, msg("hello")) }()
	for range 2 {
		assert.True(t, r.await(t, fake.RepKey).Dropped)
		r.clk.BlockUntil(2)
		r.clk.Advance(confirm)
	}
	assert.ErrorIs(t, <-sent, tuidriver.ErrUnconfirmed)
}

func TestNothingIsTypedWhileAPersonIsAttached(t *testing.T) {
	t.Parallel()
	r := start(t)
	detach := tmuxtest.Attach(r.ctx, t, r.Host, fake.SessionName("a1"))
	assert.ErrorIs(t, r.conv.Send(r.ctx, msg("held")), tuidriver.ErrAttached)

	// Hook delivery carries on while attached: a mid-turn message still
	// reaches the model.
	r.inTurn(t)
	require.NoError(t, r.conv.Send(r.ctx, msg("mid-turn")))
	r.do(t, fake.Command{Cmd: fake.CmdStep})
	assert.Equal(t, &fake.Report{Rep: fake.RepUser, Text: "mid-turn", From: "user", MidTurn: true, Via: fake.ViaHook}, r.await(t, fake.RepUser))
	r.do(t, fake.Command{Cmd: fake.CmdTurnEnd})
	next(r.ctx, t, r.conv, harness.TurnEnded)

	detach()
	require.NoError(t, r.conv.Send(r.ctx, msg("after")))
	// The first paste the fake ever saw is the one sent after detaching.
	assert.Equal(t, pointer("after"), r.await(t, fake.RepPaste).Text)
}

func TestTheScreenReportsACancelNoHookDoes(t *testing.T) {
	t.Parallel()
	r := start(t)
	r.do(t, fake.Command{Cmd: fake.CmdQuietCancel, On: true})
	r.inTurn(t)
	interrupted := make(chan error, 1)
	go func() { interrupted <- r.conv.Interrupt(r.ctx) }()
	r.await(t, fake.RepInterrupt)
	// No hook said so, and the poll hasn't run: the turn is still on.
	select {
	case ev := <-r.conv.Events():
		require.Failf(t, "event before the poll", "%+v", ev)
	default:
	}
	r.idleOnScreen(t)
	r.clk.Advance(poll)
	assert.Equal(t, harness.TurnInterrupted, next(r.ctx, t, r.conv, harness.TurnEnded).Outcome)
	require.NoError(t, <-interrupted)
}

func TestAMessageHeldMidTurnKeepsTheTurnGoing(t *testing.T) {
	t.Parallel()
	r := start(t)
	r.inTurn(t)
	require.NoError(t, r.conv.Send(r.ctx, msg("one more thing")))
	// No step hook ran, so it goes out at the turn's end, keeping it going.
	r.do(t, fake.Command{Cmd: fake.CmdTurnEnd})
	assert.Equal(t, &fake.Report{Rep: fake.RepUser, Text: "one more thing", From: "user", MidTurn: true, Via: fake.ViaHook}, r.await(t, fake.RepUser))
	r.do(t, fake.Command{Cmd: fake.CmdText, Text: "done that"})
	assert.Equal(t, "done that", next(r.ctx, t, r.conv, harness.Text).Text, "no TurnEnded came before")
	r.do(t, fake.Command{Cmd: fake.CmdTurnEnd})
	next(r.ctx, t, r.conv, harness.TurnEnded)
}

func TestAMessageHeldThroughACancelIsTypedOnceIdle(t *testing.T) {
	t.Parallel()
	r := start(t)
	r.inTurn(t)
	require.NoError(t, r.conv.Send(r.ctx, msg("after the cancel")))
	// A cancelled turn can't be kept going, so the message waits for idle.
	require.NoError(t, r.conv.Interrupt(r.ctx))
	assert.Equal(t, harness.TurnInterrupted, next(r.ctx, t, r.conv, harness.TurnEnded).Outcome)
	assert.Equal(t, &fake.Report{Rep: fake.RepUser, Text: pointer("after the cancel"), Via: fake.ViaKeys}, r.await(t, fake.RepUser))
}

func TestAdoptAfterADaemonRestart(t *testing.T) {
	t.Parallel()
	r := start(t)
	ref := r.conv.Ref()
	r.inTurn(t)
	// The daemon goes away; the session doesn't.
	require.NoError(t, r.conv.Close())
	_, open := <-r.conv.Events()
	assert.False(t, open)

	// A new daemon: a fresh kit over the same tmux server.
	restarted := *r.Harness
	adopted, err := restarted.AdoptAgent(r.ctx, "a1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = adopted.Stop(context.Background(), true) })
	assert.Equal(t, ref, adopted.Ref(), "the conversation id comes back from the session")
	next(r.ctx, t, adopted, harness.Ready)
	next(r.ctx, t, adopted, harness.TurnStarted)

	// Hooks reach the new kit.
	r.do(t, fake.Command{Cmd: fake.CmdTurnEnd})
	next(r.ctx, t, adopted, harness.TurnEnded)
	require.NoError(t, adopted.Send(r.ctx, msg("still there?")))
	assert.Equal(t, pointer("still there?"), r.await(t, fake.RepUser).Text)
	// The turn that prompt opens has to run before Stop can quit.
	r.do(t, fake.Command{Cmd: fake.CmdTurnStart})
	r.do(t, fake.Command{Cmd: fake.CmdTurnEnd})
	next(r.ctx, t, adopted, harness.TurnEnded)

	require.NoError(t, adopted.Stop(r.ctx, false))
	exited := next(r.ctx, t, adopted, harness.Exited)
	assert.Equal(t, 0, exited.ExitCode)
}

func TestAttachPlanJoinsTheSession(t *testing.T) {
	t.Parallel()
	r := start(t)
	plan, err := r.conv.AttachPlan(r.ctx)
	require.NoError(t, err)
	assert.Equal(t, harness.Join, plan.Kind)
	argv := r.Host.AttachCommand(fake.SessionName("a1"))
	assert.Equal(t, argv[0], plan.Command.Path)
	assert.Equal(t, argv[1:], plan.Command.Args)
	assert.Contains(t, plan.Command.Args, "attach-session")
}
