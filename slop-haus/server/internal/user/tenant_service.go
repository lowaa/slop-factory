package user

import (
	"context"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// TenantArchived is authn.ArchivedChecker. Asked of the database on every
// request, not cached: it is one primary-key read, and a cache would be a
// window after archiving in which the tenant still answered. The freeze
// therefore starts the moment archived_at commits, with no staleness.
func (s *Service) TenantArchived(ctx context.Context, tenantID uuid.UUID) (bool, error) {
	return s.store.TenantArchived(ctx, tenantID)
}

func (s *Service) requireLiveTenant(ctx context.Context, tenantID uuid.UUID) error {
	archived, err := s.store.TenantArchived(ctx, tenantID)
	if err != nil {
		return err
	}
	if archived {
		return ErrTenantArchived
	}
	return nil
}

// MemberDirectory lists who belongs to the signed-in user's tenant and who
// left it. The session's tenant can be up to one access-token lifetime stale,
// so membership is re-checked: a removed member must not keep reading the
// tenant's people.
func (s *Service) MemberDirectory(ctx context.Context, who *authn.User) (*MemberDirectory, error) {
	member, err := s.store.IsMember(ctx, who.ID, who.TenantID)
	if err != nil {
		return nil, err
	}
	if !member {
		return nil, ErrNotMember
	}
	return s.store.ListMemberDirectory(ctx, who.TenantID)
}
