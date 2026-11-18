package user

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/go-auth/auth"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// Invites replace open sign-up: an admin invites someone to start a tenant
// of their own, and any member invites someone into theirs. Either way the
// emailed link is what proves the invitee owns the address, as an
// activation link does.

// InviteToNewTenant sends an admin's invitation to start a tenant. The
// caller has already been checked as an admin (authn.RequireAdmin).
func (s *Service) InviteToNewTenant(ctx context.Context, admin *authn.User, rawEmail string) (*Invite, error) {
	return s.invite(ctx, admin, rawEmail, InviteNewTenant, nil)
}

// InviteToTenant sends a member's invitation into their current tenant.
func (s *Service) InviteToTenant(ctx context.Context, who *authn.User, rawEmail string) (*Invite, error) {
	tenantID := who.TenantID
	return s.invite(ctx, who, rawEmail, InviteJoinTenant, &tenantID)
}

func (s *Service) invite(ctx context.Context, who *authn.User, rawEmail string, kind InviteKind, tenantID *uuid.UUID) (*Invite, error) {
	if !s.inviteLimit.Allow(who.ID.String()) {
		return nil, ErrRateLimited
	}
	address := normalizeEmail(rawEmail)
	inviter, err := s.store.FindByID(ctx, who.ID)
	if err != nil {
		return nil, err
	}
	if tenantID != nil {
		if err := s.requireMember(ctx, who); err != nil {
			return nil, err
		}
		// Only the inviter's own tenant is at stake, so telling them the
		// address is already in it reveals nothing they could not see on
		// the members list.
		if existing, err := s.store.FindByEmail(ctx, address); err == nil {
			member, err := s.store.IsMember(ctx, existing.ID, *tenantID)
			if err != nil {
				return nil, err
			}
			if member {
				return nil, ErrAlreadyMember
			}
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}

	raw, hash, err := auth.NewOpaqueToken()
	if err != nil {
		return nil, fmt.Errorf("user: new invite token: %w", err)
	}
	inv := &Invite{
		ID:        uuid.New(),
		Email:     address,
		Kind:      kind,
		TenantID:  tenantID,
		InvitedBy: who.ID,
		ExpiresAt: s.clock.Now().Add(s.cfg.InviteTTL),
		CreatedAt: s.clock.Now(),
	}
	if err := s.store.CreateInvite(ctx, inv, hash); err != nil {
		return nil, err
	}

	var what string
	if kind == InviteNewTenant {
		what = "to start your own workspace on slop-haus"
	} else {
		what = "to join their slop-haus workspace"
	}
	s.mail.Enqueue(address, inviter.DisplayName+" invited you to slop-haus",
		inviter.DisplayName+" ("+inviter.Email+") has invited you "+what+":\n\n"+
			s.link("/invite", raw)+
			"\n\nThe link works once and expires in "+s.cfg.InviteTTL.String()+
			". If you were not expecting this, ignore this email.\n")
	return inv, nil
}

// PreviewInvite describes an invite for the page it links to.
func (s *Service) PreviewInvite(ctx context.Context, token string) (*InvitePreview, error) {
	_, preview, err := s.store.FindInvite(ctx, auth.HashOpaqueToken(token))
	return preview, err
}

// AcceptInviteAsNewAccount creates the invitee's account and signs them
// in. The invite is checked before the password is hashed, as a reset is:
// this route is public.
func (s *Service) AcceptInviteAsNewAccount(ctx context.Context, token, displayName, password, tenantName string) (*Session, error) {
	if err := s.requirePasswordSignIn(); err != nil {
		return nil, err
	}
	newTenant, err := newTenantNamed(tenantName)
	if err != nil {
		return nil, err
	}
	tokenHash := auth.HashOpaqueToken(token)
	if _, _, err := s.store.FindInvite(ctx, tokenHash); err != nil {
		return nil, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	u := &User{ID: uuid.New(), PasswordHash: hash, DisplayName: strings.TrimSpace(displayName), IsActive: true}
	tenantID, err := s.store.AcceptInviteAsNewAccount(ctx, tokenHash, u, newTenant)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, audit.InviteAccepted, "user_id", u.ID, "method", "password", "new_account", true)
	return s.newSessionFor(u, tenantID)
}

// AcceptInvite accepts an invite for the signed-in user, and moves their
// session into the tenant it grants.
func (s *Service) AcceptInvite(ctx context.Context, who *authn.User, token, tenantName string) (*Session, error) {
	newTenant, err := newTenantNamed(tenantName)
	if err != nil {
		return nil, err
	}
	u, err := s.liveSessionUser(ctx, who)
	if err != nil {
		return nil, err
	}
	tenantID, err := s.store.AcceptInvite(ctx, auth.HashOpaqueToken(token), u.ID, u.Email, newTenant)
	if err != nil {
		return nil, err
	}
	return s.sessionFor(u, tenantID, sessionOf(who))
}

// newTenantNamed is the tenant an accepted new_tenant invite makes. A
// name that is left out is the person's own, as activation names theirs. A
// name is checked whatever the invite's kind, which is not known until it is
// spent: a join_tenant invite makes nothing, and its page sends none.
func newTenantNamed(raw string) (*NewTenant, error) {
	t := &NewTenant{ID: uuid.New()}
	if strings.TrimSpace(raw) == "" {
		return t, nil
	}
	name, err := ValidateTenantName(raw)
	if err != nil {
		return nil, err
	}
	t.Name = name
	return t, nil
}

// CreateTenant makes a tenant owned by who, as any signed-in user may up to
// the owner limits, and starts a session in it. It goes through the same
// store path as an accepted new_tenant invite, which is not capped.
func (s *Service) CreateTenant(ctx context.Context, who *authn.User, rawName string) (*Session, error) {
	refuse := func(reason string, err error) (*Session, error) {
		s.audit(ctx, audit.TenantCreateRefused, "user_id", who.ID, "reason", reason)
		return nil, err
	}
	if s.cfg.TenantOwnerLimit <= 0 {
		return refuse("off", ErrTenantCreationOff)
	}
	name, err := ValidateTenantName(rawName)
	if err != nil {
		return nil, err
	}
	if !s.tenantCreateLimit.Allow(who.ID.String()) {
		return refuse("rate limited", ErrRateLimited)
	}
	u, err := s.liveSessionUser(ctx, who)
	if err != nil {
		return nil, err
	}
	t := &NewTenant{ID: uuid.New(), Name: name}
	if err := s.store.CreateTenant(ctx, u.ID, t, OwnerLimits{Active: s.cfg.TenantOwnerLimit, Total: s.cfg.TenantOwnerTotalLimit}); err != nil {
		if errors.Is(err, ErrTenantLimit) {
			return refuse("limit", err)
		}
		return nil, err
	}
	s.audit(ctx, audit.TenantCreated, "user_id", u.ID, "tenant_id", t.ID, "name", name)
	return s.sessionFor(u, t.ID, sessionOf(who))
}

// TenantOwnerLimit is how many tenants a person may create for themselves.
func (s *Service) TenantOwnerLimit() int { return s.cfg.TenantOwnerLimit }

// RenameTenant renames who's current tenant. Ownership is checked by the
// store in the transaction that writes the name.
func (s *Service) RenameTenant(ctx context.Context, who *authn.User, rawName string) (*TenantSummary, error) {
	name, err := ValidateTenantName(rawName)
	if err != nil {
		return nil, err
	}
	old, err := s.store.RenameTenant(ctx, who.ID, who.TenantID, name)
	if err != nil {
		if errors.Is(err, ErrNotOwner) {
			s.audit(ctx, audit.TenantRenameRefused, "user_id", who.ID, "tenant_id", who.TenantID, "reason", "not owner")
		}
		return nil, err
	}
	s.audit(ctx, audit.TenantRenamed, "user_id", who.ID, "tenant_id", who.TenantID, "old_name", old, "new_name", name)
	return s.currentTenant(ctx, who)
}

// SetTenantColour sets, or with nil clears, who's current tenant's colour.
// Ownership is checked by the store in the transaction that writes it.
func (s *Service) SetTenantColour(ctx context.Context, who *authn.User, colour *string) (*TenantSummary, error) {
	if err := ValidateTenantColour(colour); err != nil {
		return nil, err
	}
	old, err := s.store.SetTenantColour(ctx, who.ID, who.TenantID, colour)
	if err != nil {
		if errors.Is(err, ErrNotOwner) {
			s.audit(ctx, audit.TenantRecolourRefused, "user_id", who.ID, "tenant_id", who.TenantID, "reason", "not owner")
		}
		return nil, err
	}
	s.audit(ctx, audit.TenantRecoloured, "user_id", who.ID, "tenant_id", who.TenantID, "old_colour", deref(old), "new_colour", deref(colour))
	return s.currentTenant(ctx, who)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (s *Service) currentTenant(ctx context.Context, who *authn.User) (*TenantSummary, error) {
	tenants, err := s.store.ListTenants(ctx, who.ID, false)
	if err != nil {
		return nil, err
	}
	for i := range tenants {
		if tenants[i].ID == who.TenantID {
			return &tenants[i], nil
		}
	}
	return nil, ErrNotFound
}

// ListAdminInvites lists outstanding invitations to start a tenant. The
// caller has been checked as an admin (authn.RequireAdmin).
func (s *Service) ListAdminInvites(ctx context.Context) ([]Invite, error) {
	return s.store.ListInvites(ctx, nil)
}

// RevokeAdminInvite withdraws an invitation to start a tenant.
func (s *Service) RevokeAdminInvite(ctx context.Context, id uuid.UUID) error {
	return s.store.RevokeInvite(ctx, id, nil)
}

// ListInvites lists the outstanding invites into the member's current
// tenant.
func (s *Service) ListInvites(ctx context.Context, who *authn.User) ([]Invite, error) {
	if err := s.requireMember(ctx, who); err != nil {
		return nil, err
	}
	return s.store.ListInvites(ctx, &who.TenantID)
}

// RevokeInvite withdraws an invite into the member's current tenant.
func (s *Service) RevokeInvite(ctx context.Context, who *authn.User, id uuid.UUID) error {
	if err := s.requireMember(ctx, who); err != nil {
		return err
	}
	return s.store.RevokeInvite(ctx, id, &who.TenantID)
}

// requireMember re-checks the session's tenant against the database. The
// session's tenant is up to one access-token lifetime stale, and a member
// removed in that window must lose the tenant's invitations at once, not
// when their token runs out.
func (s *Service) requireMember(ctx context.Context, who *authn.User) error {
	member, err := s.store.IsMember(ctx, who.ID, who.TenantID)
	if err != nil {
		return err
	}
	if !member {
		return ErrNotMember
	}
	return nil
}

// IsTenantOwner re-checks the database rather than trusting the session,
// for the same reason requireMember does: an owner demoted or removed must
// lose what only owners may do at once, not when their token runs out.
func (s *Service) IsTenantOwner(ctx context.Context, userID, tenantID uuid.UUID) (bool, error) {
	return s.store.IsOwner(ctx, userID, tenantID)
}

// IsTenantMember re-checks the database for the same reason IsTenantOwner does.
func (s *Service) IsTenantMember(ctx context.Context, userID, tenantID uuid.UUID) (bool, error) {
	return s.store.IsMember(ctx, userID, tenantID)
}

// Tenants lists the tenants the signed-in user belongs to.
func (s *Service) Tenants(ctx context.Context, who *authn.User) ([]TenantSummary, error) {
	return s.store.ListTenants(ctx, who.ID, false)
}

// SwitchTenant starts a session for the same user in another of their
// tenants.
func (s *Service) SwitchTenant(ctx context.Context, who *authn.User, tenantID uuid.UUID) (*Session, error) {
	member, err := s.store.IsMember(ctx, who.ID, tenantID)
	if err != nil {
		return nil, err
	}
	if !member {
		return nil, ErrNotMember
	}
	if err := s.requireLiveTenant(ctx, tenantID); err != nil {
		return nil, err
	}
	u, err := s.liveSessionUser(ctx, who)
	if err != nil {
		return nil, err
	}
	return s.sessionFor(u, tenantID, sessionOf(who))
}

// IsAdmin is authn.AdminChecker.
func (s *Service) IsAdmin(ctx context.Context, userID uuid.UUID) (bool, error) {
	return s.store.IsAdmin(ctx, userID)
}

// PasswordLoginEnabled reports whether people may sign in with a password,
// through one place, so the config endpoint, the sign-in methods list and
// every password route can't disagree.
func (s *Service) PasswordLoginEnabled() bool { return !s.cfg.PasswordSignInDisabled }

func (s *Service) requirePasswordSignIn() error {
	if !s.PasswordLoginEnabled() {
		return ErrPasswordSignInOff
	}
	return nil
}

// RegistrationOpen reports whether anyone may sign up.
func (s *Service) RegistrationOpen() bool {
	return s.cfg.RegistrationOpen
}
