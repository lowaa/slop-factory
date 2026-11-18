package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/supervisorpkg"
)

func TestShippedPackageVersionIsTheVersionTheHandlerServes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	for name, body := range map[string]string{"pyproject.toml": "[project]\n", "uv.lock": "", "src/a.py": "x = 1\n"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}
	pkg, err := supervisorpkg.Load(dir)
	require.NoError(t, err)

	assert.Equal(t, pkg.Version, shippedPackageVersion(dir))
	assert.NotEmpty(t, shippedPackageVersion(dir))
}

func TestShippedPackageVersionIsEmptyWhenTheServerShipsNone(t *testing.T) {
	t.Parallel()
	assert.Empty(t, shippedPackageVersion(filepath.Join(t.TempDir(), "missing")))
}
