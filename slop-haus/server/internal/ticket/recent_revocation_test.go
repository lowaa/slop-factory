package ticket_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestRecentRevocation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	held := f.ticket(t, "Held", "server/")
	f.claim(t, a)
	f.moveMany(t, ticket.StatusCancelled, held.ID)

	recent := func() *ticket.RevocationContext {
		t.Helper()
		got, err := f.svc.RecentRevocation(f.ctx, a)
		require.NoError(t, err)
		return got
	}

	assert.Nil(t, recent(), "the notice is still owed, so there is no context yet")
	var notice *ticket.LeaseRevokedError
	require.ErrorAs(t, f.svc.Heartbeat(f.ctx, a), &notice)

	got := recent()
	require.NotNil(t, got)
	assert.Equal(t, held.ID, got.TicketID)
	assert.Equal(t, ticket.StatusCancelled, got.Status)
	assert.NotNil(t, recent(), "reading it changes nothing")
	assert.ErrorIs(t, f.svc.Heartbeat(f.ctx, a), ticket.ErrNoLease, "and does not repeat the notice")

	_, err := f.database.Exec(f.ctx,
		`UPDATE leases SET released_at = now() - $2::interval WHERE agent_id = $1`,
		a, (ticket.DefaultLeaseTTL + 1).String())
	require.NoError(t, err)
	assert.Nil(t, recent(), "a lease TTL later it is history")
}

func TestRecentRevocation_NotAfterAnotherLease(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	held := f.ticket(t, "Held", "server/")
	f.claim(t, a)
	f.moveMany(t, ticket.StatusCancelled, held.ID)
	require.Error(t, f.svc.Heartbeat(f.ctx, a))
	f.ticket(t, "Next", "web/")
	f.claim(t, a)

	got, err := f.svc.RecentRevocation(f.ctx, a)
	require.NoError(t, err)
	assert.Nil(t, got)
}
