package pendingaction

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// TenantOwners says who the tenant's owner is, for the approvals the steward
// makes: a steward is a tenant-wide helper with no owner of its own.
type TenantOwners interface {
	// TenantOwner is one of the tenant's owners, the same one each time.
	TenantOwner(ctx context.Context, tenant uuid.UUID) (uuid.UUID, error)
}

// WithStewardOwner lets ApproveForSteward approve as the tenant's owner.
// Without it nothing is ever approved for the steward.
func WithStewardOwner(owners TenantOwners) Option {
	return func(s *Service) { s.stewardOwners = owners }
}

// ApproveForSteward approves a proposal on the steward's behalf, as the
// tenant's owner would have, through the same checks and the same ticket
// code as a click or an auto-approve (approve), so the steward has no
// weaker path of its own. It never reads the owner's auto-approve setting:
// the steward's live mode and tier are the owner's say here, and the caller
// has already established them.
//
// The caller decides whether the steward may; this only refuses a kind that
// is never auto-approved, and a tenant with no owner. The tenant is in ctx.
func (s *Service) ApproveForSteward(ctx context.Context, id uuid.UUID) (*Action, error) {
	if s.stewardOwners == nil {
		return nil, ErrNotConfigured
	}
	tenant, err := db.TenantFrom(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := s.stewardOwners.TenantOwner(ctx, tenant)
	if err != nil {
		return nil, fmt.Errorf("pending action: resolve the tenant's owner: %w", err)
	}
	return s.approve(ctx, &authn.User{ID: owner, TenantID: tenant}, id, bySteward)
}
