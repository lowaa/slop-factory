package manifest_test

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
)

func TestTheRevisionCountsEverySaveAndClearAndIsPerTenant(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	revision := func(f *tenantFixture) int64 {
		rev, err := f.svc.Revision(f.ctx)
		require.NoError(t, err)
		return rev
	}
	repos := []manifest.Repo{{Path: "app", Remote: "github.com/a/b"}}
	assert.Zero(t, revision(a), "before any save")

	m, err := a.svc.Set(a.ctx, a.owner, repos)
	require.NoError(t, err)
	assert.EqualValues(t, 1, m.Revision)
	m, err = a.svc.Set(a.ctx, a.owner, repos)
	require.NoError(t, err)
	assert.EqualValues(t, 2, m.Revision, "a save that changes nothing is still a save")
	require.NoError(t, a.svc.Clear(a.ctx, a.owner))
	assert.EqualValues(t, 3, revision(a), "kept through a clear")
	got, err := a.svc.Get(a.ctx)
	require.NoError(t, err)
	assert.Nil(t, got)

	m, err = a.svc.Set(a.ctx, a.owner, repos)
	require.NoError(t, err)
	assert.EqualValues(t, 4, m.Revision)
	assert.Zero(t, revision(b), "another tenant's saves are not this one's")
}

func TestPutAtRefusesAnyRevisionButTheCurrentOne(t *testing.T) {
	e := newEnv(t)
	f := e.tenant()
	store := manifest.NewPostgresStore(e.database)
	repos := []manifest.Repo{{Path: "app", Remote: "github.com/a/b"}}
	put := func(base int64) error {
		return e.database.InTx(f.ctx, func(q db.Querier) error {
			_, err := store.LockRevision(f.ctx, q)
			if err != nil {
				return err
			}
			_, err = store.PutAt(f.ctx, q, repos, f.owner.ID, base)
			return err
		})
	}
	require.ErrorIs(t, put(3), manifest.ErrStaleRevision)
	require.ErrorIs(t, put(1), manifest.ErrStaleRevision)
	require.NoError(t, put(0))
	require.ErrorIs(t, put(0), manifest.ErrStaleRevision)
	require.NoError(t, put(1))
	rev, err := store.Revision(f.ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 2, rev)
}

func TestLockRevisionHoldsTheRowUntilTheTransactionEnds(t *testing.T) {
	e := newEnv(t)
	f := e.tenant()
	store := manifest.NewPostgresStore(e.database)
	_, err := f.svc.Set(f.ctx, f.owner, []manifest.Repo{{Path: "app", Remote: "github.com/a/b"}})
	require.NoError(t, err)
	err = e.database.InTx(f.ctx, func(q db.Querier) error {
		if _, err := store.LockRevision(f.ctx, q); err != nil {
			return err
		}
		// Another connection cannot take the row, which is what keeps a
		// board save out between an approval's check and its write.
		other := e.database.InTx(f.ctx, func(q2 db.Querier) error {
			_, err := q2.Exec(f.ctx, `SELECT revision FROM workspace_manifest_revisions FOR UPDATE NOWAIT`)
			return err
		})
		var pgErr *pgconn.PgError
		require.ErrorAs(t, other, &pgErr)
		assert.Equal(t, "55P03", pgErr.Code)
		return nil
	})
	require.NoError(t, err)
}

func TestTheWorkspaceRoutesReturnTheRevision(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	body := `{"repos":[{"path":"app","remote":"github.com/acme/app"}]}`
	require.Equal(t, http.StatusOK, call(a.server(a.owner), http.MethodPut, "/api/workspace/manifest", body).Code)
	assert.EqualValues(t, 1, getWorkspace(t, a.server(a.owner)).Manifest.Revision)
	rec := call(a.server(a.owner), http.MethodPut, "/api/workspace/manifest", body)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"revision":2`)
	assert.EqualValues(t, 2, getWorkspace(t, a.server(a.owner)).Manifest.Revision)
}
