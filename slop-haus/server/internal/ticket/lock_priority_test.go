package ticket_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

func (f *fixture) setPriority(t *testing.T, m merger, p model.Priority) {
	t.Helper()
	_, err := f.svc.SetPriorityMany(f.ctx, []int64{m.ticket.ID}, p)
	require.NoError(t, err)
}

// waitedFor backdates the agent's queue entry, so the aging a test needs does
// not rest on sleeping.
func (f *fixture) waitedFor(t *testing.T, m merger, interval string) {
	t.Helper()
	_, err := f.database.Exec(f.ctx,
		`UPDATE lock_waiters SET queued_at = now() - $2::interval WHERE agent_id = $1`, m.id, interval)
	require.NoError(t, err)
}

func (f *fixture) queueOrder(t *testing.T) []string {
	t.Helper()
	state, err := f.svc.IntegrationLockState(f.ctx)
	require.NoError(t, err)
	names := []string{}
	for _, e := range state.Queue {
		names = append(names, e.AgentName)
	}
	return names
}

// queued has each waiter ask in turn, behind a holder.
func (f *fixture) queued(t *testing.T, waiters ...merger) {
	t.Helper()
	for _, w := range waiters {
		f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, w.id))
	}
}

func TestLockQueue_AnUrgentWaiterQueuedLastIsOfferedTheLockNext(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder", "n1", "n2", "urgent")
	f.setPriority(t, m[3], model.PriorityUrgent)
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	f.queued(t, m[1], m[2])
	urgent := f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[3].id))

	assert.Equal(t, 1, urgent.Position)
	assert.Equal(t, ticket.LockMovedByPriority, urgent.MovedUpBy)
	assert.Contains(t, urgent.Error(), "moved up by your ticket's priority")
	assert.Equal(t, []string{"urgent", "n1", "n2"}, f.queueOrder(t))

	require.NoError(t, f.svc.ReleaseIntegrationLock(f.ctx, m[0].id))
	f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id))
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[3].id))
}

func TestLockQueue_HighGoesBeforeNormalWhenTheyArriveTogether(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder", "normal", "high", "low")
	f.setPriority(t, m[2], model.PriorityHigh)
	f.setPriority(t, m[3], model.PriorityLow)
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	f.queued(t, m[3], m[1], m[2])

	assert.Equal(t, []string{"high", "normal", "low"}, f.queueOrder(t))
}

func TestLockQueue_WaitersOfOnePriorityStayFirstInFirstOut(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder", "a", "b", "c")
	for _, w := range m[1:] {
		f.setPriority(t, w, model.PriorityHigh)
	}
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	f.queued(t, m[2], m[1], m[3])

	assert.Equal(t, []string{"b", "a", "c"}, f.queueOrder(t))
}

func TestLockQueue_AgingServesALongWaitingLowBeforeAFreshHigh(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder", "low", "high")
	f.setPriority(t, m[1], model.PriorityLow)
	f.setPriority(t, m[2], model.PriorityHigh)
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	f.queued(t, m[2], m[1])
	assert.Equal(t, []string{"high", "low"}, f.queueOrder(t))

	// Low is rank 3, high 1: two steps tie them (and low queued first); three
	// pass it.
	f.waitedFor(t, m[1], "95 minutes")
	state, err := f.svc.IntegrationLockState(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"low", "high"}, f.queueOrder(t))
	assert.Equal(t, ticket.LockMovedByAging, state.Queue[0].MovedUpBy)
	assert.Equal(t, model.PriorityLow, state.Queue[0].Priority)

	require.NoError(t, f.svc.ReleaseIntegrationLock(f.ctx, m[0].id))
	f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[2].id))
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id))
}

func TestLockQueue_TheAgingStepIsTheTenantsSetting(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder", "low", "high")
	f.setPriority(t, m[1], model.PriorityLow)
	f.setPriority(t, m[2], model.PriorityHigh)
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	f.queued(t, m[1], m[2])
	f.waitedFor(t, m[1], "95 minutes")
	require.Equal(t, []string{"low", "high"}, f.queueOrder(t))

	_, err := f.database.Exec(f.ctx,
		`INSERT INTO tenant_settings (setting_name, setting_type, int_value) VALUES ($1, 'int', 240)`,
		settings.IntegrationLockPriorityAgingMinutes)
	require.NoError(t, err)
	assert.Equal(t, []string{"high", "low"}, f.queueOrder(t))
}

func TestLockQueue_ARaisedTicketMovesUpWhileItWaits(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder", "first", "second")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	f.queued(t, m[1], m[2])
	require.Equal(t, []string{"first", "second"}, f.queueOrder(t))

	f.setPriority(t, m[2], model.PriorityUrgent)
	assert.Equal(t, []string{"second", "first"}, f.queueOrder(t))
	require.NoError(t, f.svc.ReleaseIntegrationLock(f.ctx, m[0].id))
	f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id))
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[2].id))
}

func TestLockQueue_AnUrgentWaiterNeverPreemptsTheHolder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder", "urgent")
	f.setPriority(t, m[1], model.PriorityUrgent)
	f.setPriority(t, m[0], model.PriorityLow)
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	f.queued(t, m[1])

	holder, held := f.lockHeldBy(t)
	require.True(t, held)
	assert.Equal(t, m[0].id, holder)
}

func TestLockQueue_AnOfferedHeadKeepsItWhenAnUrgentWaiterJoins(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder", "head", "urgent")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	f.queued(t, m[1])
	require.NoError(t, f.svc.ReleaseIntegrationLock(f.ctx, m[0].id))
	f.setPriority(t, m[2], model.PriorityUrgent)

	f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[2].id))
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id))
}
