package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// GrantOwners says which people approved an agent's live full-access
// grants. Satisfied by *oauth.Service.
type GrantOwners interface {
	FullAccessApprovers(ctx context.Context, tenantID, agentID uuid.UUID) ([]*uuid.UUID, error)
}

// UserNames says what to call a person on a ticket. Satisfied by *user.Service.
type UserNames interface {
	DisplayName(ctx context.Context, userID uuid.UUID) (string, error)
}

// OwnerResolver decides whose behalf an agent acts on, as of now. Satisfies
// ticket.OwnerResolver.
//
//   - A conductor acts for the user who registered it, through the same checks
//     as using it (ConductorService.AuthorizeSession).
//   - A console acts for the user who approved its live full-access grants,
//     when exactly one user did. A console has no registered owner, so grants
//     are the only link; any other shape is refused rather than guessed at.
//   - Any other agent is a worker, which has no owner.
//
// Disabled, a revoked grant, or an owner who has left the tenant all refuse.
type OwnerResolver struct {
	agents     *Service
	conductors *ConductorService
	roles      TenantRoles
	grants     GrantOwners
	names      UserNames
}

func NewOwnerResolver(agents *Service, conductors *ConductorService, roles TenantRoles, grants GrantOwners, names UserNames) *OwnerResolver {
	return &OwnerResolver{agents: agents, conductors: conductors, roles: roles, grants: grants, names: names}
}

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ticket.ErrOwnerBoundRefused, fmt.Sprintf(format, args...))
}

func (r *OwnerResolver) ResolveOwner(ctx context.Context, agentID uuid.UUID) (*ticket.OwnerBound, error) {
	found, err := r.agents.store.Get(ctx, agentID)
	if errors.Is(err, ErrNotFound) {
		return nil, refuse("agent not found")
	}
	if err != nil {
		return nil, err
	}
	var owner uuid.UUID
	switch {
	case found.IsConductor():
		owned, err := r.conductors.AuthorizeSession(ctx, agentID)
		if err != nil {
			return nil, refuse("%v", err)
		}
		owner = owned.Owner()
	case found.IsConsole():
		if found.Disabled() {
			return nil, refuse("agent is disabled")
		}
		if owner, err = r.consoleOwner(ctx, found); err != nil {
			return nil, err
		}
	default:
		return nil, nil
	}
	name, err := r.names.DisplayName(ctx, owner)
	if err != nil {
		return nil, err
	}
	return &ticket.OwnerBound{AgentID: agentID, AgentName: found.Name, UserID: owner, UserName: name}, nil
}

func (r *OwnerResolver) consoleOwner(ctx context.Context, found *Agent) (uuid.UUID, error) {
	approvers, err := r.grants.FullAccessApprovers(ctx, found.Tenant, found.ID)
	if err != nil {
		return uuid.Nil, err
	}
	if len(approvers) == 0 {
		return uuid.Nil, refuse("the console holds no live full-access grant")
	}
	var owner uuid.UUID
	for i, a := range approvers {
		if a == nil {
			return uuid.Nil, refuse("a live grant has no recorded approver")
		}
		if i > 0 && *a != owner {
			return uuid.Nil, refuse("the console's live grants were approved by more than one person")
		}
		owner = *a
	}
	member, err := r.roles.IsTenantMember(ctx, owner, found.Tenant)
	if err != nil {
		return uuid.Nil, err
	}
	if !member {
		return uuid.Nil, refuse("the console's owner is no longer a member of the tenant")
	}
	return owner, nil
}
