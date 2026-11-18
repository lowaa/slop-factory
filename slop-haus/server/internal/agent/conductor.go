package agent

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// TenantRoles says who belongs to a tenant and who owns it. Asked of the
// database on every call rather than read from the session, so a member
// removed or an owner demoted loses what they could do at once. Satisfied by
// *user.Service.
type TenantRoles interface {
	IsTenantOwner(ctx context.Context, userID, tenantID uuid.UUID) (bool, error)
	IsTenantMember(ctx context.Context, userID, tenantID uuid.UUID) (bool, error)
}

// OwnedConductor is proof that a conductor was checked against the person
// using it: the only way to get one is ConductorService.AuthorizeOwner, and
// nothing outside this package can build one.
//
// Every conductor-facing service method that acts for a user -- starting or
// using a conversation, minting the conductor's credential -- takes one
// instead of an id, so a handler cannot reach the conductor without having
// passed the ownership check, and a method added without it does not compile
// against a handler that has only an id. TestEveryConductorMethodIsOwnerChecked
// fails when a method takes neither this nor an explicit exemption.
type OwnedConductor struct {
	conductor *Agent
	owner     uuid.UUID
}

func (o *OwnedConductor) Agent() *Agent { return o.conductor }

// Owner is the user it was authorized for, who is also the user who
// registered it.
func (o *OwnedConductor) Owner() uuid.UUID { return o.owner }

// ConductorService is what registering, listing, disabling and using a
// conductor means. A conductor belongs to the user who registered it: they
// alone may use it. Tenant owners can switch one off but never use it.
type ConductorService struct {
	agents *Service
	roles  TenantRoles
	owner  *authn.OwnerGate
	member *authn.MemberGate
	logger *slog.Logger
}

func NewConductorService(agents *Service, roles TenantRoles, logHandler slog.Handler) *ConductorService {
	return &ConductorService{
		agents: agents, roles: roles, logger: slog.New(logHandler),
		owner:  authn.NewOwnerGate(roles, authn.WithRefusal(ErrNotTenantOwner)),
		member: authn.NewMemberGate(roles, authn.WithRefusal(ErrNotOwner)),
	}
}

// Register makes a conductor owned by who. Owning one lets it put proposals
// in front of the person, as turning a console on does, so it needs the same
// recent sign-in. It is only ever a signed-in person's act: the owner comes
// from the session, never from the request.
func (c *ConductorService) Register(ctx context.Context, who *authn.User, name string) (*Agent, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	if err := user.RequireRecentAuth(who); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrEmptyName
	}
	if err := c.member.RequireMemberOf(ctx, who.ID, who.TenantID); err != nil {
		return nil, err
	}
	created, err := c.agents.store.CreateConductor(ctx, uuid.New(), name, who.ID, nil)
	if err != nil {
		return nil, err
	}
	c.logger.InfoContext(ctx, "audit", "action", "conductor_registered", "user_id", who.ID, "tenant", who.TenantID, "agent", created.ID)
	return created, nil
}

// ListOwned is the conductors who registered, while who is still a member.
func (c *ConductorService) ListOwned(ctx context.Context, who *authn.User, includeArchived bool) ([]Agent, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	if err := c.member.RequireMemberOf(ctx, who.ID, who.TenantID); err != nil {
		return nil, err
	}
	return c.agents.store.ListConductors(ctx, &who.ID, includeArchived)
}

// ListOthers is the conductors other people registered, for the tenant's
// owner to disable. Anyone else sees none: listing is for your own.
func (c *ConductorService) ListOthers(ctx context.Context, who *authn.User, includeArchived bool) ([]Agent, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	if err := c.owner.RequireOwner(ctx, who); err != nil {
		if errors.Is(err, ErrNotTenantOwner) {
			return []Agent{}, nil
		}
		return nil, err
	}
	all, err := c.agents.store.ListConductors(ctx, nil, includeArchived)
	if err != nil {
		return nil, err
	}
	others := make([]Agent, 0, len(all))
	for i := range all {
		if !all[i].OwnedBy(who.ID) {
			others = append(others, all[i])
		}
	}
	return others, nil
}

// ListDirectory is every conductor in the tenant, for any member to see who
// has one and whether it is in use. It lists; acting on one stays with
// SetDisabled and the owner checks.
func (c *ConductorService) ListDirectory(ctx context.Context, who *authn.User, includeArchived bool) ([]DirectoryEntry, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	if err := c.member.RequireMemberOf(ctx, who.ID, who.TenantID); err != nil {
		return nil, err
	}
	return c.agents.store.ListConductorDirectory(ctx, includeArchived)
}

// AuthorizeOwner is the check every use of a conductor goes through: the
// conductor must exist in the user's tenant (another tenant's reads as
// missing), be enabled, have been registered by who, and who must still be
// a member of the tenant. A tenant owner who did not register it is refused
// like anyone else, and so is a former member: the check fails closed.
func (c *ConductorService) AuthorizeOwner(ctx context.Context, who *authn.User, id uuid.UUID) (*OwnedConductor, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	found, err := c.conductor(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found.OwnedBy(who.ID) {
		c.logger.WarnContext(ctx, "audit", "action", "conductor_use_refused", "user_id", who.ID, "tenant", who.TenantID, "agent", id)
		return nil, ErrNotOwner
	}
	return c.proveOwner(ctx, found, who.ID)
}

// AuthorizeSession is AuthorizeOwner for the conductor's own authenticated
// session, which has no signed-in person beside it: the owner is the user who
// registered it, and the same rules apply to them -- enabled, owner still a
// member. Only a session that has already authenticated as this conductor may
// ask, with that session's own agent id.
func (c *ConductorService) AuthorizeSession(ctx context.Context, id uuid.UUID) (*OwnedConductor, error) {
	found, err := c.conductor(ctx, id)
	if err != nil {
		return nil, err
	}
	if found.OwnerUserID == nil {
		return nil, ErrNotOwner
	}
	return c.proveOwner(ctx, found, *found.OwnerUserID)
}

func (c *ConductorService) proveOwner(ctx context.Context, found *Agent, owner uuid.UUID) (*OwnedConductor, error) {
	if err := c.member.RequireMemberOf(ctx, owner, found.Tenant); err != nil {
		return nil, err
	}
	if found.Disabled() {
		return nil, ErrIsDisabled
	}
	return &OwnedConductor{conductor: found, owner: owner}, nil
}

// MayChangeConfig is the owner check for a route that takes an agent id of
// either kind, such as saving an agent's harness config: a conductor's is its
// owner's alone, because it runs on their machine and spends on their
// subscription. Any other id is allowed, for the route's own rules to judge.
//
// Unlike AuthorizeOwner it does not need the conductor enabled: its owner
// configures it while it is off, before switching it on. It still needs the
// owner to be a member, so a former member cannot.
func (c *ConductorService) MayChangeConfig(ctx context.Context, who *authn.User, id uuid.UUID) (bool, error) {
	if err := authn.RequirePerson(who); err != nil {
		return false, err
	}
	found, err := c.conductor(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !found.OwnedBy(who.ID) {
		return false, nil
	}
	err = c.member.RequireMemberOf(ctx, who.ID, found.Tenant)
	if errors.Is(err, ErrNotOwner) {
		return false, nil
	}
	return err == nil, err
}

// SetDisabled switches a conductor off or back on. Either its owner or the
// tenant's owner may switch it off, which ends its pending proposals and
// revokes its grants, as for any agent. Only its owner may switch it on, and
// only while still a member: a tenant owner who could would revive a
// conductor its owner turned off (after a leaked token, say) and a conductor
// disabled because its owner left stays off.
func (c *ConductorService) SetDisabled(ctx context.Context, who *authn.User, id uuid.UUID, disabled bool) (*Agent, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	// A former member is neither its owner nor the tenant's.
	if err := c.member.RequireMemberOf(ctx, who.ID, who.TenantID); err != nil {
		return nil, err
	}
	found, err := c.conductor(ctx, id)
	if err != nil {
		return nil, err
	}
	switch {
	case !disabled:
		if !found.OwnedBy(who.ID) {
			return nil, ErrNotOwner
		}
		if err := c.member.RequireMemberOf(ctx, who.ID, found.Tenant); err != nil {
			return nil, err
		}
	case !found.OwnedBy(who.ID):
		if err := c.owner.RequireOwner(ctx, who); err != nil {
			// A member who is neither reads the refusal for using it.
			if errors.Is(err, ErrNotTenantOwner) {
				return nil, ErrNotOwner
			}
			return nil, err
		}
	}
	updated, err := c.agents.setDisabled(ctx, id, disabled)
	if err != nil {
		return nil, err
	}
	c.logger.InfoContext(ctx, "audit", "action", "conductor_disabled_set", "user_id", who.ID, "tenant", who.TenantID, "agent", id, "disabled", disabled)
	return updated, nil
}

// SetArchived puts a disabled conductor away or brings it back. Its owner or
// the tenant's owner may do either: neither revives anything, since an
// unarchived conductor is still disabled and only its owner enables it.
func (c *ConductorService) SetArchived(ctx context.Context, who *authn.User, id uuid.UUID, archived bool) (*Agent, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	if err := c.member.RequireMemberOf(ctx, who.ID, who.TenantID); err != nil {
		return nil, err
	}
	found, err := c.conductor(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found.OwnedBy(who.ID) {
		if err := c.owner.RequireOwner(ctx, who); err != nil {
			if errors.Is(err, ErrNotTenantOwner) {
				return nil, ErrNotOwner
			}
			return nil, err
		}
	}
	// Its supervisor's entry would still name it, and be refused it from then
	// on: removing the entry retires it instead.
	if archived && found.RunBySupervisorID != nil && !found.Archived() {
		return nil, ErrRunBySupervisor
	}
	return c.agents.setArchived(ctx, who, id, archived)
}

// SignOutWorkspaces revokes every credential acting as the conductor, ahead
// of a new setup script for it. A credential is how the conductor is used,
// so this is the owner's alone.
func (c *ConductorService) SignOutWorkspaces(ctx context.Context, owned *OwnedConductor) (*Agent, error) {
	// Read again: the proof is from some time ago, and a conductor disabled
	// since must not be signed out as if it were still live.
	current, err := c.conductor(ctx, owned.conductor.ID)
	if err != nil {
		return nil, err
	}
	return c.agents.signOut(ctx, current)
}

// conductor reads one conductor in the caller's tenant; a worker, or no
// such agent, is not found.
func (c *ConductorService) conductor(ctx context.Context, id uuid.UUID) (*Agent, error) {
	found, err := c.agents.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found.IsConductor() {
		return nil, ErrNotFound
	}
	return found, nil
}
