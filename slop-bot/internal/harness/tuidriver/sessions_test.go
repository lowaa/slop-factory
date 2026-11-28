package tuidriver_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake/faketest"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/tuidriver"
)

func TestSessionName(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "agy-t7-a1_b_c", tuidriver.SessionName("agy-", "t7-a1.b:c"))
}

func TestAdoptAllTakesOnlyItsOwnSessions(t *testing.T) {
	t.Parallel()
	r := start(t)
	ref := r.conv.Ref()
	require.NoError(t, r.conv.Close())
	// Another harness's session on the same server.
	require.NoError(t, r.Host.Start(r.ctx, &sessionhost.StartSpec{Name: "other-a2", Argv: []string{"sleep", "60"}}))

	s := &tuidriver.Sessions{Host: r.Host, Hub: r.Hub, Pointer: faketest.Pointer, Clock: r.clk}
	convs, err := s.AdoptAll(r.ctx, r.Harness.Profile(), "fake-")
	require.NoError(t, err)
	require.Len(t, convs, 1)
	t.Cleanup(func() { _ = convs[0].Stop(context.Background(), true) })
	assert.Equal(t, ref, convs[0].Ref())
	st, err := r.Host.Status(r.ctx, "other-a2")
	require.NoError(t, err)
	assert.False(t, st.Dead, "left alone")
}
