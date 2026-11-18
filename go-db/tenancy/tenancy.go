// Package tenancy makes it impossible to query a tenant-scoped table
// without going through a tenant-context-carrying transaction. TenantTx is
// the only such carrier, and there is exactly one way to construct one.
package tenancy

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/go-db/engine"
)

type tenantIDContextKey struct{}

// ContextWithTenantID stores the resolved tenant ID on ctx. Tenant identity
// resolution happens at the edges of the system (HTTP middleware reading a
// JWT claim/subdomain/header, or a background job looping over tenants) --
// this function is where that resolved ID is handed off to the rest of the
// request/job's call chain.
func ContextWithTenantID(ctx context.Context, tenantID uuid.UUID) context.Context {
	return context.WithValue(ctx, tenantIDContextKey{}, tenantID)
}

// TenantIDFromContext returns the tenant ID stored on ctx, and false if none
// is set. Callers at the edge of the system should fail closed (reject the
// request) when this returns false, rather than proceeding with a default
// tenant.
func TenantIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(tenantIDContextKey{}).(uuid.UUID)
	return id, ok
}

// TenantTx wraps an engine.Tx that has had its Postgres session scoped to a
// single tenant for the lifetime of the transaction, so every query issued
// through it is subject to that tenant's RLS policies. It satisfies
// engine.Querier (and engine.Tx), so repository code written against
// engine.Querier doesn't need to know it's talking to a tenant-scoped
// transaction rather than a raw one -- the type itself is the signal.
//
// There is no constructor that skips the tenant-scoping step: NewTenantTx is
// the only way to obtain one.
type TenantTx struct {
	tx       engine.Tx
	tenantID uuid.UUID
}

var (
	_ engine.Querier = (*TenantTx)(nil)
	_ engine.Tx      = (*TenantTx)(nil)
)

// NewTenantTx begins a transaction on eng and scopes it to tenantID by
// setting the app.current_tenant session variable for the transaction's
// lifetime, before returning.
//
// This uses set_config('app.current_tenant', $1, true) rather than the more
// obvious `SET LOCAL app.current_tenant = $1`: SET/SET LOCAL are not regular
// statements in Postgres's grammar -- the value after "=" must be a literal
// or identifier, not a bind parameter -- so `$1` there would be a syntax
// error, not a parameterized value. set_config is a normal function call
// that does accept a real bind parameter for the tenant ID (no string
// formatting/interpolation, no injection risk), and its third argument
// (true) gives it the same transaction-local scope SET LOCAL would have.
func NewTenantTx(ctx context.Context, eng engine.Engine, tenantID uuid.UUID) (*TenantTx, error) {
	return NewTenantTxWithOptions(ctx, eng, tenantID, engine.TxOptions{})
}

// NewTenantTxWithOptions is NewTenantTx for a transaction that needs a
// particular isolation level or read-only access -- a consistent snapshot
// across several reads, say. The options are applied when the transaction
// begins, before the tenant is set, because Postgres will not accept them
// once any statement has run.
func NewTenantTxWithOptions(ctx context.Context, eng engine.Engine, tenantID uuid.UUID, opts engine.TxOptions) (*TenantTx, error) {
	tx, err := eng.BeginTxWithOptions(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("tenancy: begin tx: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenantID.String()); err != nil {
		_ = tx.Rollback(ctx)
		return nil, fmt.Errorf("tenancy: set tenant context: %w", err)
	}

	return &TenantTx{tx: tx, tenantID: tenantID}, nil
}

// TenantID returns the tenant this transaction is scoped to.
func (t *TenantTx) TenantID() uuid.UUID {
	return t.tenantID
}

func (t *TenantTx) ExecContext(ctx context.Context, query string, args ...any) (engine.Result, error) {
	return t.tx.ExecContext(ctx, query, args...)
}

func (t *TenantTx) QueryContext(ctx context.Context, query string, args ...any) (engine.Rows, error) {
	return t.tx.QueryContext(ctx, query, args...)
}

func (t *TenantTx) QueryRowContext(ctx context.Context, query string, args ...any) engine.Row {
	return t.tx.QueryRowContext(ctx, query, args...)
}

func (t *TenantTx) Commit(ctx context.Context) error {
	return t.tx.Commit(ctx)
}

func (t *TenantTx) Rollback(ctx context.Context) error {
	return t.tx.Rollback(ctx)
}
