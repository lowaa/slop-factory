package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// stewardComments posts the steward's comments on tickets, authored as the
// steward agent that made the decision.
type stewardComments struct{ tickets *ticket.Service }

func (c *stewardComments) CommentAsSteward(ctx context.Context, stewardID uuid.UUID, ticketID int64, body string) error {
	return c.tickets.InTransition(ctx, func(tx ticket.TransitionTx) error {
		_, err := tx.Comment(ctx, ticketID, &ticket.NewComment{By: ticket.AgentAuthor(stewardID), Body: body})
		return err
	})
}

// stewardLinks points the owner at the ticket, with the proposal and the
// action to take, in the tenant the decision is in.
func stewardLinks(boardURL string) steward.Links {
	return func(ctx context.Context, kind steward.LinkKind, rec *steward.Record) string {
		tenant, err := db.TenantFrom(ctx)
		if err != nil || rec.TicketID == nil {
			return ""
		}
		q := url.Values{"tenant": {tenant.String()}, "steward_" + string(kind): {rec.PendingActionID.String()}}
		return fmt.Sprintf("%s/tickets/%d?%s", strings.TrimRight(boardURL, "/"), *rec.TicketID, q.Encode())
	}
}

// stewardRevert reverts through the proposal service, naming the outcome the
// controls act on.
type stewardRevert struct{ pending *pendingaction.Service }

func (r *stewardRevert) RevertStewardApproval(ctx context.Context, by *authn.User, id uuid.UUID) error {
	err := r.pending.RevertStewardApproval(ctx, by, id)
	if errors.Is(err, pendingaction.ErrNotRevertable) {
		return fmt.Errorf("%w: %w", steward.ErrNotRevertable, err)
	}
	return err
}

// stewardControlsAdapter answers the steward's controls in the proposal
// service's terms, which the approvals routes map to responses.
type stewardControlsAdapter struct{ controls *steward.Controls }

func (a *stewardControlsAdapter) Veto(ctx context.Context, owner *authn.User, id uuid.UUID) error {
	return mapStewardControlErr(a.controls.Veto(ctx, owner, id))
}

func (a *stewardControlsAdapter) Undo(ctx context.Context, owner *authn.User, id uuid.UUID) error {
	return mapStewardControlErr(a.controls.Undo(ctx, owner, id))
}

func mapStewardControlErr(err error) error {
	switch {
	case errors.Is(err, steward.ErrNotFound):
		return fmt.Errorf("%w: the steward made no decision on it", pendingaction.ErrNotFound)
	case errors.Is(err, steward.ErrNotOwner):
		return fmt.Errorf("%w: %w", pendingaction.ErrNotApprover, err)
	case errors.Is(err, steward.ErrNotApplicable):
		return fmt.Errorf("%w: %w", pendingaction.ErrNotPending, err)
	}
	return err
}
