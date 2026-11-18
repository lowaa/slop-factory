// Package migrations is a thin goose wrapper. Migration files are the
// single source of schema truth: no parallel "desired schema" file, no ORM
// auto-migrate at runtime.
//
// This package is schema-agnostic by design -- go-db is a shared library,
// and the demo schema (internal/demo) is content built on top of it, not
// something this package should know about. Callers supply their own
// migration source as an fs.FS rooted directly at their migrations (an
// embed.FS via fs.Sub, typically), one file per migration, in goose's own
// format:
//
//	migrations/
//	  000001_init.sql
//	  000002_widgets.sql
//
// Each file carries both directions via goose's "-- +goose Up" / "-- +goose
// Down" annotations (see internal/demo/migrations for real examples);
// multi-statement bodies (a plpgsql function, say) need "-- +goose
// StatementBegin"/"StatementEnd" around them so goose's splitter doesn't cut
// on an internal semicolon. Version numbers must be sequential and gapless
// -- see the timestamp-vs-sequence discussion this package's design left in
// its wake: sequential numbers are what make "did we skip one" a checkable
// property at all, at the cost of an occasional renumber when two branches
// both add "the next" migration. A LATEST file beside the migrations names
// the newest one, so those two branches conflict in git (not later, in a
// failing test); SequenceLint.Lint checks the sequence and LATEST without a
// database.
//
// goose also supports migrations written as Go functions (goose.AddMigration
// et al.), which get a live *sql.Tx/*sql.DB to introspect the database
// before deciding what to do -- something a golang-migrate-style pure-SQL
// pipeline can't express at all. None of the current schemas need that yet,
// but it's why this package moved to goose over hand-rolling a competing
// migration runner: the capability was the whole point, not a shopping list
// feature.
package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver used below
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/lowaa/slop-factory/go-db/engine"
)

// newProvider opens a database/sql handle from the Engine's connection
// details (goose's driver machinery is database/sql-shaped) and wires it to
// migrationsFS. Callers must close the returned *sql.DB.
//
// A Postgres session-level advisory lock (rather than goose's alternative
// table-based locker) guards the migration run itself, so two instances
// starting up concurrently against the same database can't race each other
// -- the same guarantee golang-migrate provided via its own advisory lock,
// which a hand-rolled replacement would otherwise have had to reinvent.
func newProvider(eng engine.Engine, migrationsFS fs.FS) (*goose.Provider, *sql.DB, error) {
	db, err := sql.Open("pgx", eng.ConnString())
	if err != nil {
		return nil, nil, fmt.Errorf("migrations: open: %w", err)
	}

	sessionLocker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("migrations: session locker: %w", err)
	}

	p, err := goose.NewProvider(goose.DialectPostgres, db, migrationsFS, goose.WithSessionLocker(sessionLocker))
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("migrations: new provider: %w", err)
	}

	return p, db, nil
}

// Up runs all pending migrations from migrationsFS.
func Up(ctx context.Context, eng engine.Engine, migrationsFS fs.FS) error {
	p, db, err := newProvider(eng, migrationsFS)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("migrations: up: %w", err)
	}
	return nil
}

// Down rolls back the given number of migrations from migrationsFS, most
// recently applied first.
func Down(ctx context.Context, eng engine.Engine, migrationsFS fs.FS, steps int) error {
	p, db, err := newProvider(eng, migrationsFS)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	for i := 0; i < steps; i++ {
		if _, err := p.Down(ctx); err != nil {
			return fmt.Errorf("migrations: down: %w", err)
		}
	}
	return nil
}

// Version reports the current schema version. A never-migrated database
// reports version 0.
func Version(ctx context.Context, eng engine.Engine, migrationsFS fs.FS) (version uint, err error) {
	p, db, err := newProvider(eng, migrationsFS)
	if err != nil {
		return 0, err
	}
	defer func() { _ = db.Close() }()

	v, err := p.GetDBVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("migrations: version: %w", err)
	}
	return uint(v), nil
}
