package tenantstate

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Roster lists the tenants a background pass visits.
type Roster interface {
	Tenants(ctx context.Context) ([]uuid.UUID, error)
}

type activeRoster struct{ q db.Querier }

func (r activeRoster) Tenants(ctx context.Context) ([]uuid.UUID, error) { return ActiveIDs(ctx, r.q) }

type allRoster struct{ q db.Querier }

func (r allRoster) Tenants(ctx context.Context) ([]uuid.UUID, error) { return allIDs(ctx, r.q) }

// Active is the roster of tenants that are not archived: the default for a
// loop, which must not do work or spend for an archived one. q is db.Global().
func Active(q db.Querier) Roster { return activeRoster{q} }

// All is the roster of every tenant, archived or not, for work that must
// reach an archived tenant's rows too (pruning). q is db.Global().
func All(q db.Querier) Roster { return allRoster{q} }

func allIDs(ctx context.Context, q db.Querier) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, `SELECT id FROM global_tenants`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tenants := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		tenants = append(tenants, id)
	}
	return tenants, rows.Err()
}

// Each calls fn once per tenant of the roster, with ctx scoped to that
// tenant. The tenant list is read once per call. A tenant that fails does not
// stop the others: they are independent, and one tenant's error is no reason
// to leave every other unvisited. The errors are joined once all have been
// tried. Only ctx ending stops the visit early.
func Each(ctx context.Context, roster Roster, fn func(ctx context.Context, tenant uuid.UUID) error) error {
	tenants, err := roster.Tenants(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, tenant := range tenants {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if err := fn(db.WithTenant(ctx, tenant), tenant); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
