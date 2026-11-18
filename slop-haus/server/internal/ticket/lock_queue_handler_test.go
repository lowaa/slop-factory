package ticket_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

func TestLockIntegration_RefusalCarriesTheQueuePositionAndWhoIsAhead(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.agentMux()
	m := f.mergers(t, "holder", "first", "second")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))

	for _, w := range m[1:] {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, lockRoute(w.id, http.MethodPost))
		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, lockRoute(m[2].id, http.MethodPost))

	require.Equal(t, http.StatusConflict, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "number 2 in the queue")
	assert.Contains(t, body, `agent \"first\"`, "who is ahead")
	assert.Contains(t, body, "queued 20", "when they queued, as a server timestamp")
	assert.Contains(t, body, "end your turn")
}

func TestIntegrationLockRoutes_TheBoardSeesTheHolderAndQueue(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder", "waiting")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	require.ErrorIs(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id), ticket.ErrLockHeld)

	rec := send(t, f.boardMux(), http.MethodGet, "/api/integration_lock", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		Holder *ticket.LockHolderBody      `json:"holder"`
		Queue  []ticket.LockQueueEntryBody `json:"queue"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.NotNil(t, got.Holder)
	assert.Equal(t, "holder", got.Holder.AgentName)
	assert.Equal(t, m[0].ticket.ID, got.Holder.TicketID)
	require.Len(t, got.Queue, 1)
	assert.Equal(t, "waiting", got.Queue[0].AgentName)
	assert.Equal(t, m[1].id.String(), got.Queue[0].AgentID)
}

func TestIntegrationLockRoutes_APersonReleasesTheLockAndDropsAnEntry(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	m := f.mergers(t, "dead", "dropped", "waiting")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	require.ErrorIs(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id), ticket.ErrLockHeld)
	require.ErrorIs(t, f.svc.AcquireIntegrationLock(f.ctx, m[2].id), ticket.ErrLockHeld)

	rec := send(t, mux, http.MethodDelete, "/api/integration_lock/queue/"+m[1].id.String(), "")
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	rec = send(t, mux, http.MethodDelete, "/api/integration_lock/queue/"+m[1].id.String(), "")
	assert.Equal(t, http.StatusNotFound, rec.Code, "already gone")
	rec = send(t, mux, http.MethodDelete, "/api/integration_lock/queue/not-a-uuid", "")
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	rec = send(t, mux, http.MethodDelete, "/api/integration_lock/holder", "")
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	rec = send(t, mux, http.MethodDelete, "/api/integration_lock/holder", "")
	assert.Equal(t, http.StatusNotFound, rec.Code, "nothing held")

	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[2].id), "the next in line has it")
}

// The release and the drop take a lock from an agent that has not finished
// with it: people only. An agent's own token must not reach them, or any
// agent could knock another off the lock.
func TestIntegrationLockRoutes_AgentsCannotReleaseOrDropForOthers(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	mux, _ := f.rolesMux(t, true)
	m := f.mergers(t, "holder", "waiting")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	require.ErrorIs(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id), ticket.ErrLockHeld)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/integration_lock"},
		{http.MethodDelete, "/api/integration_lock/holder"},
		{http.MethodDelete, "/api/integration_lock/queue/" + m[1].id.String()},
	} {
		rec := sendAs(t, mux, tc.method, tc.path, rolesAgentToken, "")
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s: %s", tc.method, tc.path, rec.Body.String())
	}
	state, err := f.svc.IntegrationLockState(f.ctx)
	require.NoError(t, err)
	require.NotNil(t, state.Holder)
	assert.Len(t, state.Queue, 1)

	// The token itself works: it is refused for what it is. (The caller has
	// no ticket, so its own lock call is refused for that, not as a person's.)
	rec := sendAs(t, mux, http.MethodPost, "/api/integration_lock", rolesAgentToken, "")
	assert.NotEqual(t, http.StatusUnauthorized, rec.Code)
}

func TestIntegrationLockRoutes_TheQueueShowsPriorityAndWhyAnEntryMovedUp(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder", "normal", "urgent")
	f.setPriority(t, m[2], model.PriorityUrgent)
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	f.queued(t, m[1], m[2])

	rec := send(t, f.boardMux(), http.MethodGet, "/api/integration_lock", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		Queue []ticket.LockQueueEntryBody `json:"queue"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Queue, 2)
	assert.Equal(t, "urgent", got.Queue[0].AgentName)
	assert.Equal(t, "urgent", got.Queue[0].Priority)
	assert.Equal(t, "priority", got.Queue[0].MovedUpBy)
	assert.Equal(t, "normal", got.Queue[1].Priority)
	assert.Empty(t, got.Queue[1].MovedUpBy)
}
