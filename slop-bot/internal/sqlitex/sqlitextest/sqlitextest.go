// Package sqlitextest gives each test a database file of its own, so tests
// against real SQLite can run in parallel.
package sqlitextest

import (
	"context"
	"io/fs"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/sqlitex"
)

// Open migrates a fresh database under t.TempDir() and closes it when the
// test ends.
func Open(t testing.TB, migrations fs.FS) *sqlitex.DB {
	t.Helper()
	db, err := sqlitex.Open(context.Background(), &sqlitex.Options{
		Path:       filepath.Join(t.TempDir(), "test.db"),
		Migrations: migrations,
		Logger:     slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}
