package ticket_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestWorkerSnapshot_NoLeaseEverIsErrNoLease(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	_, err := f.svc.WorkerSnapshot(f.ctx, f.agent(t, "builder-1"))

	assert.ErrorIs(t, err, ticket.ErrNoLease)
}

func TestWorkerSnapshot_AHeldLeaseIsActiveOnItsTicketAndAttempt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	f.ticket(t, "Work", "server/")
	held, lease := f.heldLease(t, a)

	snap, err := f.svc.WorkerSnapshot(f.ctx, a)

	require.NoError(t, err)
	assert.True(t, snap.Active)
	assert.Equal(t, lease.ID, snap.LeaseID)
	assert.Equal(t, held.ID, snap.TicketID)
	assert.Equal(t, 1, snap.Attempt)
	assert.Equal(t, ticket.StatusInProgress, snap.TicketStatus)
	assert.Equal(t, "in_progress", snap.AttemptStatus)
	assert.True(t, snap.Promoted)
}

// A supervisor reads this after the run has exited: the lease is gone, and
// what the run left the ticket as is the whole answer.
func TestWorkerSnapshot_AnEndedLeaseStillSaysWhereTheTicketWent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	f.ticket(t, "Work", "server/")
	held, lease := f.heldLease(t, a)
	_, err := f.svc.Discuss(f.ctx, a, "Which shape?", "Half done.")
	require.NoError(t, err)

	snap, err := f.svc.WorkerSnapshot(f.ctx, a)

	require.NoError(t, err)
	assert.False(t, snap.Active)
	assert.Equal(t, lease.ID, snap.LeaseID)
	assert.Equal(t, held.ID, snap.TicketID)
	assert.Equal(t, ticket.StatusDiscussion, snap.TicketStatus)
	assert.Equal(t, "discussion", snap.AttemptStatus)
}

func TestWorkerSnapshot_IsTheLatestLeaseNotAnEarlierOne(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	f.ticket(t, "Work", "server/")
	_, first := f.heldLease(t, a)
	_, err := f.svc.Release(f.ctx, a, "swap", nil)
	require.NoError(t, err)
	_, second := f.heldLease(t, a)
	require.NotEqual(t, first.ID, second.ID)

	snap, err := f.svc.WorkerSnapshot(f.ctx, a)

	require.NoError(t, err)
	assert.True(t, snap.Active)
	assert.Equal(t, second.ID, snap.LeaseID)
}

func TestWorkerSnapshot_ReadsOnlyItNeitherExtendsTheLeaseNorTakesTheNotice(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	f.ticket(t, "Work", "server/")
	_, lease := f.heldLease(t, a)

	first, err := f.svc.WorkerSnapshot(f.ctx, a)
	require.NoError(t, err)
	second, err := f.svc.WorkerSnapshot(f.ctx, a)
	require.NoError(t, err)

	assert.Equal(t, lease.ExpiresAt, first.ExpiresAt)
	assert.True(t, first.ExpiresAt.Equal(second.ExpiresAt), "a read does not push the expiry out")
}

// The revocation notice is owed to the agent's next MCP call. A supervisor
// polling the state in between must not be the one to take it.
func TestWorkerSnapshot_LeavesAHumansRevocationNoticeForTheAgent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	held := f.ticket(t, "Work", "server/")
	f.claim(t, a)
	_, err := f.svc.MoveMany(f.ctx, []int64{held.ID}, ticket.StatusPreparing)
	require.NoError(t, err)

	snap, err := f.svc.WorkerSnapshot(f.ctx, a)
	require.NoError(t, err)
	assert.False(t, snap.Active)

	assert.ErrorIs(t, f.svc.Heartbeat(f.ctx, a), ticket.ErrLeaseRevoked, "the notice was still there to be delivered")
}
