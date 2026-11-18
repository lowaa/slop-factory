// Package db is slop-haus's wrapper over go-db: every tenant-scoped query
// runs in a transaction scoped to the request's tenant, and the few that
// must run before a tenant is known go through an explicitly global handle.
//
// Store code is written against Querier -- pgx's own method names and
// types, which go-db's engine re-exports -- so the stores never import
// go-db, and a transaction and a one-off statement look the same to them.
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/lowaa/slop-factory/go-db/engine"
	"github.com/lowaa/slop-factory/go-db/tenancy"
)

// Querier is the query surface store code is written against.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ErrNoTenant means a tenant-scoped query was made on a context carrying no
// tenant. That is a wiring bug -- a route mounted outside the middleware
// that resolves one -- never a caller error, so it fails loudly rather than
// falling back to anything.
var ErrNoTenant = errors.New("db: no tenant on the context")

// WithTenant scopes ctx to a tenant. Authentication calls it once it knows
// who the request is for; everything downstream of that is scoped.
func WithTenant(ctx context.Context, tenantID uuid.UUID) context.Context {
	return tenancy.ContextWithTenantID(ctx, tenantID)
}

// TenantFrom returns the tenant ctx is scoped to.
func TenantFrom(ctx context.Context) (uuid.UUID, error) {
	id, ok := tenancy.TenantIDFromContext(ctx)
	if !ok {
		return uuid.Nil, ErrNoTenant
	}
	return id, nil
}

// DB owns the connection pool. Used directly it is a Querier whose every
// statement runs in its own transaction scoped to the context's tenant.
type DB struct {
	eng *engine.Postgres
}

var _ Querier = (*DB)(nil)

// New connects and verifies it can reach the server, so a bad DSN or an
// unreachable database fails at startup rather than on the first request.
func New(ctx context.Context, dsn string) (*DB, error) {
	eng := engine.NewPostgres(dsn)
	if err := eng.Connect(ctx); err != nil {
		return nil, fmt.Errorf("db: connect: %w", err)
	}
	if err := eng.Ping(ctx); err != nil {
		_ = eng.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return &DB{eng: eng}, nil
}

// Close releases every pooled connection. Safe to call more than once.
func (d *DB) Close() { _ = d.eng.Close() }

func (d *DB) Ping(ctx context.Context) error { return d.eng.Ping(ctx) }

// Engine exposes go-db's engine for migrations and role management, which
// take it directly.
func (d *DB) Engine() engine.Engine { return d.eng }

// Exec runs one statement in its own tenant-scoped transaction.
func (d *DB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	var tag pgconn.CommandTag
	err := d.InTx(ctx, func(q Querier) error {
		var err error
		tag, err = q.Exec(ctx, sql, args...)
		return err
	})
	return tag, err
}

// Query runs one statement in its own tenant-scoped transaction. The rows
// are read after this returns, so the transaction stays open until they
// are closed -- committing if they were read cleanly, since the statement
// may have been a write with RETURNING.
func (d *DB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	tx, err := d.begin(ctx, engine.TxOptions{})
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, sql, args...)
	if err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		return nil, err
	}
	return &txRows{Rows: rows, ctx: ctx, tx: tx}, nil
}

// QueryRow runs one statement in its own tenant-scoped transaction, which
// ends when the row is scanned -- committed if the scan succeeded, since an
// INSERT ... RETURNING comes through here too.
func (d *DB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx, err := d.begin(ctx, engine.TxOptions{})
	if err != nil {
		return errRow{err: err}
	}
	return &txRow{row: tx.QueryRowContext(ctx, sql, args...), ctx: ctx, tx: tx}
}

// InTx runs fn inside a transaction scoped to the context's tenant,
// committing if it returns nil and rolling back otherwise.
//
// The rollback is synchronous and deferred, never spawned into a goroutine:
// a transaction left open after this returns holds its pooled connection
// for an unbounded time, and a burst of constraint failures can then starve
// the pool. context.WithoutCancel so the rollback still runs when the
// caller's context is what failed.
//
// This one is enforced by review, not by a test, and deliberately so: three
// attempts to catch an async rollback all passed with the bug present,
// because a local rollback completes in microseconds and the goroutine only
// loses the race under the load that makes it a problem. Don't "fix" a
// failing test here by making this asynchronous.
func (d *DB) InTx(ctx context.Context, fn func(q Querier) error) error {
	return d.inTx(ctx, engine.TxOptions{}, fn)
}

// InReadTx runs fn inside a read-only, repeatable-read transaction scoped to
// the context's tenant, so every statement in it sees the same snapshot.
//
// InTx is not a substitute: read committed takes a fresh snapshot per
// statement, so a multi-statement read through it can splice together two
// instants. Read-only is declared rather than merely intended, so a stray
// write fails instead of being left to review.
func (d *DB) InReadTx(ctx context.Context, fn func(q Querier) error) error {
	return d.inTx(ctx, engine.TxOptions{Isolation: engine.IsolationRepeatableRead, ReadOnly: true}, fn)
}

func (d *DB) inTx(ctx context.Context, opts engine.TxOptions, fn func(q Querier) error) error {
	tx, err := d.begin(ctx, opts)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	if err := fn(querier{q: tx}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("db: commit: %w", err)
	}
	committed = true
	return nil
}

func (d *DB) begin(ctx context.Context, opts engine.TxOptions) (*tenancy.TenantTx, error) {
	tenantID, err := TenantFrom(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := tenancy.NewTenantTxWithOptions(ctx, d.eng, tenantID, opts)
	if err != nil {
		return nil, fmt.Errorf("db: begin: %w", err)
	}
	return tx, nil
}

// Global is the handle for the global_ tables -- identity, tenancy,
// credentials -- which have to be readable before any tenant is known.
// It carries no tenant, so tenant-scoped tables read through it are empty:
// reaching one from here is a bug that shows up as missing data, not as
// another tenant's.
func (d *DB) Global() Querier { return querier{q: d.eng} }

// InGlobalTx runs fn in a transaction with no tenant, for multi-statement
// writes to global_ tables. Anything that also writes a tenant's rows --
// creating a tenant's first data, say -- belongs in InTx on a context
// scoped to that tenant instead; global_ tables are writable from there
// too.
func (d *DB) InGlobalTx(ctx context.Context, fn func(q Querier) error) error {
	tx, err := d.eng.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("db: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	if err := fn(querier{q: tx}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("db: commit: %w", err)
	}
	committed = true
	return nil
}

// querier adapts go-db's Querier (database/sql-style method names) to this
// package's (pgx's names). The types underneath are pgx's either way.
type querier struct {
	q engine.Querier
}

func (q querier) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return q.q.ExecContext(ctx, sql, args...)
}

func (q querier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return q.q.QueryContext(ctx, sql, args...)
}

func (q querier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return q.q.QueryRowContext(ctx, sql, args...)
}

// txRows ends its transaction as soon as the rows run out, or when they
// are closed if that comes first. At the end of iteration rather than only
// at Close because that is where callers look for errors: a commit that
// fails -- a deferred constraint under an INSERT ... RETURNING -- is
// reported by Err, which a caller checks after the loop, where one
// swallowed by Close would read as success.
type txRows struct {
	pgx.Rows
	ctx       context.Context
	tx        *tenancy.TenantTx
	finished  bool
	commitErr error
}

func (r *txRows) Next() bool {
	if r.finished {
		return false
	}
	if r.Rows.Next() {
		return true
	}
	r.finish()
	return false
}

func (r *txRows) Err() error {
	if err := r.Rows.Err(); err != nil {
		return err
	}
	return r.commitErr
}

func (r *txRows) Close() {
	r.finish()
}

// finish closes the rows and ends the transaction, once: committed if the
// rows were read cleanly, since the statement may have been a write with
// RETURNING, and rolled back otherwise.
func (r *txRows) finish() {
	if r.finished {
		return
	}
	r.finished = true
	r.Rows.Close()
	if r.Rows.Err() != nil {
		_ = r.tx.Rollback(context.WithoutCancel(r.ctx))
		return
	}
	if err := r.tx.Commit(context.WithoutCancel(r.ctx)); err != nil {
		r.commitErr = fmt.Errorf("db: commit: %w", err)
	}
}

// txRow ends its transaction when the row is scanned.
type txRow struct {
	row pgx.Row
	ctx context.Context
	tx  *tenancy.TenantTx
}

func (r *txRow) Scan(dest ...any) error {
	err := r.row.Scan(dest...)
	if err != nil {
		_ = r.tx.Rollback(context.WithoutCancel(r.ctx))
		return err
	}
	if commitErr := r.tx.Commit(context.WithoutCancel(r.ctx)); commitErr != nil {
		return fmt.Errorf("db: commit: %w", commitErr)
	}
	return nil
}

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }
