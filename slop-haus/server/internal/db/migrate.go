package db

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/lowaa/slop-factory/go-db/migrations"
)

// Migrate applies every pending migration in fsys. It runs as the owning
// role, never the app role: creating tables and policies needs ownership,
// and the role that owns the tables is one RLS does not bind.
func Migrate(ctx context.Context, d *DB, fsys fs.FS) error {
	if err := migrations.Up(ctx, d.eng, fsys); err != nil {
		return fmt.Errorf("db: migrate up: %w", err)
	}
	return nil
}

// MigrateDown rolls back the most recent migration. Used by migration
// verification tests -- a down that is written but never executed is a
// down that does not work.
func MigrateDown(ctx context.Context, d *DB, fsys fs.FS) error {
	if err := migrations.Down(ctx, d.eng, fsys, 1); err != nil {
		return fmt.Errorf("db: migrate down: %w", err)
	}
	return nil
}

// Version reports the currently applied migration version.
func Version(ctx context.Context, d *DB, fsys fs.FS) (int64, error) {
	version, err := migrations.Version(ctx, d.eng, fsys)
	if err != nil {
		return 0, fmt.Errorf("db: get version: %w", err)
	}
	return int64(version), nil
}

// EnsureAppRole creates the role the server serves requests as, and grants
// it the tables. It is a separate role from the one that ran the
// migrations because row level security -- even FORCEd -- does not apply
// to a table's owner or to a superuser: served as the owner, every tenant
// could read every other's rows.
func EnsureAppRole(ctx context.Context, owner *DB, role, password string) error {
	if err := migrations.EnsureRole(ctx, owner.eng, role, password); err != nil {
		return fmt.Errorf("db: ensure app role: %w", err)
	}
	return nil
}
