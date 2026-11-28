package sqlitex_test

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/sqlitex"
	"github.com/lowaa/slop-factory/slop-bot/internal/sqlitex/sqlitextest"
)

//go:embed testdata/migrations/*.sql
var migrationFiles embed.FS

func migrations(t *testing.T) fs.FS {
	t.Helper()
	sub, err := fs.Sub(migrationFiles, "testdata/migrations")
	require.NoError(t, err)
	return sub
}

func open(t *testing.T, path string, m fs.FS) (*sqlitex.DB, error) {
	t.Helper()
	return sqlitex.Open(context.Background(), &sqlitex.Options{
		Path: path, Migrations: m, Logger: slog.New(slog.DiscardHandler),
	})
}

func TestOpenMigratesAndCreatesTheDirectory(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "dir", "x.db")
	db, err := open(t, path, migrations(t))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	_, err = db.NewRaw("INSERT INTO widgets (name) VALUES (?)", "a").Exec(context.Background())
	require.NoError(t, err)
}

func TestForeignKeysAreEnforcedOnEveryConnection(t *testing.T) {
	t.Parallel()
	db := sqlitextest.Open(t, migrations(t))
	ctx := context.Background()

	// Several connections at once, so the pragma is checked beyond the
	// first connection the pool opened.
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Go(func() {
			_, errs[i] = db.NewRaw("INSERT INTO parts (widget_id) VALUES (?)", 999).Exec(ctx)
		})
	}
	wg.Wait()
	for _, err := range errs {
		assert.ErrorContains(t, err, "FOREIGN KEY")
	}
}

func TestOpenRefusesANewerSchema(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "x.db")
	newer := fstest.MapFS{
		"00001_widgets.sql": {Data: mustRead(t, "testdata/migrations/00001_widgets.sql")},
		"00002_more.sql":    {Data: []byte("-- +goose Up\nCREATE TABLE more (id INTEGER);\n-- +goose Down\nDROP TABLE more;\n")},
	}
	db, err := open(t, path, newer)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = open(t, path, migrations(t))
	assert.ErrorIs(t, err, sqlitex.ErrNewerSchema)
}

func TestOpenAppliesOnlyPendingMigrations(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "x.db")
	db, err := open(t, path, migrations(t))
	require.NoError(t, err)
	_, err = db.NewRaw("INSERT INTO widgets (name) VALUES ('kept')").Exec(context.Background())
	require.NoError(t, err)
	require.NoError(t, db.Close())

	db, err = open(t, path, migrations(t))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	var n int
	require.NoError(t, db.NewRaw("SELECT count(*) FROM widgets").Scan(context.Background(), &n))
	assert.Equal(t, 1, n)
}

func TestConcurrentWriteTransactionsQueueRatherThanFail(t *testing.T) {
	t.Parallel()
	db := sqlitextest.Open(t, migrations(t))
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make([]error, 16)
	for i := range errs {
		wg.Go(func() {
			errs[i] = db.RunInTx(ctx, nil, func(ctx context.Context, tx bunTx) error {
				var n int
				if err := tx.NewRaw("SELECT count(*) FROM widgets").Scan(ctx, &n); err != nil {
					return err
				}
				_, err := tx.NewRaw("INSERT INTO widgets (name) VALUES (?)", nameFor(i, n)).Exec(ctx)
				return err
			})
		})
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
}

func TestOpenValidatesOptions(t *testing.T) {
	t.Parallel()
	_, err := sqlitex.Open(context.Background(), &sqlitex.Options{Path: "x"})
	assert.Error(t, err)
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	b, err := migrationFiles.ReadFile(name)
	require.NoError(t, err)
	return b
}
