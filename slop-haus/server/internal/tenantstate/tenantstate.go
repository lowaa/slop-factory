// Package tenantstate answers whether a tenant is archived, for the layers that must refuse work on one.
package tenantstate

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Archived reports whether the tenant is archived. A tenant with no row is
// not: many tests, and a tenant being created, run on an id nothing has
// inserted yet, and "unknown" must not read as "frozen".
func Archived(ctx context.Context, q db.Querier, tenant uuid.UUID) (bool, error) {
	var archived bool
	err := q.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM global_tenants WHERE id = $1 AND archived_at IS NOT NULL)`, tenant).Scan(&archived)
	if err != nil {
		return false, fmt.Errorf("read tenant archived state: %w", err)
	}
	return archived, nil
}

// ActiveIDs is every tenant that is not archived, for the background loops
// that visit each tenant and must not do work or spend for an archived one.
// Pass db.Global().
func ActiveIDs(ctx context.Context, q db.Querier) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, `SELECT id FROM global_tenants WHERE archived_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("list active tenants: %w", err)
	}
	defer rows.Close()
	tenants := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan tenant: %w", err)
		}
		tenants = append(tenants, id)
	}
	return tenants, rows.Err()
}

// Archive is what a package that must turn an archived tenant away takes,
// rather than a database handle. A nil Archive means nothing is archived.
type Archive interface {
	ArchivedTenant(ctx context.Context, tenant uuid.UUID) (bool, error)
}

// Checker is the database-backed Archive.
type Checker struct {
	db *db.DB
}

func NewChecker(database *db.DB) *Checker { return &Checker{db: database} }

var _ Archive = (*Checker)(nil)

func (c *Checker) ArchivedTenant(ctx context.Context, tenant uuid.UUID) (bool, error) {
	return Archived(ctx, c.db.Global(), tenant)
}

// ArchivedInContext is a.ArchivedTenant for ctx's tenant; false for a nil a.
func ArchivedInContext(ctx context.Context, a Archive) (bool, error) {
	if a == nil {
		return false, nil
	}
	tenant, err := db.TenantFrom(ctx)
	if err != nil {
		return false, err
	}
	return a.ArchivedTenant(ctx, tenant)
}
