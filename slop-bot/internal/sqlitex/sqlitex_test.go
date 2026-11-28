package sqlitex_test

import (
	"context"
	"database/sql"
	"embed"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
	"time"

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

// A write transaction queues behind another here, rather than at SQLite's
// lock, where under load it could wait out busy_timeout and fail.
func TestAWriteTransactionQueuesBehindAnother(t *testing.T) {
	t.Parallel()
	db := sqlitextest.Open(t, migrations(t))
	// A safety net, never a synchronisation.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queued := make(chan struct{})
	sqlitex.OnWriterWait(db, func() { close(queued) })

	release, inFirst := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- db.RunInTx(ctx, nil, func(ctx context.Context, tx bunTx) error {
			if _, err := tx.NewRaw("INSERT INTO widgets (name) VALUES ('first')").Exec(ctx); err != nil {
				return err
			}
			close(inFirst)
			<-release
			return nil
		})
	}()
	<-inFirst
	second := make(chan error, 1)
	go func() {
		second <- db.RunInTx(ctx, nil, func(ctx context.Context, tx bunTx) error {
			_, err := tx.NewRaw("INSERT INTO widgets (name) VALUES ('second')").Exec(ctx)
			return err
		})
	}()
	select {
	case <-queued:
	case <-ctx.Done():
		require.FailNow(t, "the second write never queued")
	}
	// A read-only transaction doesn't queue: it reads beside the writer.
	require.NoError(t, db.RunInTx(ctx, &sql.TxOptions{ReadOnly: true}, func(ctx context.Context, tx bunTx) error {
		var n int
		return tx.NewRaw("SELECT count(*) FROM widgets").Scan(ctx, &n)
	}))
	close(release)
	require.NoError(t, <-first)
	require.NoError(t, <-second)
	var names []string
	require.NoError(t, db.NewRaw("SELECT name FROM widgets ORDER BY id").Scan(ctx, &names))
	assert.Equal(t, []string{"first", "second"}, names)
}

func TestAQueuedWriteEndsWithItsContext(t *testing.T) {
	t.Parallel()
	db := sqlitextest.Open(t, migrations(t))
	ctx := context.Background()
	release, inFirst := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- db.RunInTx(ctx, nil, func(context.Context, bunTx) error {
			close(inFirst)
			<-release
			return nil
		})
	}()
	<-inFirst
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	err := db.RunInTx(cancelled, nil, func(context.Context, bunTx) error { return nil })
	require.ErrorIs(t, err, context.Canceled)
	close(release)
	require.NoError(t, <-first)
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

// The path goes into a file: URI, where ?, # and %XX mean something: a
// home directory holding them must still open the file named, with the
// pragmas applied.
func TestAPathWithURICharactersOpensThatFile(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "a#b?c%20d e")
	path := filepath.Join(dir, "x.db")
	db, err := open(t, path, migrations(t))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	var fk int
	require.NoError(t, db.NewRaw("PRAGMA foreign_keys").Scan(context.Background(), &fk))
	assert.Equal(t, 1, fk, "the pragmas weren't lost to the path")
	assert.FileExists(t, path)
	entries, err := os.ReadDir(filepath.Dir(dir))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "nothing opened beside it, at a truncated path")
}

func TestDurableSetsSynchronousFull(t *testing.T) {
	t.Parallel()
	for durable, want := range map[bool]int{false: 1, true: 2} { // NORMAL=1, FULL=2
		db, err := sqlitex.Open(context.Background(), &sqlitex.Options{
			Path: filepath.Join(t.TempDir(), "x.db"), Migrations: migrations(t),
			Logger: slog.New(slog.DiscardHandler), Durable: durable,
		})
		require.NoError(t, err)
		var got int
		require.NoError(t, db.NewRaw("PRAGMA synchronous").Scan(context.Background(), &got))
		assert.Equal(t, want, got, "durable=%v", durable)
		require.NoError(t, db.Close())
	}
}
