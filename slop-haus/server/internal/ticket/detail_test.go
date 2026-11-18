package ticket_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestDetail_IsTheTicketWithItsCommentsLatestHandoffAndEdits(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Loose ticket", "server/")

	f.claim(t, a)
	_, err := f.svc.Discuss(f.ctx, a, "First question?", "First handoff.")
	require.NoError(t, err)
	f.humanReply(t, created.ID)
	f.claim(t, a)
	_, err = f.svc.Discuss(f.ctx, a, "Second question?", "Second handoff.")
	require.NoError(t, err)
	_, err = f.svc.Edit(f.ctx, created.ID, f.human, &ticket.EditParams{Title: ptr("Tight ticket")})
	require.NoError(t, err)

	detail, err := f.svc.Detail(f.ctx, created.ID)
	require.NoError(t, err)

	assert.Equal(t, created.ID, detail.Ticket.ID)
	assert.Equal(t, "Tight ticket", detail.Ticket.Title)
	assert.Equal(t, ticket.StatusDiscussion, detail.Ticket.Status)

	bodies := make([]string, 0, len(detail.Comments))
	for _, c := range detail.Comments {
		bodies = append(bodies, c.Body)
	}
	assert.Equal(t, []string{"First question?", "Go with the flat shape.", "Second question?"}, bodies, "oldest first")

	require.NotNil(t, detail.Handoff)
	assert.Equal(t, "Second handoff.", detail.Handoff.Body, "only the latest handoff")
	assert.Equal(t, a, detail.Handoff.AgentID)

	require.Len(t, detail.Revisions, 1)
	assert.Equal(t, ptr("Tight ticket"), detail.Revisions[0].NewTitle)
}

func TestDetail_ATicketNobodyHandedOffHasNoHandoff(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Fresh ticket")

	detail, err := f.svc.Detail(f.ctx, created.ID)
	require.NoError(t, err)
	assert.Nil(t, detail.Handoff)
	assert.Empty(t, detail.Comments)
	assert.Empty(t, detail.Revisions)
}

func TestDetail_AnUnknownTicketIsNotFound(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.svc.Detail(f.ctx, 404)
	assert.ErrorIs(t, err, ticket.ErrNotFound)
}

var errDetailRead = errors.New("read failed")

// failingDetailReads is the real store with one of Detail's history reads
// failing.
type failingDetailReads struct {
	ticket.Store
	fail string
}

func (s *failingDetailReads) ListComments(ctx context.Context, id int64) ([]ticket.Comment, error) {
	if s.fail == "comments" {
		return nil, errDetailRead
	}
	return s.Store.ListComments(ctx, id)
}

func (s *failingDetailReads) ListCommentsIn(ctx context.Context, id int64, conv ticket.Conversation) ([]ticket.Comment, error) {
	if s.fail == "comments" {
		return nil, errDetailRead
	}
	return s.Store.ListCommentsIn(ctx, id, conv)
}

func (s *failingDetailReads) ListAgentContextsIn(ctx context.Context, id int64, conv ticket.Conversation) ([]ticket.AgentContext, error) {
	if s.fail == "handoffs" {
		return nil, errDetailRead
	}
	return s.Store.ListAgentContextsIn(ctx, id, conv)
}

func (s *failingDetailReads) ListAgentContexts(ctx context.Context, id int64) ([]ticket.AgentContext, error) {
	if s.fail == "handoffs" {
		return nil, errDetailRead
	}
	return s.Store.ListAgentContexts(ctx, id)
}

func (s *failingDetailReads) ListRevisions(ctx context.Context, id int64) ([]ticket.Revision, error) {
	if s.fail == "revisions" {
		return nil, errDetailRead
	}
	return s.Store.ListRevisions(ctx, id)
}

// A read that fails fails the whole view: a ticket shown without its
// handoff reads as one nobody handed off.
func TestDetail_AFailedReadFailsTheView(t *testing.T) {
	t.Parallel()
	for _, fail := range []string{"comments", "handoffs", "revisions"} {
		t.Run(fail, func(t *testing.T) {
			f := newFixture(t)
			created := f.ticket(t, "Loose ticket")
			store := &failingDetailReads{Store: newStore(f.database), fail: fail}
			svc := ticket.NewService(store, slog.DiscardHandler)

			detail, err := svc.Detail(f.ctx, created.ID)
			assert.ErrorIs(t, err, errDetailRead)
			assert.Nil(t, detail)
		})
	}
}
