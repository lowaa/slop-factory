// Package conformance is the suite every harness implementation runs, the
// fake and each real harness alike, so the core can rely on Conversation
// behaving the same whichever harness and driver is behind it. An
// implementation that passes is a drop-in replacement for another.
package conformance

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
)

// Puppet makes the harness under test do things: the fake through its
// controller, a real harness through the scripted model. Each method arranges
// what the harness does in response to the next message it is sent.
type Puppet interface {
	// Reply makes the next turn answer with text and complete.
	Reply(ctx context.Context, text string) error
	// LongTool makes the next turn start a tool call that runs until release
	// is called. A message sent meanwhile is mid-turn.
	LongTool(ctx context.Context) (release func(context.Context) error, err error)
	// Received checks that the harness received text in the current
	// session, mid-turn or not.
	Received(ctx context.Context, text string, midTurn bool) error
	// Interrupted checks that the harness saw an interrupt.
	Interrupted(ctx context.Context) error
	// Crash makes the harness process die with a non-zero status.
	Crash(ctx context.Context) error
	// Started waits for a session to start, reporting whether it resumed id.
	Started(ctx context.Context) (id string, resumed bool, err error)
}

// Subject is one harness implementation under test.
type Subject struct {
	Harness harness.AIHarness
	// Workspace returns a fresh workspace for one conversation.
	Workspace func(t *testing.T) *harness.Workspace
	Puppet    Puppet
	// Timeout bounds each step: a safety net, never a synchronisation.
	Timeout time.Duration
}

// Run runs the suite. new makes a fresh Subject per test, so tests can run
// in parallel.
func Run(t *testing.T, new func(t *testing.T) *Subject) {
	tests := map[string]func(*testing.T, *Subject){
		"starts ready, with an id":                      testStarts,
		"an idle message runs a turn":                   testIdleTurn,
		"a mid-turn message reaches the running turn":   testMidTurn,
		"interrupt ends the turn and keeps the session": testInterrupt,
		"stop while idle exits cleanly":                 testStopIdle,
		"stop waits for the running turn":               testStopWaits,
		"forced stop ends a running turn":               testStopForce,
		"resume continues the same conversation":        testResume,
		"a crash is reported with its status":           testCrash,
	}
	for name, fn := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fn(t, new(t))
		})
	}
}

func ctxFor(t *testing.T, s *Subject) context.Context {
	t.Helper()
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(cancel)
	return ctx
}

func start(ctx context.Context, t *testing.T, s *Subject, resume *harness.ConversationRef) harness.Conversation {
	t.Helper()
	conv, err := s.Harness.Start(ctx, s.Workspace(t), resume)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conv.Stop(context.Background(), true) })
	_, _, err = s.Puppet.Started(ctx)
	require.NoError(t, err)
	next(ctx, t, conv, harness.Ready)
	return conv
}

// next returns the next event of kind, skipping others (text, usage, tools)
// a real harness emits in between.
func next(ctx context.Context, t *testing.T, conv harness.Conversation, kind harness.EventKind) harness.Event {
	t.Helper()
	for {
		select {
		case ev, ok := <-conv.Events():
			require.True(t, ok, "events closed waiting for %s", kind)
			if ev.Kind == kind {
				return ev
			}
			if ev.Kind == harness.Exited && kind != harness.Exited {
				require.Failf(t, "exited", "waiting for %s, the harness exited: %v", kind, ev.Err)
			}
		case <-ctx.Done():
			require.FailNow(t, "timed out", "waiting for %s", kind)
		}
	}
}

func send(ctx context.Context, t *testing.T, conv harness.Conversation, text string) {
	t.Helper()
	require.NoError(t, conv.Send(ctx, &harness.Message{Text: text}))
}

// inLongTool starts a conversation and leaves it in a turn, inside a tool
// call that runs until release is called.
func inLongTool(ctx context.Context, t *testing.T, s *Subject) (harness.Conversation, func(context.Context) error) {
	t.Helper()
	conv := start(ctx, t, s, nil)
	release, err := s.Puppet.LongTool(ctx)
	require.NoError(t, err)
	send(ctx, t, conv, "do the long thing")
	next(ctx, t, conv, harness.ToolStarted)
	return conv, release
}

func testStarts(t *testing.T, s *Subject) {
	conv := start(ctxFor(t, s), t, s, nil)
	ref := conv.Ref()
	assert.NotEmpty(t, ref.ID)
	assert.Equal(t, s.Harness.Name(), ref.Harness)
	assert.Equal(t, s.Harness.Driver(), ref.Driver)
}

func testIdleTurn(t *testing.T, s *Subject) {
	ctx := ctxFor(t, s)
	conv := start(ctx, t, s, nil)
	require.NoError(t, s.Puppet.Reply(ctx, "hello back"))
	send(ctx, t, conv, "hello")
	next(ctx, t, conv, harness.TurnStarted)
	ended := next(ctx, t, conv, harness.TurnEnded)
	assert.Equal(t, harness.TurnCompleted, ended.Outcome)
	require.NoError(t, s.Puppet.Received(ctx, "hello", false))
}

func testMidTurn(t *testing.T, s *Subject) {
	ctx := ctxFor(t, s)
	conv, release := inLongTool(ctx, t, s)
	send(ctx, t, conv, "main is broken, stop merging")
	require.NoError(t, s.Puppet.Received(ctx, "main is broken, stop merging", true))
	require.NoError(t, release(ctx))
	next(ctx, t, conv, harness.TurnEnded)
}

func testInterrupt(t *testing.T, s *Subject) {
	ctx := ctxFor(t, s)
	conv, _ := inLongTool(ctx, t, s)
	require.NoError(t, conv.Interrupt(ctx))
	ended := next(ctx, t, conv, harness.TurnEnded)
	assert.Equal(t, harness.TurnInterrupted, ended.Outcome)
	require.NoError(t, s.Puppet.Interrupted(ctx))

	// Still usable.
	require.NoError(t, s.Puppet.Reply(ctx, "ok"))
	send(ctx, t, conv, "carry on")
	assert.Equal(t, harness.TurnCompleted, next(ctx, t, conv, harness.TurnEnded).Outcome)
}

func testStopIdle(t *testing.T, s *Subject) {
	ctx := ctxFor(t, s)
	conv := start(ctx, t, s, nil)
	require.NoError(t, conv.Stop(ctx, false))
	exited := next(ctx, t, conv, harness.Exited)
	assert.Equal(t, 0, exited.ExitCode)
	assert.NoError(t, exited.Err)
	_, open := <-conv.Events()
	assert.False(t, open, "Events closes after Exited")
}

func testStopWaits(t *testing.T, s *Subject) {
	ctx := ctxFor(t, s)
	conv, release := inLongTool(ctx, t, s)

	stopped := make(chan error, 1)
	go func() { stopped <- conv.Stop(ctx, false) }()
	select {
	case err := <-stopped:
		require.Failf(t, "stop didn't wait", "returned %v while the turn ran", err)
	default:
	}
	require.NoError(t, release(ctx))
	next(ctx, t, conv, harness.TurnEnded)
	require.NoError(t, <-stopped)
	next(ctx, t, conv, harness.Exited)
}

func testStopForce(t *testing.T, s *Subject) {
	ctx := ctxFor(t, s)
	conv, _ := inLongTool(ctx, t, s)
	require.NoError(t, conv.Stop(ctx, true))
	next(ctx, t, conv, harness.Exited)
}

func testResume(t *testing.T, s *Subject) {
	ctx := ctxFor(t, s)
	conv := start(ctx, t, s, nil)
	ref := conv.Ref()
	require.NoError(t, conv.Stop(ctx, false))
	next(ctx, t, conv, harness.Exited)

	resumed, err := s.Harness.Start(ctx, s.Workspace(t), &ref)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resumed.Stop(context.Background(), true) })
	id, wasResumed, err := s.Puppet.Started(ctx)
	require.NoError(t, err)
	assert.True(t, wasResumed)
	assert.Equal(t, ref.ID, id)
	next(ctx, t, resumed, harness.Ready)
	assert.Equal(t, ref.ID, resumed.Ref().ID)
}

func testCrash(t *testing.T, s *Subject) {
	ctx := ctxFor(t, s)
	conv := start(ctx, t, s, nil)
	require.NoError(t, s.Puppet.Crash(ctx))
	exited := next(ctx, t, conv, harness.Exited)
	assert.NotEqual(t, 0, exited.ExitCode)
	assert.Error(t, exited.Err)
}
