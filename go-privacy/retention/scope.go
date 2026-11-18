package retention

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/go-db/engine"
	"github.com/lowaa/slop-factory/go-db/tenancy"
)

// Scopes says where a Sweeper runs: which scopes there are, and how to run
// one batch inside one. A scope is a unit the database isolates, so what a
// batch can see (and delete) is only that scope's rows.
type Scopes interface {
	// List returns the scopes to sweep, in the order to sweep them.
	List(ctx context.Context) ([]uuid.UUID, error)
	// Tx runs fn in a transaction scoped to id, and commits it if fn
	// returns nil; anything else rolls it back.
	Tx(ctx context.Context, id uuid.UUID, fn func(q engine.Querier) error) error
}

// TenantScopes sweeps each tenant in a tenant-scoped transaction on the
// app's own role, so row-level security bounds a batch to the tenant it is
// running for, however the rule's predicate is written.
type TenantScopes struct {
	eng engine.Engine
	// listTenants discovers the tenants. Sweeping has no request to inherit
	// a tenant from, so the app reads its tenant registry (a global table).
	listTenants func(ctx context.Context) ([]uuid.UUID, error)
}

// NewTenantScopes sweeps through eng, which must connect as the app's role
// (not one that bypasses row-level security).
func NewTenantScopes(eng engine.Engine, listTenants func(ctx context.Context) ([]uuid.UUID, error)) *TenantScopes {
	return &TenantScopes{eng: eng, listTenants: listTenants}
}

func (s *TenantScopes) List(ctx context.Context) ([]uuid.UUID, error) {
	return s.listTenants(ctx)
}

func (s *TenantScopes) Tx(ctx context.Context, id uuid.UUID, fn func(q engine.Querier) error) error {
	tx, err := tenancy.NewTenantTx(ctx, s.eng, id)
	if err != nil {
		return err
	}
	return finish(ctx, tx, fn)
}

// GlobalScope is for a table that has no tenant (a global_* table): one
// scope, one plain transaction per batch.
type GlobalScope struct {
	eng engine.Engine
}

func NewGlobalScope(eng engine.Engine) *GlobalScope { return &GlobalScope{eng: eng} }

func (s *GlobalScope) List(context.Context) ([]uuid.UUID, error) {
	return []uuid.UUID{uuid.Nil}, nil
}

func (s *GlobalScope) Tx(ctx context.Context, _ uuid.UUID, fn func(q engine.Querier) error) error {
	tx, err := s.eng.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("retention: begin tx: %w", err)
	}
	return finish(ctx, tx, fn)
}

const rollbackTimeout = 5 * time.Second

func finish(ctx context.Context, tx engine.Tx, fn func(q engine.Querier) error) error {
	// After a commit this is a no-op. Detached from ctx because a sweep
	// cancelled mid-batch is exactly when the rollback matters.
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancel()
		_ = tx.Rollback(rctx)
	}()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
