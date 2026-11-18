package supervisorpkg_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/supervisorpkg"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	return dir
}

func shipped() map[string]string {
	return map[string]string{
		"pyproject.toml":                         "[project]\n",
		"uv.lock":                                "lock\n",
		"src/slop_haus_supervisor/cli.py":        "A = 1\n",
		"src/slop_haus_supervisor/x/__init__.py": "",
		// Never shipped:
		"tests/test_x.py": "def test(): ...\n",
		"test.sh":         "#!/bin/sh\n",
		"src/slop_haus_supervisor/__pycache__/cli.pyc": "\x00",
		"src/slop_haus_supervisor/stale.pyc":           "\x00",
	}
}

func members(t *testing.T, archive []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	require.NoError(t, err)
	out := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		require.NoError(t, err)
		body, err := io.ReadAll(tr)
		require.NoError(t, err)
		out[h.Name] = string(body)
	}
}

func TestLoadShipsWhatRunningNeedsAndNothingElse(t *testing.T) {
	pkg, err := supervisorpkg.Load(writeTree(t, shipped()))
	require.NoError(t, err)

	assert.Equal(t, map[string]string{
		"pyproject.toml":                         "[project]\n",
		"uv.lock":                                "lock\n",
		"src/slop_haus_supervisor/cli.py":        "A = 1\n",
		"src/slop_haus_supervisor/x/__init__.py": "",
	}, members(t, pkg.Archive))
}

func TestLoadIsDeterministicAndTheVersionFollowsTheContent(t *testing.T) {
	dir := writeTree(t, shipped())
	first, err := supervisorpkg.Load(dir)
	require.NoError(t, err)
	again, err := supervisorpkg.Load(dir)
	require.NoError(t, err)
	assert.Equal(t, first.Archive, again.Archive)
	assert.Equal(t, first.Version, again.Version)

	// A file that is not shipped does not move it; one that is does.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tests", "test_x.py"), []byte("changed"), 0o644))
	same, err := supervisorpkg.Load(dir)
	require.NoError(t, err)
	assert.Equal(t, first.Version, same.Version)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "slop_haus_supervisor", "cli.py"), []byte("A = 2\n"), 0o644))
	changed, err := supervisorpkg.Load(dir)
	require.NoError(t, err)
	assert.NotEqual(t, first.Version, changed.Version)
}

func TestLoadRefusesAnIncompleteOrUnusualDirectory(t *testing.T) {
	missingLock := shipped()
	delete(missingLock, "uv.lock")
	_, err := supervisorpkg.Load(writeTree(t, missingLock))
	assert.Error(t, err, "uv run --frozen needs the lock")

	_, err = supervisorpkg.Load(filepath.Join(t.TempDir(), "absent"))
	assert.Error(t, err)

	linked := writeTree(t, shipped())
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(linked, "src", "slop_haus_supervisor", "link.py")))
	_, err = supervisorpkg.Load(linked)
	assert.Error(t, err, "a link would ship whatever it points at")
}

func newAPI(t *testing.T, pkg *supervisorpkg.Package) humatest.TestAPI {
	t.Helper()
	return newAPIWithName(t, pkg, func(context.Context, *authn.User) (string, error) { return "Acme Corp", nil })
}

func newAPIWithName(t *testing.T, pkg *supervisorpkg.Package, name supervisorpkg.TenantName) humatest.TestAPI {
	t.Helper()
	_, api := humatest.New(t)
	api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		next(huma.WithContext(ctx, authn.NewUserContext(ctx.Context(), &authn.User{ID: uuid.New(), TenantID: tenantID})))
	})
	h := supervisorpkg.NewHandler(pkg, func(context.Context) (string, error) { return "https://slop-haus.example", nil }, name, slog.New(slog.DiscardHandler))
	supervisorpkg.RegisterRoutes(api, h, nil)
	return api
}

var tenantID = uuid.MustParse("11111111-2222-3333-4444-555555555555")

func TestRoutesServeThePackageWithItsVersion(t *testing.T) {
	pkg, err := supervisorpkg.Load(writeTree(t, shipped()))
	require.NoError(t, err)
	api := newAPI(t, pkg)

	resp := api.Get("/api/supervisor/package")

	require.Equal(t, http.StatusOK, resp.Code)
	assert.Equal(t, pkg.Version, resp.Header().Get(supervisorpkg.VersionHeader))
	assert.Equal(t, "application/gzip", resp.Header().Get("Content-Type"))
	assert.Equal(t, pkg.Archive, resp.Body.Bytes())
}

func TestRoutesServeASetupScriptForTheCallersTenant(t *testing.T) {
	pkg, err := supervisorpkg.Load(writeTree(t, shipped()))
	require.NoError(t, err)
	api := newAPI(t, pkg)

	resp := api.Get("/api/supervisor/setup_script")

	require.Equal(t, http.StatusOK, resp.Code)
	assert.Contains(t, resp.Body.String(), "server='https://slop-haus.example'")
	assert.Contains(t, resp.Body.String(), "tenant_id='"+tenantID.String()+"'")
	assert.Equal(t, "no-store", resp.Header().Get("Cache-Control"))
}

func TestSetupScriptCarriesTheSluggedTenantName(t *testing.T) {
	pkg, err := supervisorpkg.Load(writeTree(t, shipped()))
	require.NoError(t, err)

	resp := newAPI(t, pkg).Get("/api/supervisor/setup_script")

	require.Equal(t, http.StatusOK, resp.Code)
	assert.Contains(t, resp.Body.String(), "tenant_name='acme-corp'")
}

// The name is a suggestion: a failed read must not stop someone setting up.
func TestSetupScriptStillServedWhenTheTenantNameCannotBeRead(t *testing.T) {
	pkg, err := supervisorpkg.Load(writeTree(t, shipped()))
	require.NoError(t, err)
	api := newAPIWithName(t, pkg, func(context.Context, *authn.User) (string, error) { return "", errors.New("db down") })

	resp := api.Get("/api/supervisor/setup_script")

	require.Equal(t, http.StatusOK, resp.Code)
	assert.Contains(t, resp.Body.String(), "tenant_name=''")
}

func TestRoutesAnswer404WhereTheServerShipsNoSupervisor(t *testing.T) {
	api := newAPI(t, nil)

	assert.Equal(t, http.StatusNotFound, api.Get("/api/supervisor/package").Code)
	assert.Equal(t, http.StatusNotFound, api.Get("/api/supervisor/setup_script").Code)
}

// The package is for the updater, which is an agent; the script is for a
// person. Swapping them would hand a worker's credential the script or
// a visitor the package.
func TestRoutesAreAuthenticatedAsIntended(t *testing.T) {
	api := newAPI(t, nil)

	paths := api.OpenAPI().Paths
	assert.Equal(t, authn.RequireAgent(), paths["/api/supervisor/package"].Get.Metadata)
	assert.Equal(t, authn.RequireUser(), paths["/api/supervisor/setup_script"].Get.Metadata)
}

// The real directory, as the image carries it: a rename or a missing lock in
// slop-haus/supervisor fails here rather than at the first setup.
func TestLoadsTheRepoSupervisorPackage(t *testing.T) {
	pkg, err := supervisorpkg.Load(filepath.Join("..", "..", "..", "supervisor"))
	require.NoError(t, err)

	files := members(t, pkg.Archive)
	for _, want := range []string{"pyproject.toml", "uv.lock", "src/slop_haus_supervisor/cli.py", "src/slop_haus_supervisor/__main__.py"} {
		assert.Contains(t, files, want)
	}
	for name := range files {
		assert.NotContains(t, name, "tests/")
		assert.NotContains(t, name, "__pycache__")
	}
}
