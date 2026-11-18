package pendingaction_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestDiscussionPosterAnswersAsTheOwnerAndRequeues(t *testing.T) {
	d := newDecider(t)
	id := d.ticketIn(t, ticket.StatusDiscussion)
	poster := pendingaction.NewDiscussionPoster(d.tickets)

	require.NoError(t, poster.Post(authn.NewUserContext(d.ctx, d.approver), id, 1, "carry on", ""))

	assert.Equal(t, ticket.StatusOpen, d.ticketStatus(t, id), "re-queued, as an approved discussion_answer proposal does")
	comments := d.comments(t, id)
	require.Len(t, comments, 1)
	require.NotNil(t, comments[0].AuthorUserID)
	assert.Equal(t, d.approver.ID, *comments[0].AuthorUserID)
	assert.Nil(t, comments[0].AuthorAgentID)
	assert.Equal(t, "carry on\n\n_Drafted by the steward._", comments[0].Body)
}

func TestDiscussionPosterNeedsAPerson(t *testing.T) {
	d := newDecider(t)
	id := d.ticketIn(t, ticket.StatusDiscussion)

	err := pendingaction.NewDiscussionPoster(d.tickets).Post(d.ctx, id, 1, "carry on", "")

	assert.ErrorIs(t, err, authn.ErrNotPerson)
	assert.Equal(t, ticket.StatusDiscussion, d.ticketStatus(t, id))
	assert.Empty(t, d.comments(t, id))
}

func TestDiscussionPosterPostsServerWorkAsTheTenantOwner(t *testing.T) {
	d := newDecider(t)
	id := d.ticketIn(t, ticket.StatusDiscussion)
	poster := pendingaction.NewDiscussionPoster(d.tickets).WithServerOwners(&fixedOwner{id: d.approver.ID})

	require.NoError(t, poster.Post(authn.AsServer(d.ctx), id, 1, "carry on", ""))

	assert.Equal(t, ticket.StatusOpen, d.ticketStatus(t, id))
	comments := d.comments(t, id)
	require.Len(t, comments, 1)
	require.NotNil(t, comments[0].AuthorUserID)
	assert.Equal(t, d.approver.ID, *comments[0].AuthorUserID)
}

func TestDiscussionPosterRefusesServerWorkWithoutOwners(t *testing.T) {
	d := newDecider(t)
	id := d.ticketIn(t, ticket.StatusDiscussion)

	err := pendingaction.NewDiscussionPoster(d.tickets).Post(authn.AsServer(d.ctx), id, 1, "carry on", "")

	assert.ErrorIs(t, err, authn.ErrNotPerson)
	assert.Empty(t, d.comments(t, id))
}

// Posting again under the key of a post that landed writes nothing more, and
// does not act on the ticket again: it left discussion the first time.
func TestDiscussionPosterRepeatedKeyPostsOnce(t *testing.T) {
	d := newDecider(t)
	id := d.ticketIn(t, ticket.StatusDiscussion)
	poster := pendingaction.NewDiscussionPoster(d.tickets)
	ctx := authn.NewUserContext(d.ctx, d.approver)

	require.NoError(t, poster.Post(ctx, id, 1, "carry on", "decision-1"))
	require.NoError(t, poster.Post(ctx, id, 1, "carry on", "decision-1"))
	assert.Len(t, d.comments(t, id), 1)

	require.NoError(t, poster.Post(ctx, id, 1, "carry on", "decision-2"))
	assert.Len(t, d.comments(t, id), 2, "another key is another comment")
}
