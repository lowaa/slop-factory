package migrations

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/lowaa/slop-factory/go-db/engine"
)

// EnsureRole creates (if missing) a NOSUPERUSER/NOBYPASSRLS role and grants
// it read/write access to every table in the public schema, including
// tables created by migrations that haven't run yet (via ALTER DEFAULT
// PRIVILEGES).
//
// Sequences are granted alongside tables, not as an afterthought: inserting
// into a table with a serial/bigserial column also reads and advances that
// column's sequence, so a role holding INSERT but not USAGE on the sequence
// fails with "permission denied for sequence <table>_id_seq" on every
// insert. Schemas keyed entirely on uuid never notice, which is exactly why
// this is easy to leave out and confusing to debug when a schema finally
// uses a serial key.
//
// This matters everywhere RLS is expected to actually apply: Postgres row
// security -- even with FORCE ROW LEVEL SECURITY -- is unconditionally
// bypassed by superusers and by any role with the BYPASSRLS attribute. The
// role migrations themselves run as is typically a superuser (it needs to
// create tables/policies/roles), but that same role must never be the one
// application code -- or an RLS-dependent test -- connects as.
//
// The role name and password cannot be bind parameters -- CREATE ROLE and
// GRANT take none -- so the server quotes them, with format()'s %I and %L,
// and the statement it builds is then run. Quoting in Go instead depends on
// the session's standard_conforming_strings: with it off, a backslash in the
// password escapes the closing quote and the rest runs as the migrating
// superuser. %L knows the setting and writes an E” literal when it must.
//
// Replicas starting together all run this, and Postgres does not let two
// sessions rewrite the same catalog row at once: role DDL that finds
// another session's in-flight update of the schema ACL fails with "tuple
// concurrently updated" (XX000) instead of waiting and retrying, and two
// CREATE ROLEs collide on pg_authid's unique index. So the whole step runs
// in one transaction holding an advisory lock: the second replica waits for
// the first to commit, then finds the role there and re-grants it.
//
// The lock only orders EnsureRole against itself. Other statements rewrite
// the same rows without taking it -- TRUNCATE and ALTER TABLE rewrite a
// table's pg_class row, which GRANT ON ALL TABLES also rewrites while
// taking no lock on the table -- and the lock is per database while roles
// are cluster-wide, so the same role ensured from two databases still
// meets on pg_authid. Those conflicts can't be locked out from here, so a
// step that fails on one is rolled back and run again: it is idempotent,
// and on the next run it finds the other session's work committed.
//
// An existing role is checked, not trusted: one created by hand with
// SUPERUSER or BYPASSRLS would pass through here and quietly switch RLS off
// for every query the application makes.
func EnsureRole(ctx context.Context, eng engine.Engine, role, password string) error {
	if strings.ContainsRune(role, 0) || strings.ContainsRune(password, 0) {
		return fmt.Errorf("migrations: ensure role: NUL in role name or password")
	}

	for attempt := 1; ; attempt++ {
		err := ensureRoleOnce(ctx, eng, role, password)
		if err == nil || !isConcurrentCatalogUpdate(err) || attempt == ensureRoleAttempts {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (not retried: %w)", err, ctx.Err())
		case <-time.After(time.Duration(attempt) * ensureRoleRetryBackoff):
		}
	}
}

// ensureRoleAttempts bounds the retries: each conflict needs another
// session's catalog write to land inside this step's few milliseconds, so
// repeated ones mean something is rewriting the schema continuously, and
// that is better surfaced than waited out.
const (
	ensureRoleAttempts     = 5
	ensureRoleRetryBackoff = 50 * time.Millisecond
)

// isConcurrentCatalogUpdate reports whether err is Postgres refusing a
// catalog write because another session wrote the same row first (or a
// deadlock over such rows, where this session was the one aborted). XX000 is
// Postgres's generic internal-error code, so its message is matched too;
// it comes from an elog, which is never translated, so lc_messages can't
// change it.
// The unique-violation and duplicate-object cases can only come from
// CREATE ROLE here: another database's EnsureRole created the role between
// this one's look-up and its CREATE.
func isConcurrentCatalogUpdate(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "40P01": // deadlock_detected
		// Another session's statement took the catalog rows in the opposite
		// order to this step's grants (e.g. a migration, or index creation
		// in a binary starting alongside). Postgres aborts one side; if it
		// was this one, nothing here has committed, so running again is safe.
		return true
	case "XX000": // internal_error
		return pgErr.Message == "tuple concurrently updated"
	case "23505": // unique_violation
		return pgErr.ConstraintName == "pg_authid_rolname_index"
	case "42710": // duplicate_object
		return true
	}
	return false
}

func ensureRoleOnce(ctx context.Context, eng engine.Engine, role, password string) (err error) {
	tx, err := eng.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("migrations: ensure role %s: %w", role, err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(context.Background())
		}
	}()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, ensureRoleLockKey); err != nil {
		return fmt.Errorf("migrations: ensure role %s: lock: %w", role, err)
	}

	var exists, super, bypass bool
	err = tx.QueryRowContext(ctx,
		`SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = $1`, role).Scan(&super, &bypass)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return fmt.Errorf("migrations: ensure role %s: look up: %w", role, err)
	default:
		exists = true
	}
	if exists && (super || bypass) {
		return fmt.Errorf("migrations: ensure role %s: it exists with SUPERUSER or BYPASSRLS, so row level security would not apply to it -- alter or drop it", role)
	}
	if !exists {
		if err := execFormatted(ctx, tx,
			`SELECT format('CREATE ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOBYPASSRLS', $1::text, $2::text)`, role, password); err != nil {
			return fmt.Errorf("migrations: ensure role %s: create: %w", role, err)
		}
	}

	for _, grant := range []string{
		`GRANT USAGE ON SCHEMA public TO %I`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %I`,
		`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %I`,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %I`,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO %I`,
	} {
		if err := execFormatted(ctx, tx, `SELECT format('`+grant+`', $1::text)`, role); err != nil {
			return fmt.Errorf("migrations: ensure role %s: %w", role, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("migrations: ensure role %s: commit: %w", role, err)
	}
	return nil
}

// ensureRoleLockKey is the pg_advisory_xact_lock key EnsureRole serialises
// on. It is one key for every role, not one per role: different roles'
// grants still rewrite the same rows (the public schema's ACL, its
// default-ACL entries). Advisory locks are per database, as are those rows;
// pg_authid is not, and is left to EnsureRole's retry.
// The value is arbitrary; it only has to differ from other advisory lock
// keys used against the same database (goose's migration lock among them).
const ensureRoleLockKey int64 = 0x676f2d64625f726c // "go-db_rl"

// execFormatted asks the server to build a statement (a SELECT format(...)
// with the values bound as parameters) and then runs what it built.
func execFormatted(ctx context.Context, q engine.Querier, build string, args ...any) error {
	var stmt string
	if err := q.QueryRowContext(ctx, build, args...).Scan(&stmt); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, stmt)
	return err
}
