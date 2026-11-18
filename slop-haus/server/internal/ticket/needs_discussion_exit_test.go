package ticket_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// A ticket claimed while it needs discussion can be neither closed nor put
// into review: either refusal writes nothing and leaves the ticket held,
// and ticket_discuss -- which clears the flag -- is still the way out.
func TestDoneAndReview_RefuseATicketThatStillNeedsDiscussion(t *testing.T) {
	t.Parallel()
	for name, exit := range map[string]func(f *fixture, agentID uuid.UUID) error{
		"done": func(f *fixture, agentID uuid.UUID) error {
			_, err := f.svc.Done(f.ctx, agentID, &ticket.DoneParams{Summary: "built it anyway", PRURL: reviewPR})
			return err
		},
		"review": func(f *fixture, agentID uuid.UUID) error {
			_, err := f.svc.Review(f.ctx, agentID, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: reviewPR, Handoff: "PR open"})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			a := f.agent(t, "planner")
			created, err := f.svc.Create(f.ctx, &ticket.CreateParams{
				Title: "Rough idea", NeedsDiscussion: true, Paths: []model.Path{{Path: "server/"}},
			})
			require.NoError(t, err)
			f.claim(t, a)

			err = exit(f, a)

			require.ErrorIs(t, err, ticket.ErrNeedsDiscussion)
			held, err := f.svc.LeaseFor(f.ctx, a)
			require.NoError(t, err)
			assert.Equal(t, created.ID, held.TicketID, "still held, to discuss")
			got := f.get(t, created.ID)
			assert.Equal(t, ticket.StatusInProgress, got.Status)
			assert.True(t, got.NeedsDiscussion)
			assert.Nil(t, got.PRURL, "the refused change's link is not recorded")
			comments, err := f.svc.ListComments(f.ctx, created.ID)
			require.NoError(t, err)
			assert.Empty(t, comments)

			discussed, err := f.svc.Discuss(f.ctx, a, "Which shape?", "plan posted")
			require.NoError(t, err, "discussing is always open")
			assert.Equal(t, ticket.StatusDiscussion, discussed.Status)
			assert.False(t, discussed.NeedsDiscussion)
		})
	}
}
