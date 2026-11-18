package pendingaction

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// editParams are the ticket's own parameters for the paths and priority an
// edit proposal changes; its title and body go through ticket.EditParams.
func (e *EditPayload) updateParams() *ticket.UpdateParams {
	params := &ticket.UpdateParams{Priority: e.Priority}
	if e.Paths != nil {
		params.Paths = make([]model.Path, len(e.Paths))
		for i, p := range e.Paths {
			params.Paths[i] = model.Path{Path: p, Source: model.PathSourceFiled}
		}
	}
	return params
}

// filedPaths are the paths an edit replaces: the ones filed, not the ones an
// agent was later granted.
func filedPaths(t *ticket.Ticket) []string {
	var out []string
	for _, p := range t.Paths {
		if p.Source == model.PathSourceFiled {
			out = append(out, p.Path)
		}
	}
	return out
}

func samePaths(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a, b = slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b))
	return slices.Equal(a, b)
}

// drift is, for what an edit replaces without a revision to guard it, why
// the ticket no longer matches what it was proposed against.
func (e *EditPayload) drift(live *ticket.Ticket) string {
	var what []string
	if e.Priority != nil && (e.BasePriority == nil || *e.BasePriority != live.Priority) {
		what = append(what, "its priority was changed")
	}
	if e.Paths != nil && !samePaths(e.BasePaths, filedPaths(live)) {
		what = append(what, "its paths were changed")
	}
	return strings.Join(what, "; ")
}

func (e *EditPayload) changesFields() bool { return e.Paths != nil || e.Priority != nil }

func (e *EditPayload) changesText() bool { return e.Title != nil || e.Body != nil }

// prepareEdit holds a proposal's paths to the ticket's rules for them and
// stores them as normalised, so the card shows what would be written.
func (s *Service) prepareEdit(ctx context.Context, ticketID int64, p Payload) (Payload, error) {
	if s.tickets == nil {
		return Payload{}, ErrNotConfigured
	}
	edit := *p.Edit
	if edit.changesFields() {
		live, err := s.tickets.Get(ctx, ticketID)
		if errors.Is(err, ticket.ErrNotFound) {
			return Payload{}, fmt.Errorf("%w: #%d", ErrTicketNotFound, ticketID)
		}
		if err != nil {
			return Payload{}, err
		}
		edit.BasePaths = nil
		edit.BasePriority = nil
		if edit.Priority != nil {
			base := live.Priority
			edit.BasePriority = &base
		}
		if edit.Paths != nil {
			edit.BasePaths = filedPaths(live)
		}
	}
	if edit.Paths != nil {
		params := edit.updateParams()
		if err := s.tickets.PrepareUpdate(ctx, params); err != nil {
			return Payload{}, fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		edit.Paths = make([]string, len(params.Paths))
		for i, path := range params.Paths {
			edit.Paths[i] = path.Path
		}
	}
	return Payload{Edit: &edit}, nil
}

// prepareApproval runs, ahead of the approval's transaction, the check an
// edit's paths meet before the board's own, which reads the workspace through
// a connection of its own. A proposal it refuses is settled as failed, as
// one the ticket's rules refuse inside the transaction is.
func (s *Service) prepareApproval(ctx context.Context, approver *authn.User, id uuid.UUID, mode approvalMode) (*Action, error) {
	a, err := s.store.Get(ctx, id, s.now())
	if err != nil {
		return nil, err
	}
	if a.Kind != KindEdit || a.State != StatePending || a.Payload.Edit.Paths == nil {
		return nil, nil
	}
	refusal := s.tickets.PrepareUpdate(ctx, a.Payload.Edit.updateParams())
	if refusal == nil {
		return nil, nil
	}
	settled, err := s.store.SettleOwn(ctx, id, StateFailed, refusal.Error(), approver.ID, s.now())
	if err != nil {
		return nil, err
	}
	s.refused(ctx, approver, settled, mode)
	return settled, fmt.Errorf("%w: %w", ErrRefused, refusal)
}

// applyEdit makes the person's edit through the ticket's own code: Edit for
// the title and body, which refuses a ticket edited since the proposal was
// made, and the board's field edit for the paths and priority. With Open the
// move out of preparing follows in the same transaction, so a refused edit
// leaves the ticket in preparing.
func (s *Service) applyEdit(ctx context.Context, tx ticket.TransitionTx, approver uuid.UUID, a *Action) error {
	e := a.Payload.Edit
	if err := s.applyEditFields(ctx, tx, approver, a); err != nil {
		return err
	}
	if e.Open {
		_, err := ticket.MarkTicketReady(ctx, tx, a.ticketID())
		return err
	}
	return nil
}

func (s *Service) applyEditFields(ctx context.Context, tx ticket.TransitionTx, approver uuid.UUID, a *Action) error {
	e := a.Payload.Edit
	if e.changesText() {
		base := e.BaseRevision
		if _, err := ticket.EditTicketText(ctx, tx, a.ticketID(), approver,
			&ticket.EditParams{Title: e.Title, Body: e.Body, BaseRevision: &base}); err != nil {
			return err
		}
	}
	if e.changesFields() {
		return ticket.UpdateInTx(ctx, tx, a.ticketID(), e.updateParams())
	}
	return nil
}
