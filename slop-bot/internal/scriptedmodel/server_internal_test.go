package scriptedmodel

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newInternalServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// gatedAnswer plays the first turn of a one-turn gated scenario, as a
// request would, and returns what it waits with.
func gatedAnswer(t *testing.T, s *Server) (*gate, *Turn, *Request) {
	t.Helper()
	require.NoError(t, s.Load(&Scenario{Name: "s", Turns: []*Turn{{Text: "late", Gate: "g"}}}))
	req := scriptRequest(t, anthropicTools())
	turn, g, err := s.script.answer(req)
	require.NoError(t, err)
	require.NotNil(t, g)
	return g, turn, req
}

// A harness that gives up on a gated turn never got it, though the turn is
// used up: that is the scenario's failure, not a turn played.
func TestWaitAHarnessHangingUpAtAGateIsAnError(t *testing.T) {
	t.Parallel()
	s := newInternalServer(t)
	g, turn, req := gatedAnswer(t, s)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := s.wait(ctx, g, turn, req)
	require.ErrorIs(t, err, ErrUnexpectedRequest)
	assert.Contains(t, err.Error(), "hung up")
	assert.Len(t, s.Recording().Errors, 1)
}

func TestWaitReplacingTheScenarioIsNotAnError(t *testing.T) {
	t.Parallel()
	s := newInternalServer(t)
	g, turn, req := gatedAnswer(t, s)
	s.Reset()
	ctx, cancel := context.WithCancel(t.Context())
	// A hang-up at the same time does not make it one either.
	cancel()
	require.NoError(t, s.wait(ctx, g, turn, req))
	assert.Empty(t, s.Recording().Errors)
}

func TestWaitReturnsOnceTheGateOpens(t *testing.T) {
	t.Parallel()
	s := newInternalServer(t)
	g, turn, req := gatedAnswer(t, s)
	s.Release("g")
	require.NoError(t, s.wait(t.Context(), g, turn, req))
}

// The gate is taken before the Load, which a caller of WaitHeld cannot
// order: so this is where the replaced-scenario case is pinned down.
func TestWaitHeldEndsWhenTheScenarioIsReplaced(t *testing.T) {
	t.Parallel()
	s := newInternalServer(t)
	g := s.script.gate("g")
	waited := make(chan error, 1)
	go func() { waited <- s.waitHeld(t.Context(), g) }()
	require.NoError(t, s.Load(&Scenario{Name: "next", Turns: []*Turn{{Text: "x", Gate: "g"}}}))
	require.ErrorIs(t, <-waited, ErrGateRetired)
}

func TestWaitHeldAnswersOnceARequestWaits(t *testing.T) {
	t.Parallel()
	s := newInternalServer(t)
	g, _, _ := gatedAnswer(t, s)
	g.markHeld()
	require.NoError(t, s.WaitHeld(t.Context(), "g"))
}

func TestWaitHeldEndsWhenTheServerCloses(t *testing.T) {
	t.Parallel()
	s := newInternalServer(t)
	require.NoError(t, s.Close())
	require.ErrorIs(t, s.WaitHeld(t.Context(), "g"), ErrServerClosed)
}
