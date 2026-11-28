// Package sqlitex is slop-bot's wrapper over SQLite: it opens a database
// file with the pragmas every slop-bot database needs, runs that database's
// embedded goose migrations, and hands back a bun handle. state.db and the
// local tickets db both go through it, so the rules below hold for both.
package sqlitex

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"

	"github.com/pressly/goose/v3"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"

	// The pure-Go driver: no cgo, so the binary cross-compiles for macOS.
	_ "modernc.org/sqlite"
)

// ErrNewerSchema is returned when the file was migrated by a newer slop-bot
// than this one. Running on would mean writing through a schema this binary
// doesn't know, so Open refuses instead.
var ErrNewerSchema = errors.New("sqlitex: database schema is newer than this binary")

// busyTimeoutMillis is how long a connection waits on another's write lock
// before failing with SQLITE_BUSY. The daemon is the only writer, and its
// write transactions queue in RunInTx, so this only covers its writes
// outside a transaction, and another process's.
const busyTimeoutMillis = 5000

// maxOpenConns bounds the pool. WAL lets readers run beside the one writer;
// more connections than this buys nothing for a single-process daemon.
const maxOpenConns = 4

// Options configures Open.
type Options struct {
	// Path is the database file. Its directory is created (0700) if missing.
	Path string
	// Migrations holds the goose .sql files at its root.
	Migrations fs.FS
	// Logger receives migration progress. Required.
	Logger *slog.Logger
	// Durable syncs every commit to disk (synchronous=FULL), for user data:
	// a commit the event feed has already delivered must survive a power
	// loss. Operational state can rebuild, so it keeps the faster NORMAL.
	Durable bool
}

// DB is an open, migrated database. Close it when done.
type DB struct {
	*bun.DB
	sql *sql.DB
	// writer admits one write transaction at a time (see RunInTx).
	writer chan struct{}
	// waiting, when set, is called by a write transaction about to queue
	// behind another: the test's proof that it queued here.
	waiting func()
}

// RunInTx is bun's RunInTx, with write transactions queued here, one at a
// time. SQLite admits one writer at a time whatever this does; left to
// busy_timeout, concurrent writers poll for its lock, unfairly, and under
// load one can wait out the timeout and fail with SQLITE_BUSY however
// briefly each holds the lock. Queued here, they take turns in order, for
// as long as their ctx allows. A write outside a transaction, or from
// another process, still meets busy_timeout. fn must not start another
// write transaction on d: it would wait for itself.
func (d *DB) RunInTx(ctx context.Context, opts *sql.TxOptions, fn func(ctx context.Context, tx bun.Tx) error) error {
	if opts == nil || !opts.ReadOnly {
		if err := d.takeWriter(ctx); err != nil {
			return err
		}
		defer func() { <-d.writer }()
	}
	return d.DB.RunInTx(ctx, opts, fn)
}

func (d *DB) takeWriter(ctx context.Context) error {
	select {
	case d.writer <- struct{}{}:
		return nil
	default:
	}
	if d.waiting != nil {
		d.waiting()
	}
	select {
	case d.writer <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("sqlitex: waiting to write: %w", ctx.Err())
	}
}

// Open opens (creating if needed) the database at opts.Path, applies any
// pending migrations from opts.Migrations, and refuses a database migrated
// past what opts.Migrations knows.
func Open(ctx context.Context, opts *Options) (*DB, error) {
	if opts == nil || opts.Path == "" || opts.Migrations == nil || opts.Logger == nil {
		return nil, errors.New("sqlitex: Open needs Path, Migrations and Logger")
	}
	if err := os.MkdirAll(filepath.Dir(opts.Path), 0o700); err != nil {
		return nil, fmt.Errorf("sqlitex: creating %s: %w", filepath.Dir(opts.Path), err)
	}
	sqldb, err := sql.Open("sqlite", dsn(opts.Path, opts.Durable))
	if err != nil {
		return nil, fmt.Errorf("sqlitex: opening %s: %w", opts.Path, err)
	}
	sqldb.SetMaxOpenConns(maxOpenConns)
	db := &DB{DB: bun.NewDB(sqldb, sqlitedialect.New()), sql: sqldb, writer: make(chan struct{}, 1)}
	if err := migrate(ctx, sqldb, opts); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// Close releases the pool.
func (d *DB) Close() error { return d.DB.Close() }

// dsn sets the pragmas on every connection the pool opens, not just the
// first: foreign keys and busy_timeout are per connection in SQLite.
// _txlock=immediate takes the write lock at BEGIN, so two write transactions
// queue on busy_timeout instead of one failing at its first write.
func dsn(path string, durable bool) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMillis))
	sync := "NORMAL"
	if durable {
		sync = "FULL"
	}
	q.Add("_pragma", "synchronous("+sync+")")
	q.Set("_txlock", "immediate")
	return "file:" + path + "?" + q.Encode()
}

func migrate(ctx context.Context, sqldb *sql.DB, opts *Options) error {
	p, err := goose.NewProvider(goose.DialectSQLite3, sqldb, opts.Migrations,
		goose.WithDisableGlobalRegistry(true),
		goose.WithSlog(opts.Logger),
	)
	if err != nil {
		return fmt.Errorf("sqlitex: loading migrations: %w", err)
	}
	current, err := p.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("sqlitex: reading schema version of %s: %w", opts.Path, err)
	}
	if latest := latestVersion(p); current > latest {
		return fmt.Errorf("%w: %s is at version %d, this binary knows up to %d",
			ErrNewerSchema, opts.Path, current, latest)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("sqlitex: migrating %s: %w", opts.Path, err)
	}
	return nil
}

func latestVersion(p *goose.Provider) int64 {
	var latest int64
	for _, s := range p.ListSources() {
		latest = max(latest, s.Version)
	}
	return latest
}
