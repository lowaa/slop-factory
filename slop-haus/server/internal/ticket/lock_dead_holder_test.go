package ticket_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// ageHolder moves the holder's last heartbeat and lock acquisition back,
// independently, so a test can tell which of the two the verdict follows.
func (f *fixture) ageHolder(t *testing.T, ticketID int64, sinceHeartbeat, sinceAcquired string) {
	t.Helper()
	_, err := f.database.Exec(f.ctx,
		`UPDATE leases SET last_heartbeat_at = now() - $2::interval WHERE ticket_id = $1`, ticketID, sinceHeartbeat)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx,
		`UPDATE locks SET acquired_at = now() - $1::interval`, sinceAcquired)
	require.NoError(t, err)
}

func (f *fixture) setDeadAfter(t *testing.T, minutes int64) {
	t.Helper()
	_, err := f.database.Exec(f.ctx,
		`INSERT INTO tenant_settings (setting_name, setting_type, int_value) VALUES ($1, 'int', $2)`,
		settings.IntegrationLockDeadHolderAfter, minutes)
	require.NoError(t, err)
}

func (f *fixture) holderState(t *testing.T) *ticket.LockHolder {
	t.Helper()
	state, err := f.svc.IntegrationLockState(f.ctx)
	require.NoError(t, err)
	require.NotNil(t, state.Holder)
	return state.Holder
}

func TestLockHolder_DeadWhenTheHeartbeatIsOlderThanTheSetting(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))

	assert.False(t, f.holderState(t).Dead, "fresh")
	f.ageHolder(t, m[0].ticket.ID, "61 minutes", "61 minutes")
	h := f.holderState(t)
	assert.True(t, h.Dead)
	assert.GreaterOrEqual(t, h.SinceHeartbeat.Minutes(), 61.0)
}

func TestLockHolder_HeartbeatingHolderIsNotDeadHoweverLongItHoldsTheLock(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	f.ageHolder(t, m[0].ticket.ID, "3 hours", "3 hours")
	require.True(t, f.holderState(t).Dead)

	require.NoError(t, f.svc.Heartbeat(f.ctx, m[0].id))
	h := f.holderState(t)
	assert.False(t, h.Dead, "a heartbeat renews it; the lock was still acquired 3 hours ago")
	assert.Greater(t, h.Held.Hours(), 2.9)
}

func TestLockHolder_TheTenantSettingSetsTheThreshold(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	f.ageHolder(t, m[0].ticket.ID, "10 minutes", "10 minutes")

	assert.False(t, f.holderState(t).Dead, "default is an hour")
	f.setDeadAfter(t, 5)
	assert.True(t, f.holderState(t).Dead)
	f.setDeadAfter(t, 120)
	assert.False(t, f.holderState(t).Dead, "the newest value wins")
}

func TestDeadHolderSetting_DefaultsToAnHourAndIsBounded(t *testing.T) {
	t.Parallel()
	def, err := settings.Default(settings.IntegrationLockDeadHolderAfter)
	require.NoError(t, err)
	assert.EqualValues(t, 60, def.Int)
}

func TestIntegrationLockRoute_CarriesTheDeadFlag(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	f.ageHolder(t, m[0].ticket.ID, "90 minutes", "90 minutes")

	rec := send(t, f.boardMux(), http.MethodGet, "/api/integration_lock", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		Holder *ticket.LockHolderBody `json:"holder"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.NotNil(t, got.Holder)
	assert.True(t, got.Holder.Dead)
	assert.GreaterOrEqual(t, got.Holder.SinceHeartbeatSeconds, int64(90*60))
	assert.False(t, got.Holder.LastHeartbeatAt.IsZero())
}

func TestLockHolder_ThresholdBoundary(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	f.setDeadAfter(t, 5)

	f.ageHolder(t, m[0].ticket.ID, "4 minutes 50 seconds", "1 minute")
	assert.False(t, f.holderState(t).Dead, "just under")
	f.ageHolder(t, m[0].ticket.ID, "5 minutes", "1 minute")
	assert.True(t, f.holderState(t).Dead, "at the threshold")
}
