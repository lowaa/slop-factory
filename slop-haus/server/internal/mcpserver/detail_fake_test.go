package mcpserver_test

import (
	"context"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// DetailFor assembles the fake's ticket, comments, handoffs and revisions the
// way ticket.Service.DetailFor does, so tests set each part on its own.
func (f *fakeTickets) DetailFor(ctx context.Context, id int64, _ uuid.UUID) (*ticket.Detail, error) {
	found, err := f.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	detail := &ticket.Detail{Ticket: found, Comments: f.comments, Revisions: f.revisions, External: f.external, Held: f.held, Branch: f.branch}
	if len(f.contexts) > 0 {
		detail.Handoff = &f.contexts[len(f.contexts)-1]
	}
	return detail, nil
}
