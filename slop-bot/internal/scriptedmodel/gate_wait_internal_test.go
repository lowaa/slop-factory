package scriptedmodel

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// With the gate open and the harness gone, wait's select may take either
// case: the opened gate, or the hang-up, which must then see the open gate
// and not report an error. The pick is random, so go round until the hang-up
// case has certainly been taken (2^-200 otherwise); every pass must agree.
func TestWaitAnOpenGateWinsOverAHangUp(t *testing.T) {
	t.Parallel()
	s := newInternalServer(t)
	g, turn, req := gatedAnswer(t, s)
	g.open()
	require.True(t, g.isOpen())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for range 200 {
		require.NoError(t, s.wait(ctx, g, turn, req))
	}
	assert.Empty(t, s.Recording().Errors)
}

func TestGateIsOpenOnlyOnceOpened(t *testing.T) {
	t.Parallel()
	g := newGate()
	assert.False(t, g.isOpen())
	g.open()
	assert.True(t, g.isOpen())
}
