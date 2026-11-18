package ticket_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func (f *fixture) longHeldFlag(t *testing.T) bool {
	t.Helper()
	rec := send(t, f.boardMux(), http.MethodGet, "/api/integration_lock", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		Holder *ticket.LockHolderBody `json:"holder"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.NotNil(t, got.Holder)
	return got.Holder.LongHeld
}

// A heartbeating holder is alive, so only the hold's age can flag it.
func TestIntegrationLockRoute_FlagsAHoldPastTheLongHeldThreshold(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))

	f.ageHolder(t, m[0].ticket.ID, "1 minute", "89 minutes")
	assert.False(t, f.longHeldFlag(t), "under the threshold")
	f.ageHolder(t, m[0].ticket.ID, "1 minute", "91 minutes")
	assert.True(t, f.longHeldFlag(t), "past it, though the holder heartbeats")
}
