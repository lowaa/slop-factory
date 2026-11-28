package codex

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/codex/protocol"
)

// A message Codex's codec queued was accepted by its Send: the caller has
// it marked delivered. When the codec later sends it on and that write
// fails (the pipe is gone), the message stays the codec's, Undelivered,
// in each of the paths that send a queued message on.

func TestAQueuedMessageWhoseTurnStartFailsStaysUndelivered(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	f.open()
	f.send("sleep")
	pl := newPlayer(t, f.p, "turn_interrupted")
	pl.decode(f.c, f.w, itemStarted(protocol.ThreadItemTypeCommandExecution))
	require.NoError(t, f.c.Interrupt(context.Background(), f.w))
	queued := &harness.Message{Text: "do this instead", IDs: []int64{3}}
	require.NoError(t, f.c.Send(context.Background(), f.w, queued, false))

	f.p.refuse = protocol.MethodTurnStart
	for len(pl.lines) > 0 {
		line := pl.take()
		_, _ = f.c.Decode(line, f.w)
		if method(protocol.MethodTurnCompleted)(line) {
			break
		}
	}

	assert.Equal(t, []*harness.Message{queued}, f.c.Undelivered(), "turnCompleted's turn/start failed")
}

func TestAMessageWaitingForATurnStartWhoseSteerFailsStaysUndelivered(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	f.open()
	f.send("first")
	waiting := &harness.Message{Text: "second", IDs: []int64{4}}
	require.NoError(t, f.c.Send(context.Background(), f.w, waiting, false))
	require.Equal(t, []*harness.Message{waiting}, f.c.Undelivered(), "no turn id to steer by yet")

	f.p.broken = true
	// turn/start's response: flush steers what waited for it.
	pl := newPlayer(t, f.p, "turn_completed")
	for len(pl.lines) > 0 && !method(protocol.MethodTurnStarted)(pl.lines[0]) {
		_, _ = f.c.Decode(pl.take(), f.w)
	}

	assert.Equal(t, []*harness.Message{waiting}, f.c.Undelivered(), "flush's steer failed")
}

func TestARefusedSteerWhoseTurnStartFailsStaysUndelivered(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	f.open()
	f.send("first")
	pl := newPlayer(t, f.p, "turn_completed")
	pl.decode(f.c, f.w, method(protocol.MethodTurnStarted))
	steered := &harness.Message{Text: "second", IDs: []int64{5}}
	require.NoError(t, f.c.Send(context.Background(), f.w, steered, false))
	f.p.last(protocol.MethodTurnSteer, nil)
	pl.decode(f.c, f.w, nil) // the turn completes before the refusal comes

	f.p.broken = true
	refusal := newPlayer(t, f.p, "steer_refused")
	for len(refusal.lines) > 0 {
		_, _ = f.c.Decode(refusal.take(), f.w)
	}

	assert.Equal(t, []*harness.Message{steered}, f.c.Undelivered(), "steerRefused's turn/start failed")
}
