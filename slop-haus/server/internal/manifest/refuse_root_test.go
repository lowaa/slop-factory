package manifest_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
)

func TestSet_RefusesARootRepo(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	for _, path := range []string{".", " . "} {
		_, err := a.svc.Set(a.ctx, a.owner, []manifest.Repo{{Path: path, Remote: "github.com/acme/mono"}})
		require.ErrorIs(t, err, manifest.ErrInvalid, path)
		assert.Contains(t, err.Error(), "subfolder")
	}
	_, err := a.svc.Set(a.ctx, a.owner, []manifest.Repo{
		{Path: "app", Remote: "github.com/acme/app"}, {Path: ".", Remote: "github.com/acme/mono"},
	})
	require.ErrorIs(t, err, manifest.ErrInvalid)
	got, err := a.svc.Get(a.ctx)
	require.NoError(t, err)
	assert.Nil(t, got, "nothing was saved")
}

func TestSetRoute_RefusesARootRepoWith422(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	rec := call(a.server(a.owner), http.MethodPut, "/api/workspace/manifest", `{"repos":[{"path":".","remote":"github.com/acme/mono"}]}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
}

func TestEditorApply_RefusesARootRepo(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	editor := manifest.NewEditor(manifest.NewPostgresStore(e.database))
	err := e.database.InTx(a.ctx, func(q db.Querier) error {
		if _, err := editor.LockRevision(a.ctx, q); err != nil {
			return err
		}
		_, err := editor.Apply(a.ctx, q, a.owner.ID, 0, []manifest.Repo{{Path: ".", Remote: "github.com/acme/mono"}})
		return err
	})
	require.ErrorIs(t, err, manifest.ErrInvalid)
}

func TestMove_RefusesMovingARepoToTheRoot(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	a.adopt(manifest.Repo{Path: "app", Remote: "github.com/acme/app"})
	_, err := a.svc.Move(a.ctx, a.owner, manifest.MoveRequest{From: "app", To: "."}, false)
	require.ErrorIs(t, err, manifest.ErrInvalid)
}
