package ticket

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// OwnerBound is an agent session that acts for one person: a conductor, tied
// to the user who registered it, or a console, tied to the user whose live
// full-access grant it holds. It is what an OwnerResolver vouches for.
type OwnerBound struct {
	AgentID   uuid.UUID
	AgentName string
	UserID    uuid.UUID
	UserName  string
}

// annotate names who a change was made for, so a reader of the ticket sees
// "agent X for user Y" and not an agent's edit that looks self-directed.
func (o *OwnerBound) annotate(body string) string {
	if o == nil {
		return body
	}
	return fmt.Sprintf("%s (by agent %s for %s)", body, o.AgentName, o.UserName)
}

// OwnerResolver says whether an agent is owner-bound and, if so, for whom,
// as of now. It returns (nil, nil) for an agent that is not of an
// owner-bound kind, and an error wrapping ErrOwnerBoundRefused for one that
// is but whose owner cannot be established: disabled, grant revoked, owner
// no longer a member, or an owner that is ambiguous. Implemented by
// internal/agent.
type OwnerResolver interface {
	ResolveOwner(ctx context.Context, agentID uuid.UUID) (*OwnerBound, error)
}

// ErrOwnerBoundRefused refuses an owner-bound session whose owner cannot be
// established. It never falls back to the worker rule: a console that lost
// its grant, or a conductor whose owner left, is not a worker.
var ErrOwnerBoundRefused = errors.New("ticket: this session is tied to a person who cannot be established right now")

// WithEvents records the direct blocker edits of owner-bound sessions.
func WithEvents(r *audit.Recorder) ServiceOption {
	return func(s *Service) { s.events = r }
}

// AgentComment is the ticket_comment tool's comment. A worker's is a plain
// agent comment. An owner-bound session's is recorded as made for its owner
// and annotated, and is then delivered to the ticket's holder as the owner's
// own comment (the deliver_human_comment trigger) -- the one route by which
// an agent's words reach another agent as a person's. Only a session that
// already acts as the person gets it; it never widens beyond that.
//
// A session whose owner cannot be established right now (grant revoked, owner
// gone) comments plainly: a comment is never refused for this, and a plain
// comment reaches nobody.
//
// It names no attempt: see AgentCommentOn.
func (s *Service) AgentComment(ctx context.Context, agentID uuid.UUID, ticketID int64, body string) (*Comment, error) {
	return s.AgentCommentOn(ctx, agentID, ticketID, nil, body)
}

// AgentCommentOn is AgentComment addressed to the attempt an owner-bound
// session named; see ResolveAttempt for its refusals. Naming none is never
// refused: the comment is the ticket's, delivered to every live attempt. A worker
// never passes one: it is refused with ErrWorkerNamedAttempt, so it cannot
// reach a sibling's conversation.
func (s *Service) AgentCommentOn(ctx context.Context, agentID uuid.UUID, ticketID int64, attempt *int, body string) (*Comment, error) {
	if body == "" {
		return nil, ErrEmptyComment
	}
	by := AgentAuthor(agentID)
	if s.owners != nil {
		bound, err := s.owners.ResolveOwner(ctx, agentID)
		if err != nil && !errors.Is(err, ErrOwnerBoundRefused) {
			return nil, err
		}
		if err == nil && bound != nil {
			by.speaksFor = bound
			body = bound.annotate(body)
		}
		if err != nil && attempt != nil {
			return nil, err
		}
	}
	if by.speaksFor == nil {
		// A worker's attempt is the one its lease is on, and a number it
		// supplied could only be a way to reach a sibling.
		if attempt != nil {
			return nil, ErrWorkerNamedAttempt
		}
		return s.store.AddComment(ctx, ticketID, by, body)
	}
	var c *Comment
	err := s.store.InTransition(ctx, func(tx TransitionTx) error {
		q := tx.Querier()
		held, err := heldAttempt(ctx, q, agentID, ticketID)
		if err != nil && !errors.Is(err, ErrNoLease) {
			return err
		}
		var addressed *AttemptRef
		if held != nil {
			// A session that also works one of the ticket's attempts keeps to
			// it, as a worker does: its lease names the attempt, and naming a
			// sibling's would reach past it.
			if attempt != nil && *attempt != held.Number {
				return ErrWorkerNamedAttempt
			}
		} else if attempt != nil {
			// None named is not ambiguous for a comment: with several live
			// attempts it is the ticket's, and reaches every one of them.
			if addressed, err = ResolveAttempt(ctx, q, ticketID, attempt); err != nil {
				return err
			}
		}
		c, err = tx.Comment(ctx, ticketID, &NewComment{By: by, Body: body, Attempt: addressed})
		return err
	})
	return c, err
}

// AgentAddDependency is AddDependency for an agent caller. A worker follows
// agentMayEditBlockers. An owner-bound session may add a blocker to any
// ticket in its tenant, whatever its status or holder, and is recorded as
// acting for its owner.
//
// This is the one direct power an owner-bound session has over tickets.
// Approvals, review decisions, requires_approval, credentials, membership
// and settings stay with the person, so nothing here widens beyond blockers.
func (s *Service) AgentAddDependency(ctx context.Context, agentID uuid.UUID, ticketID, dependsOnID int64) (*DependencyChange, error) {
	return s.agentDependency(ctx, agentID, ticketID, dependsOnID, "add", s.store.AddDependency)
}

// AgentRemoveDependency is RemoveDependency for an agent caller; see
// AgentAddDependency. An owner-bound session may lift any blocker.
func (s *Service) AgentRemoveDependency(ctx context.Context, agentID uuid.UUID, ticketID, dependsOnID int64) (*DependencyChange, error) {
	return s.agentDependency(ctx, agentID, ticketID, dependsOnID, "remove", s.store.RemoveDependency)
}

type dependencyWrite func(ctx context.Context, by Author, ticketID, dependsOnID int64) (*DependencyChange, error)

func (s *Service) agentDependency(ctx context.Context, agentID uuid.UUID, ticketID, dependsOnID int64, op string, write dependencyWrite) (*DependencyChange, error) {
	by := AgentAuthor(agentID)
	var bound *OwnerBound
	if s.owners != nil {
		var err error
		if bound, err = s.owners.ResolveOwner(ctx, agentID); err != nil {
			// Only a refusal is an event; a failed lookup is just an error.
			if errors.Is(err, ErrOwnerBoundRefused) {
				s.recordOwnerBound(ctx, audit.OwnerBoundBlockerRefused, agentID, nil, op, ticketID, dependsOnID, "reason", err.Error())
			}
			return nil, err
		}
	}
	by.onBehalf = bound
	change, err := write(ctx, by, ticketID, dependsOnID)
	if err != nil {
		return nil, err
	}
	if bound != nil && change.Changed {
		s.recordOwnerBound(ctx, audit.OwnerBoundBlockerChanged, agentID, bound, op, ticketID, dependsOnID)
	}
	return change, nil
}

func (s *Service) recordOwnerBound(ctx context.Context, action audit.Action, agentID uuid.UUID, bound *OwnerBound, op string, ticketID, blockerID int64, extra ...any) {
	if s.events == nil {
		return
	}
	attrs := []any{"agent_id", agentID, "ticket", ticketID, "blocker", blockerID, "op", op}
	if tenant, err := db.TenantFrom(ctx); err == nil {
		attrs = append(attrs, "tenant_id", tenant)
	}
	if bound != nil {
		attrs = append(attrs, "on_behalf_of_user_id", bound.UserID)
	}
	s.events.Record(ctx, action, append(attrs, extra...)...)
}
