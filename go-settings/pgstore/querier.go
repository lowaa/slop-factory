package pgstore

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/go-db/engine"
)

// Querier is what the queries run on: pgx's method names, so a transaction
// and a pool both fit, and an app's own scoped query handle fits when it
// already has these two methods.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// FromEngine adapts a go-db engine.Querier (an Engine, or a tenancy.TenantTx)
// to a Querier. A TenantTx is how a tenant scope is applied, so row-level
// security keeps a tenant table's queries to its tenant.
func FromEngine(q engine.Querier) Querier { return engineQuerier{q} }

type engineQuerier struct{ q engine.Querier }

func (e engineQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return e.q.QueryContext(ctx, sql, args...)
}

func (e engineQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return e.q.QueryRowContext(ctx, sql, args...)
}
