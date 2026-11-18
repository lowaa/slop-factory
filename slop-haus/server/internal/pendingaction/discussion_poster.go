package pendingaction

import (
	"context"
	"fmt"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// stewardName is who the footer of an answer the owner posted with the
// steward's help says drafted it.
const stewardName = "the steward"

// CommentPoster is the ticket service's human comment; *ticket.Service is one.
type CommentPoster interface {
	PostComment(ctx context.Context, hc *ticket.HumanComment) (*ticket.HumanCommentResult, error)
}

// DiscussionPoster posts a discussion answer as the signed-in owner in ctx,
// the way an approved discussion_answer proposal does: the same comment, the
// same "drafted by" footer, so the ticket leaves discussion alike. It
// implements discussionanswer.Poster.
//
// Work the server does itself (authn.AsServer in ctx, as the steward's sweep
// does) has no person: it posts as the tenant's owner, as ApproveForSteward
// approves, when the poster was given owners.
type DiscussionPoster struct {
	tickets CommentPoster
	owners  TenantOwners
}

func NewDiscussionPoster(tickets CommentPoster) *DiscussionPoster {
	return &DiscussionPoster{tickets: tickets}
}

// WithServerOwners lets the server's own work post as the tenant's owner.
func (p *DiscussionPoster) WithServerOwners(owners TenantOwners) *DiscussionPoster {
	p.owners = owners
	return p
}

// author is the person the answer is posted as, in a ctx that carries them.
func (p *DiscussionPoster) author(ctx context.Context) (context.Context, *authn.User, error) {
	if authn.IsServer(ctx) && p.owners != nil {
		tenant, err := db.TenantFrom(ctx)
		if err != nil {
			return nil, nil, err
		}
		owner, err := p.owners.TenantOwner(ctx, tenant)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve the tenant's owner: %w", err)
		}
		who := &authn.User{ID: owner, TenantID: tenant}
		return authn.NewUserContext(ctx, who), who, nil
	}
	who := authn.PersonFromContext(ctx)
	if err := authn.RequirePerson(who); err != nil {
		return nil, nil, err
	}
	return ctx, who, nil
}

// Post is repeatable under key: a second call with the same key posts nothing,
// so a caller unsure whether the first landed can call again.
func (p *DiscussionPoster) Post(ctx context.Context, ticketID int64, attempt int, body, key string) error {
	ctx, who, err := p.author(ctx)
	if err != nil {
		return err
	}
	// Affinity is the ticket service's own (PostComment sets it).
	hc := discussionAnswer(ticketID, &ticket.AttemptRef{TicketID: ticketID, Number: attempt}, who.ID, body, stewardName, 0)
	hc.IdempotencyKey = key
	if _, err := p.tickets.PostComment(ctx, hc); err != nil {
		return fmt.Errorf("post discussion answer: %w", err)
	}
	return nil
}
