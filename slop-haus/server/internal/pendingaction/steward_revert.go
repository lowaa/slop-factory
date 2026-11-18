package pendingaction

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// ErrNotRevertable is an approval the steward made that cannot be taken back:
// the kind has no inverse, or the ticket has moved on since, so reverting
// would undo someone else's work. Nothing is changed.
var ErrNotRevertable = errors.New("pending action: the steward's approval can no longer be taken back")

func notRevertable(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrNotRevertable, fmt.Sprintf(format, args...))
}

// RevertStewardApproval takes back what the steward applied for the proposal,
// as the owner by: a discussion answer that re-queued the ticket puts it back
// in discussion, a ready ticket goes back to preparing, and an edit is
// reverted to what it replaced. Each is refused with ErrNotRevertable unless
// the ticket is still as the approval left it and no agent holds it, so a
// revert never overwrites later work. The tenant is in ctx.
func (s *Service) RevertStewardApproval(ctx context.Context, by *authn.User, id uuid.UUID) error {
	if err := authn.RequirePerson(by); err != nil {
		return err
	}
	if s.tickets == nil {
		return ErrNotConfigured
	}
	a, err := s.store.Get(ctx, id, s.now())
	if err != nil {
		return err
	}
	if a.State != StateApproved || a.Outcome != OutcomeStewardApproved || a.TicketID == nil {
		return notRevertable("it was not applied by the steward")
	}
	var restore *ticket.UpdateParams
	if a.Kind == KindEdit && a.Payload.Edit.changesFields() {
		restore = a.Payload.Edit.restoreParams()
		if restore != nil && restore.Paths != nil {
			if err := s.tickets.PrepareUpdate(ctx, restore); err != nil {
				return notRevertable("the previous paths are no longer acceptable: %v", err)
			}
		}
	}
	return s.tickets.InTransition(ctx, func(tx ticket.TransitionTx) error {
		ctx := ticket.WithCause(ticket.WithUser(ctx, by.ID), StewardUndoCauseFor(a.ID))
		basis, held, err := s.store.LiveBasis(ctx, tx.Querier(), a.ticketID(), a.Attempt)
		if err != nil {
			return err
		}
		if held {
			return notRevertable("an agent now holds the ticket")
		}
		// A status alone cannot tell a ticket left as the approval made it from
		// one that moved on and came back, so the approval's own move must
		// still be the latest.
		if a.Kind == KindReady || a.Kind == KindDiscussionAnswer || (a.Kind == KindEdit && a.Payload.Edit.Open) {
			var cause *string
			err := tx.Querier().QueryRow(ctx,
				`SELECT cause FROM ticket_status_changes WHERE ticket_id = $1 ORDER BY id DESC LIMIT 1`, a.ticketID()).Scan(&cause)
			if err != nil {
				return fmt.Errorf("read the ticket's last move: %w", err)
			}
			if cause == nil || *cause != StewardCauseFor(a.ID) {
				return notRevertable("the ticket has moved since")
			}
		}
		note := func(what string) error {
			_, err := tx.Comment(ctx, a.ticketID(), &ticket.NewComment{
				By: ticket.UserAuthor(by.ID), Body: "Taken back: the steward's " + what + " was undone by the owner.",
			})
			return err
		}
		switch a.Kind {
		case KindReady:
			if basis.Status != ticket.StatusOpen {
				return notRevertable("the ticket is %s, not open", basis.Status)
			}
			if _, err := ticket.MoveTicketTo(ctx, tx, a.ticketID(), ticket.StatusPreparing); err != nil {
				return err
			}
			return note("ready")
		case KindDiscussionAnswer:
			if basis.Status != ticket.StatusOpen {
				return notRevertable("the ticket is %s, not open", basis.Status)
			}
			// The answer was to an attempt's discussion: it is that attempt
			// that waits on the person again.
			moving := tx
			if ref := a.attemptRef(); ref != nil {
				moving = tx.ForAttempt(ref)
			}
			if _, err := ticket.MoveTicketTo(ctx, moving, a.ticketID(), ticket.StatusDiscussion); err != nil {
				return err
			}
			return note("answer")
		case KindEdit:
			if err := s.revertEdit(ctx, tx, by, a, basis, restore); err != nil {
				return err
			}
			return note("edit")
		}
		return notRevertable("a %s proposal has no inverse", a.Kind)
	})
}

// StewardUndoCauseFor is CauseFor for a status change made by taking back the
// steward's approval of a proposal.
func StewardUndoCauseFor(id uuid.UUID) string { return CauseFor(id) + " steward_undone" }

// restoreParams puts back what the edit replaced.
func (e *EditPayload) restoreParams() *ticket.UpdateParams {
	if e.BasePriority == nil && len(e.BasePaths) == 0 {
		return nil
	}
	back := &EditPayload{Priority: e.BasePriority, Paths: e.BasePaths}
	return back.updateParams()
}

// revertEdit restores the title and body from the revision the edit recorded,
// and the priority and paths from the proposal's bases, each only while the
// ticket still holds what the edit wrote.
func (s *Service) revertEdit(ctx context.Context, tx ticket.TransitionTx, by *authn.User, a *Action, basis *Basis, restore *ticket.UpdateParams) error {
	e := a.Payload.Edit
	if e.Paths != nil && len(e.BasePaths) == 0 {
		return notRevertable("the ticket had no filed paths to put back")
	}
	live, err := tx.Ticket(ctx, a.ticketID())
	if err != nil {
		return err
	}
	if e.Open && live.Status != ticket.StatusOpen {
		return notRevertable("the ticket is %s, not open", live.Status)
	}
	if e.changesText() && basis.Revision != e.BaseRevision {
		if basis.Revision != e.BaseRevision+1 {
			return notRevertable("its title or text was edited since")
		}
		var oldTitle, oldBody, newTitle, newBody *string
		var editor *uuid.UUID
		if err := tx.Querier().QueryRow(ctx, `
			SELECT old_title, old_body, new_title, new_body, editor_user_id FROM ticket_revisions WHERE ticket_id = $1 AND revision = $2`,
			a.ticketID(), basis.Revision).Scan(&oldTitle, &oldBody, &newTitle, &newBody, &editor); err != nil {
			return fmt.Errorf("read the edit's revision: %w", err)
		}
		// The revision must be this edit's, not a later edit by the same
		// owner after an edit that changed nothing and recorded none.
		if editor == nil || a.DecidedByUserID == nil || *editor != *a.DecidedByUserID ||
			!equalPtr(e.Title, newTitle) || !equalPtr(e.Body, newBody) {
			return notRevertable("its title or text was edited since")
		}
		current := basis.Revision
		if _, err := ticket.EditTicketText(ctx, tx, a.ticketID(), by.ID,
			&ticket.EditParams{Title: oldTitle, Body: oldBody, BaseRevision: &current}); err != nil {
			return err
		}
	}
	if e.Priority != nil && live.Priority != *e.Priority {
		return notRevertable("its priority was changed since")
	}
	if e.Paths != nil && !samePaths(e.Paths, filedPaths(live)) {
		return notRevertable("its paths were changed since")
	}
	if restore != nil {
		if err := ticket.UpdateInTx(ctx, tx, a.ticketID(), restore); err != nil {
			return err
		}
	}
	if e.Open {
		if _, err := ticket.MoveTicketTo(ctx, tx, a.ticketID(), ticket.StatusPreparing); err != nil {
			return err
		}
	}
	return nil
}
