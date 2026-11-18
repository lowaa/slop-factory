package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// stewardOwner names the tenant's owner the steward's approvals are made as.
// A tenant may have several; the longest-standing one is used every time, so
// the attribution does not drift between runs.
type stewardOwner struct{ database *db.DB }

func newStewardOwner(database *db.DB) *stewardOwner { return &stewardOwner{database: database} }

func (o *stewardOwner) TenantOwner(ctx context.Context, tenant uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := o.database.Global().QueryRow(ctx, `
		SELECT user_id FROM global_tenant_memberships WHERE tenant = $1 AND role = 'owner'
		 ORDER BY created_at, id LIMIT 1`, tenant).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, errors.New("the tenant has no owner")
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("read the tenant's owner: %w", err)
	}
	return id, nil
}
