package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadSupervisorPackageMissingDirIsNoPackage(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.DiscardHandler)
	assert.Nil(t, loadSupervisorPackage(t.TempDir()+"/absent", logger))
}

func TestLoadSupervisorPackageReadsAShippedDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for name, body := range map[string]string{"pyproject.toml": "x", "uv.lock": "y", "src/a.py": "z"} {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}
	pkg := loadSupervisorPackage(dir, slog.New(slog.DiscardHandler))
	require.NotNil(t, pkg)
	assert.NotEmpty(t, pkg.Version)
}
