package state_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

func TestMergeGrantsArePerTicketUntilRevoked(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	granted := func(ticket ident.TicketID) bool {
		t.Helper()
		ok, err := f.MergeGrants.Granted(ctx, ticket)
		require.NoError(t, err)
		return ok
	}
	assert.False(t, granted("7"))

	require.NoError(t, f.MergeGrants.Grant(ctx, "7", state.UserParty))
	require.NoError(t, f.MergeGrants.Grant(ctx, "7", state.UserParty), "granting twice is one grant")
	assert.True(t, granted("7"))
	assert.False(t, granted("8"), "a grant is its ticket's alone")

	require.NoError(t, f.MergeGrants.Revoke(ctx, "7"))
	assert.False(t, granted("7"))
	require.NoError(t, f.MergeGrants.Revoke(ctx, "7"), "revoking what isn't granted is a no-op")
}

func TestMergeGrantsRefuseABlankTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	assert.Error(t, f.MergeGrants.Grant(context.Background(), "", state.UserParty))
}
