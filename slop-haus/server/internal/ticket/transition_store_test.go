package ticket_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// All of it or none of it, where it can actually go wrong: a transition
// writes its comment and ends the lease before the handoff, and a handoff
// the database refuses (its CHECK forbids an empty one) must take the
// comment and the release back out with it. The service would never send
// one; this goes to the store directly to make the late failure happen.
func TestOnHeldLease_ALateFailureRollsBackEveryEarlierWrite(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Loose ticket", "server/")
	f.claim(t, a)

	err := newStore(f.database).OnHeldLease(f.ctx, a,
		func(tx ticket.TransitionTx, lease *ticket.HeldLease) error {
			if _, err := tx.Comment(f.ctx, lease.TicketID, &ticket.NewComment{By: ticket.AgentAuthor(a), Body: "question?"}); err != nil {
				return err
			}
			if err := tx.EndLease(f.ctx, lease.ID, "discussion"); err != nil {
				return err
			}
			return tx.Handoff(f.ctx, lease.TicketID, a, "")
		})

	require.Error(t, err)
	comments, err := f.svc.ListComments(f.ctx, created.ID)
	require.NoError(t, err)
	assert.Empty(t, comments, "the comment went back out with the failed handoff")
	held, err := f.svc.LeaseFor(f.ctx, a)
	require.NoError(t, err)
	assert.Equal(t, created.ID, held.TicketID, "the agent still holds its ticket")
	assert.Equal(t, ticket.StatusInProgress, f.status(t, created.ID))
}
