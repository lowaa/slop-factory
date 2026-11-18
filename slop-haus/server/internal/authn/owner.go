package authn

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// ErrNotOwner means the caller is not an owner of the tenant. Packages map it
// onto their own sentinel so their error tables are unchanged.
var ErrNotOwner = errors.New("authn: only a tenant owner can do this")

// ErrNotMember means the caller is not a member of the tenant.
var ErrNotMember = errors.New("authn: only a tenant member can do this")

// TenantOwners answers from the database, not the session: a demoted owner
// must lose owner-only actions at once, not when their token runs out.
type TenantOwners interface {
	IsTenantOwner(ctx context.Context, userID, tenantID uuid.UUID) (bool, error)
}

// TenantMembers is TenantOwners for membership.
type TenantMembers interface {
	IsTenantMember(ctx context.Context, userID, tenantID uuid.UUID) (bool, error)
}

// OwnerGate is the one definition of "owner of this tenant". It refuses
// anything but a signed-in person, and refuses everyone when it has no roles
// to ask: failing open would hand an ordinary member owner-only actions.
type OwnerGate struct {
	roles TenantOwners
	cfg   gateConfig
}

// GateOption configures an OwnerGate or MemberGate.
type GateOption func(*gateConfig)

type gateConfig struct {
	refusal   error
	onRefused func(ctx context.Context, userID, tenantID uuid.UUID)
}

// WithRefusal returns err instead of the gate's own sentinel, so a package
// keeps the error its httperr table already maps.
func WithRefusal(err error) GateOption {
	return func(c *gateConfig) { c.refusal = err }
}

// WithAudit calls fn for each person refused as non-owner or non-member.
func WithAudit(fn func(ctx context.Context, userID, tenantID uuid.UUID)) GateOption {
	return func(c *gateConfig) { c.onRefused = fn }
}

func newGateConfig(def error, opts []GateOption) gateConfig {
	c := gateConfig{refusal: def}
	for _, o := range opts {
		o(&c)
	}
	return c
}

// NewOwnerGate returns a gate over roles.
func NewOwnerGate(roles TenantOwners, opts ...GateOption) *OwnerGate {
	return &OwnerGate{roles: roles, cfg: newGateConfig(ErrNotOwner, opts)}
}

// RequireOwner is nil for an owner of who's tenant, ErrNotPerson for a nil
// who and ErrNotOwner otherwise.
func (g *OwnerGate) RequireOwner(ctx context.Context, who *User) error {
	if err := RequirePerson(who); err != nil {
		return err
	}
	return g.requireOwner(ctx, who.ID, who.TenantID)
}

// RequireOwnerInContext is RequireOwner for a caller known only by id, in the
// tenant ctx is scoped to.
func (g *OwnerGate) RequireOwnerInContext(ctx context.Context, userID uuid.UUID) error {
	tenant, err := db.TenantFrom(ctx)
	if err != nil {
		return g.denial()
	}
	return g.requireOwner(ctx, userID, tenant)
}

// IsOwner reports whether who owns their tenant, for callers that show
// rather than refuse. A nil who or an unconfigured gate is not an owner.
func (g *OwnerGate) IsOwner(ctx context.Context, who *User) (bool, error) {
	if who == nil || g == nil || g.roles == nil {
		return false, nil
	}
	return g.roles.IsTenantOwner(ctx, who.ID, who.TenantID)
}

func (g *OwnerGate) requireOwner(ctx context.Context, userID, tenantID uuid.UUID) error {
	if g == nil || g.roles == nil {
		return g.denial()
	}
	ok, err := g.roles.IsTenantOwner(ctx, userID, tenantID)
	if err != nil {
		return err
	}
	if !ok {
		g.refused(ctx, userID, tenantID)
		return g.cfg.refusal
	}
	return nil
}

// denial is the refusal for a gate with nothing to ask. A nil gate has no
// configuration, so it falls back to the shared sentinel.
func (g *OwnerGate) denial() error {
	if g == nil {
		return ErrNotOwner
	}
	return g.cfg.refusal
}

func (g *OwnerGate) refused(ctx context.Context, userID, tenantID uuid.UUID) {
	if g.cfg.onRefused != nil {
		g.cfg.onRefused(ctx, userID, tenantID)
	}
}

// MemberGate is OwnerGate for membership.
type MemberGate struct {
	roles TenantMembers
	cfg   gateConfig
}

// NewMemberGate returns a gate over roles.
func NewMemberGate(roles TenantMembers, opts ...GateOption) *MemberGate {
	return &MemberGate{roles: roles, cfg: newGateConfig(ErrNotMember, opts)}
}

// RequireMemberOf is nil for a member of tenantID and ErrNotMember otherwise;
// the tenant need not be the session's, as when a conductor is used from
// another of its owner's tenants.
func (g *MemberGate) RequireMemberOf(ctx context.Context, userID, tenantID uuid.UUID) error {
	if g == nil || g.roles == nil {
		if g == nil {
			return ErrNotMember
		}
		return g.cfg.refusal
	}
	ok, err := g.roles.IsTenantMember(ctx, userID, tenantID)
	if err != nil {
		return err
	}
	if !ok {
		if g.cfg.onRefused != nil {
			g.cfg.onRefused(ctx, userID, tenantID)
		}
		return g.cfg.refusal
	}
	return nil
}
