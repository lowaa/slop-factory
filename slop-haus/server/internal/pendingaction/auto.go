package pendingaction

import (
	"context"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Owners says for whom an agent acts. Satisfied by agent.OwnerResolver; the
// same answer decides whose ticket changes the agent may make directly.
type Owners interface {
	ResolveOwner(ctx context.Context, agentID uuid.UUID) (*ticket.OwnerBound, error)
}

// AutoApproveSettings says whether a person, in the tenant in ctx, has
// turned on auto-approve for a kind of proposal.
type AutoApproveSettings interface {
	AutoApprove(ctx context.Context, userID uuid.UUID, kind Kind) (bool, error)
}

type autoApproval struct {
	owners   Owners
	settings AutoApproveSettings
}

// WithAutoApprove lets a person opt in to their own sessions' proposals being
// applied as they are made. Without it nothing is ever auto-approved.
func WithAutoApprove(owners Owners, settings AutoApproveSettings) Option {
	return func(s *Service) { s.auto = &autoApproval{owners: owners, settings: settings} }
}

// autoApprove approves a just-stored proposal as its proposer's owner, when
// that owner has the kind switched on, and returns the proposal as it then
// stands. It never fails the propose call: whatever goes wrong leaves the
// proposal pending for a person, and the checks an approval makes (stale
// ticket, revoked grant, the ticket's rules) apply exactly as to a click.
//
// The owner is resolved before the approval's transaction, so a grant added
// between the two (making the console ambiguous) is not seen; the approval
// still requires a live grant, and the owner could have clicked the same
// approval a moment earlier, so the window gives no one new reach.
//
// Only the owner the resolver names counts. An agent with none (a worker), a
// refused one, or any other member's setting never approves anything.
func (s *Service) autoApprove(ctx context.Context, created *Action) *Action {
	if s.auto == nil || s.tickets == nil {
		return created
	}
	if !created.Kind.Spec().AutoApprovable {
		return created
	}
	owner, err := s.auto.owners.ResolveOwner(ctx, created.AgentID)
	if err != nil || owner == nil {
		return created
	}
	on, err := s.auto.settings.AutoApprove(ctx, owner.UserID, created.Kind)
	if err != nil || !on {
		return created
	}
	tenant, err := db.TenantFrom(ctx)
	if err != nil {
		return created
	}
	approver := &authn.User{ID: owner.UserID, TenantID: tenant}
	if _, err := s.approve(ctx, approver, created.ID, byAutoApprove); err != nil {
		s.logger.InfoContext(ctx, "auto-approval left the proposal as it was",
			"id", created.ID, "kind", created.Kind, "error", err)
	}
	if current, err := s.store.Get(ctx, created.ID, s.now()); err == nil {
		return current
	}
	return created
}
