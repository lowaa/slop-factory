package user

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// TenantAgents switches off every agent of a tenant, releasing what they hold.
// Satisfied by *agent.Service; declared here so this package does not import
// agent.
type TenantAgents interface {
	DisableTenantAgents(ctx context.Context, tenantID uuid.UUID) error
}

// ArchiveTenant puts away a tenant who owns, and switches off its agents.
// The tenant is archived (and its invites revoked) in one transaction first;
// the agents follow, since their disabling spans packages. If that fails the
// tenant stays archived and the error is returned, and archiving again
// finishes the job: it is idempotent. Stopping new agents, sessions and
// switches into an archived tenant is the later parts' freeze; until then
// the window between the commit and the disable is not closed.
func (s *Service) ArchiveTenant(ctx context.Context, who *authn.User, tenantID uuid.UUID) error {
	if s.agents == nil {
		return ErrAgentsUnavailable
	}
	changed, err := s.store.ArchiveTenant(ctx, who.ID, tenantID)
	if err != nil {
		s.auditArchiveRefused(ctx, "archive", who, tenantID, err)
		return err
	}
	if err := s.agents.DisableTenantAgents(ctx, tenantID); err != nil {
		return err
	}
	if changed {
		s.audit(ctx, audit.TenantArchived, "user_id", who.ID, "tenant_id", tenantID)
	}
	return nil
}

// UnarchiveTenant brings back a tenant who owns. Its agents stay disabled:
// the owner turns on the ones they want.
func (s *Service) UnarchiveTenant(ctx context.Context, who *authn.User, tenantID uuid.UUID) error {
	changed, err := s.store.UnarchiveTenant(ctx, who.ID, tenantID, s.cfg.TenantOwnerLimit)
	if err != nil {
		s.auditArchiveRefused(ctx, "unarchive", who, tenantID, err)
		return err
	}
	if changed {
		s.audit(ctx, audit.TenantUnarchived, "user_id", who.ID, "tenant_id", tenantID)
	}
	return nil
}

func (s *Service) auditArchiveRefused(ctx context.Context, op string, who *authn.User, tenantID uuid.UUID, err error) {
	var reason string
	switch {
	case errors.Is(err, ErrNotOwner):
		reason = "not owner"
	case errors.Is(err, ErrLastActiveTenant):
		reason = "last active tenant"
	case errors.Is(err, ErrUnarchiveOverLimit):
		reason = "over limit"
	default:
		return
	}
	s.audit(ctx, audit.TenantArchiveRefused, "user_id", who.ID, "tenant_id", tenantID, "op", op, "reason", reason)
}

// OtherActiveTenant is a tenant who could move to when leaving the one named,
// for a session in a tenant that has just been archived. ErrNotFound if they
// have no other.
func (s *Service) OtherActiveTenant(ctx context.Context, who *authn.User, leaving uuid.UUID) (uuid.UUID, error) {
	return s.store.OtherActiveTenant(ctx, who.ID, leaving)
}

// ListTenants is Tenants with the owner's archived tenants too when
// includeArchived.
func (s *Service) ListTenants(ctx context.Context, who *authn.User, includeArchived bool) ([]TenantSummary, error) {
	return s.store.ListTenants(ctx, who.ID, includeArchived)
}

// ArchiveOwnedTenant is an owner's archive from the settings page: it needs a
// recent sign-in and the tenant's exact name typed back. If who's session is
// in the tenant being archived it comes back moved to another of their active
// tenants (nil otherwise), since an archived tenant admits no session.
func (s *Service) ArchiveOwnedTenant(ctx context.Context, who *authn.User, tenantID uuid.UUID, confirmName string) (*Session, error) {
	if err := RequireRecentAuth(who); err != nil {
		return nil, err
	}
	name, err := s.ownedTenantName(ctx, who, tenantID)
	if err != nil {
		s.auditArchiveRefused(ctx, "archive", who, tenantID, err)
		return nil, err
	}
	if confirmName != name {
		s.audit(ctx, audit.TenantArchiveRefused, "user_id", who.ID, "tenant_id", tenantID, "op", "archive", "reason", "name mismatch")
		return nil, ErrArchiveNameMismatch
	}
	if err := s.ArchiveTenant(ctx, who, tenantID); err != nil {
		return nil, err
	}
	if who.TenantID != tenantID {
		return nil, nil
	}
	other, err := s.store.OtherActiveTenant(ctx, who.ID, tenantID)
	if err != nil {
		return nil, err
	}
	u, err := s.liveSessionUser(ctx, who)
	if err != nil {
		return nil, err
	}
	return s.sessionFor(u, other, sessionOf(who))
}

// UnarchiveOwnedTenant is an owner's unarchive, which needs a recent sign-in.
func (s *Service) UnarchiveOwnedTenant(ctx context.Context, who *authn.User, tenantID uuid.UUID) error {
	if err := RequireRecentAuth(who); err != nil {
		return err
	}
	return s.UnarchiveTenant(ctx, who, tenantID)
}

// ArchivedTenants are the tenants who has archived: only an owner can, and
// only an owner is shown them.
func (s *Service) ArchivedTenants(ctx context.Context, who *authn.User) ([]TenantSummary, error) {
	all, err := s.store.ListTenants(ctx, who.ID, true)
	if err != nil {
		return nil, err
	}
	var archived []TenantSummary
	for i := range all {
		if all[i].ArchivedAt != nil && all[i].IsOwner {
			archived = append(archived, all[i])
		}
	}
	return archived, nil
}

// ownedTenantName is the name of a tenant who owns, archived or not.
// ErrNotOwner for anyone else, whether or not the tenant exists.
func (s *Service) ownedTenantName(ctx context.Context, who *authn.User, tenantID uuid.UUID) (string, error) {
	all, err := s.store.ListTenants(ctx, who.ID, true)
	if err != nil {
		return "", err
	}
	for i := range all {
		if all[i].ID == tenantID && all[i].IsOwner {
			return all[i].Name, nil
		}
	}
	return "", ErrNotOwner
}
