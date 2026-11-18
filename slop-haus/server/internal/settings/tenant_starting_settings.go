package settings

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// TenantStartingSettings starts new tenants with provision_from_board on.
//
// It is written, not declared as the default: only a write runs the
// provisioning hook, and a changed default would switch every existing
// tenant that has stored nothing, without being asked.
type TenantStartingSettings struct{ svc *Service }

func NewTenantStartingSettings(svc *Service) *TenantStartingSettings {
	return &TenantStartingSettings{svc: svc}
}

// TenantCreated writes the starting settings through the service, as the
// owner's own toggle would, so the hook runs (with nothing to import yet).
func (d *TenantStartingSettings) TenantCreated(ctx context.Context, tenantID, ownerID uuid.UUID) error {
	ctx = db.WithTenant(ctx, tenantID)
	if _, err := d.svc.Set(ctx, ProvisionFromBoard, BoolValue(true), ownerID); err != nil {
		return fmt.Errorf("settings: start new tenant with %s on: %w", ProvisionFromBoard, err)
	}
	return nil
}
