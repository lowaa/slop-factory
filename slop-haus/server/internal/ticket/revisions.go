package ticket

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

// EditParams is a change to a ticket's title and/or body. Nil leaves a field
// alone; at least one must be set.
type EditParams struct {
	Title *string
	Body  *string
	// BaseRevision, when set, is the ticket's Revision the editor started
	// from. An edit against an older one is refused with ErrStaleRevision
	// rather than overwriting whatever changed in between.
	BaseRevision *int
}

// EditResult is the ticket after an edit, and whether the edit changed
// anything: one that only restates the current text records no revision,
// and is not progress either.
type EditResult struct {
	Ticket  *Ticket
	Changed bool
}

// validTitle is the one rule a title is held to, at creation and on every
// edit alike.
func validTitle(title string) bool {
	return strings.TrimSpace(title) != ""
}

func (p *EditParams) validate() error {
	if p.Title == nil && p.Body == nil {
		return ErrEmptyEdit
	}
	if p.Title != nil && !validTitle(*p.Title) {
		return ErrEmptyTitle
	}
	return nil
}

// Edit is a person changing any ticket's title or body. The change is
// recorded as a revision with the full old and new text, so the fleet can
// see what a ticket used to say, and who changed it.
func (s *Service) Edit(ctx context.Context, id int64, userID uuid.UUID, params *EditParams) (*EditResult, error) {
	if err := params.validate(); err != nil {
		return nil, err
	}
	return s.store.EditText(ctx, id, UserAuthor(userID), params)
}

// EditHeld is an agent changing the title or body of the ticket it holds,
// as it learns more about the work. Only that ticket: there is no way to
// name another, so one agent cannot rewrite the spec another is building
// from, or queued work nobody has started. Recorded exactly as Edit is.
func (s *Service) EditHeld(ctx context.Context, agentID uuid.UUID, params *EditParams) (*EditResult, error) {
	if err := params.validate(); err != nil {
		return nil, err
	}
	return s.store.EditHeld(ctx, agentID, params)
}

// ListRevisions returns a ticket's edits, oldest first.
func (s *Service) ListRevisions(ctx context.Context, ticketID int64) ([]Revision, error) {
	return s.store.ListRevisions(ctx, ticketID)
}

// ListStatusChanges returns a ticket's status changes, oldest first.
func (s *Service) ListStatusChanges(ctx context.Context, ticketID int64) ([]StatusChange, error) {
	return s.store.ListStatusChanges(ctx, ticketID)
}
