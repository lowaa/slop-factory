package ticket_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

type holdRow struct {
	TicketID int64
	Queued   bool
	Open     bool
	Wait     time.Duration
}

func (f *fixture) lockHolds(t *testing.T) []holdRow {
	t.Helper()
	rows, err := f.database.Query(f.ctx, `
		SELECT ticket_id, queued_at IS NOT NULL, released_at IS NULL,
		       COALESCE(acquired_at - queued_at, '0'::interval)
		  FROM lock_holds ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	var out []holdRow
	for rows.Next() {
		var r holdRow
		require.NoError(t, rows.Scan(&r.TicketID, &r.Queued, &r.Open, &r.Wait))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestLockHolds_ATakenFreeLockIsAHoldWithNoWaitClosedByTheRelease(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "solo")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))

	assert.Equal(t, []holdRow{{TicketID: m[0].ticket.ID, Open: true}}, f.lockHolds(t))

	require.NoError(t, f.svc.ReleaseIntegrationLock(f.ctx, m[0].id))
	assert.Equal(t, []holdRow{{TicketID: m[0].ticket.ID}}, f.lockHolds(t))
}

func TestLockHolds_AQueuedAgentsHoldRemembersWhenItJoinedTheQueue(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder", "waiting")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id))
	// Backdate the queue entry so the wait is a known size.
	_, err := f.database.Exec(f.ctx, `UPDATE lock_waiters SET queued_at = now() - interval '30 minutes'`)
	require.NoError(t, err)
	require.NoError(t, f.svc.ReleaseIntegrationLock(f.ctx, m[0].id))
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id))

	holds := f.lockHolds(t)
	require.Len(t, holds, 2)
	assert.False(t, holds[0].Queued)
	assert.True(t, holds[1].Queued)
	assert.True(t, holds[1].Open)
	assert.InDelta(t, (30 * time.Minute).Seconds(), holds[1].Wait.Seconds(), 5)
}

func TestLockHolds_EveryWayTheLockIsFreedClosesTheHold(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "forced", "expired")

	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	_, err := f.svc.ForceReleaseNamedLock(f.ctx, ticket.IntegrationLockName)
	require.NoError(t, err)

	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id))
	f.expire(t, m[1].id)
	_, err = f.svc.Release(f.ctx, m[1].id, "done", nil)
	require.NoError(t, err)

	holds := f.lockHolds(t)
	require.Len(t, holds, 2)
	for _, h := range holds {
		assert.False(t, h.Open, "%+v", h)
	}
}
