package ticket_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

const branchLock = "integration:app@release"

// twoLocks leaves the default lock held by m[0] and the branch lock held by
// m[1] with m[2] queued behind it. The branch lock is made by renaming a
// taken one: which name a ticket merges under is the base-branch resolver's
// business, tested with it.
func (f *fixture) twoLocks(t *testing.T) []merger {
	t.Helper()
	m := f.mergers(t, "branch-holder", "branch-waiter", "default-holder")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	_, err := f.database.Exec(f.ctx, `UPDATE locks SET name = $1`, branchLock)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `UPDATE lock_holds SET name = $1`, branchLock)
	require.NoError(t, err)
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[2].id))
	_, err = f.database.Exec(f.ctx, `
		INSERT INTO lock_waiters (name, agent_id, lease_id)
		SELECT $1, $2, id FROM leases WHERE ticket_id = $3`, branchLock, m[1].id, m[1].ticket.ID)
	require.NoError(t, err)
	return m
}

func lockQuery(path, name string) string {
	return path + "?name=" + url.QueryEscape(name)
}

func TestIntegrationLocks_ListsTheDefaultAndEachBranchLock(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.twoLocks(t)

	rec := send(t, f.boardMux(), http.MethodGet, "/api/integration_locks", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		Locks []ticket.NamedLockBody `json:"locks"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Locks, 2)

	assert.Equal(t, "integration", got.Locks[0].Name)
	assert.Empty(t, got.Locks[0].Repo)
	require.NotNil(t, got.Locks[0].Holder)
	assert.Equal(t, m[2].id.String(), got.Locks[0].Holder.AgentID)

	assert.Equal(t, branchLock, got.Locks[1].Name)
	assert.Equal(t, "app", got.Locks[1].Repo)
	assert.Equal(t, "release", got.Locks[1].Branch)
	require.NotNil(t, got.Locks[1].Holder)
	assert.Equal(t, m[0].id.String(), got.Locks[1].Holder.AgentID)
	require.Len(t, got.Locks[1].Queue, 1)
	assert.Equal(t, m[1].id.String(), got.Locks[1].Queue[0].AgentID)
}

func TestIntegrationLocks_TheDefaultLockIsListedWhenIdle(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	locks, err := f.svc.IntegrationLocks(f.ctx)

	require.NoError(t, err)
	require.Len(t, locks, 1)
	assert.Equal(t, ticket.IntegrationLockName, locks[0].Name)
	assert.Nil(t, locks[0].Holder)
}

func TestIntegrationLockRoutes_ForceReleaseByNameFreesOnlyThatLock(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.twoLocks(t)

	rec := send(t, f.boardMux(), http.MethodDelete, lockQuery("/api/integration_lock/holder", branchLock), "")
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	branch, err := f.svc.IntegrationLockStateNamed(f.ctx, branchLock)
	require.NoError(t, err)
	assert.Nil(t, branch.Holder, "the named lock is free")
	def, err := f.svc.IntegrationLockState(f.ctx)
	require.NoError(t, err)
	require.NotNil(t, def.Holder, "the default lock is untouched")
	assert.Equal(t, m[2].id, def.Holder.AgentID)
}

func TestIntegrationLockRoutes_NoNameMeansTheDefaultLock(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.twoLocks(t)

	rec := send(t, f.boardMux(), http.MethodDelete, "/api/integration_lock/holder", "")
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	def, err := f.svc.IntegrationLockState(f.ctx)
	require.NoError(t, err)
	assert.Nil(t, def.Holder)
	branch, err := f.svc.IntegrationLockStateNamed(f.ctx, branchLock)
	require.NoError(t, err)
	require.NotNil(t, branch.Holder)
	assert.Equal(t, m[0].id, branch.Holder.AgentID)
}

func TestIntegrationLockRoutes_DropWaiterByNameLeavesTheOtherQueue(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.twoLocks(t)
	path := "/api/integration_lock/queue/" + m[1].id.String()

	rec := send(t, f.boardMux(), http.MethodDelete, path, "")
	assert.Equal(t, http.StatusNotFound, rec.Code, "not queued on the default lock")

	rec = send(t, f.boardMux(), http.MethodDelete, lockQuery(path, branchLock), "")
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	branch, err := f.svc.IntegrationLockStateNamed(f.ctx, branchLock)
	require.NoError(t, err)
	assert.Empty(t, branch.Queue)
}

func TestIntegrationLockRoutes_AnUnknownLockNameIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.twoLocks(t)
	mux := f.boardMux()

	for _, name := range []string{"integration:nope@main", "other", "integration:"} {
		for _, req := range []struct{ method, path string }{
			{http.MethodGet, "/api/integration_lock"},
			{http.MethodDelete, "/api/integration_lock/holder"},
			{http.MethodDelete, "/api/integration_lock/queue/" + m[1].id.String()},
		} {
			rec := send(t, mux, req.method, lockQuery(req.path, name), "")
			assert.Equal(t, http.StatusNotFound, rec.Code, "%s %s %q: %s", req.method, req.path, name, rec.Body.String())
		}
	}
	locks, err := f.svc.IntegrationLocks(f.ctx)
	require.NoError(t, err)
	for _, l := range locks {
		assert.NotNil(t, l.Holder, "%s still held", l.Name)
	}
}

func TestIntegrationLocks_AnotherTenantsLockNameIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.twoLocks(t)
	other := db.WithTenant(context.Background(), dbtest.NewTenant(t, f.database))

	_, err := f.svc.ForceReleaseNamedLock(other, branchLock)

	require.ErrorIs(t, err, ticket.ErrUnknownLock)
	branch, err := f.svc.IntegrationLockStateNamed(f.ctx, branchLock)
	require.NoError(t, err)
	assert.NotNil(t, branch.Holder, "still held")
}

func TestParseLockName(t *testing.T) {
	t.Parallel()
	repo, branch := ticket.ParseLockName("integration")
	assert.Empty(t, repo)
	assert.Empty(t, branch)
	repo, branch = ticket.ParseLockName("integration:app/web@feat@x")
	assert.Equal(t, "app/web", repo)
	assert.Equal(t, "feat@x", branch)
}
