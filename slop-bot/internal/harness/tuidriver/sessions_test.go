package tuidriver_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/tuidriver"
)

func TestSessionName(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "agy-t7-a1_b_c", tuidriver.SessionName("agy-", "t7-a1.b:c"))
}

func TestOwnsSession(t *testing.T) {
	t.Parallel()
	assert.True(t, tuidriver.OwnsSession("agy-t7-abc", "t7-abc"))
	assert.True(t, tuidriver.OwnsSession("fake-t7-a_b", "t7-a.b"))
	for _, s := range []string{
		"agy-t17-abc", // another agent whose id ends the same
		"agy-xt7-abc",
		"agy-t7-abcd",
		"t7-abc",       // no prefix
		"-t7-abc",      // an empty one
		"ag1-t7-abc",   // not a harness's
		"agy_x-t7-abc", // nor this
	} {
		assert.False(t, tuidriver.OwnsSession(s, "t7-abc"), s)
	}
}

func TestAdoptWithoutASessionIsErrNoSession(t *testing.T) {
	t.Parallel()
	r := start(t)
	_, err := r.Harness.AdoptAgent(r.ctx, "a2")
	require.ErrorIs(t, err, harness.ErrNoSession)

	// Nor once the server has gone.
	require.NoError(t, r.conv.Close())
	require.NoError(t, r.Host.KillServer(context.Background()))
	_, err = r.Harness.AdoptAgent(r.ctx, "a1")
	require.ErrorIs(t, err, harness.ErrNoSession)
}
