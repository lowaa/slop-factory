package agents_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
)

// The limits are the guard's contract, so the tests pin them exactly: one
// off either way and an agent is cut short, or two ping-pong for longer.
const (
	pairLimit  = 20
	sendLimit  = 30
	sendWindow = time.Minute
)

func TestTheLoopGuardRefusesTheMessageAfterThePairLimit(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	_, _, from := f.startedAgent("sender", nil)
	_, to, _ := f.startedAgent("receiver", nil)
	ctx := f.ctx()
	for i := range pairLimit {
		_, err := f.mgr.Tell(ctx, from, string(to.ID), "ping", false)
		require.NoError(t, err, "message %d is within the limit", i+1)
	}
	_, err := f.mgr.Tell(ctx, from, string(to.ID), "ping", false)
	require.ErrorIs(t, err, agents.ErrLoopGuard)
}

func TestTheLoopGuardCountsASenderOnlyWithinItsSendWindow(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	_, _, from := f.startedAgent("sender", nil)
	ctx := f.ctx()
	tell := func() error {
		_, err := f.mgr.Tell(ctx, from, "user", "fyi", false)
		return err
	}
	for i := range sendLimit {
		require.NoError(t, tell(), "message %d is within the limit", i+1)
	}
	require.ErrorIs(t, tell(), agents.ErrLoopGuard)

	// A message exactly a window old still counts; a moment later it
	// doesn't.
	f.clock.Advance(sendWindow)
	require.ErrorIs(t, tell(), agents.ErrLoopGuard, "the window is still open")
	f.clock.Advance(time.Millisecond)
	require.NoError(t, tell(), "the sends have aged out")
}
