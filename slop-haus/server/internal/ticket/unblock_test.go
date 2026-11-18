package ticket_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Blocked-ness is a predicate re-evaluated whenever what it depends on
// changes. These pin each way a dependency can stop being pending; before
// them, nothing re-evaluated it and a blocked ticket was stranded for good.

// blockedOn has b hold dep and a release waiting blocked on it.
func blockedOn(t *testing.T, f *fixture) (a, b uuid.UUID, dep, waiting *ticket.Ticket) {
	t.Helper()
	agentA, agentB := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	dep = f.ticket(t, "Dependency", "web/")
	waiting = f.ticket(t, "Waiting", "server/")
	_, err := f.svc.Claim(f.ctx, agentB, &dep.ID)
	require.NoError(t, err)
	_, err = f.svc.Claim(f.ctx, agentA, &waiting.ID)
	require.NoError(t, err)
	released, err := f.svc.Release(f.ctx, agentA, "needs the dependency", &dep.ID)
	require.NoError(t, err)
	require.Equal(t, ticket.StatusBlocked, released.Status)
	return agentA, agentB, dep, waiting
}

func (f *fixture) status(t *testing.T, id int64) ticket.Status {
	t.Helper()
	got, err := f.svc.Get(f.ctx, id)
	require.NoError(t, err)
	return got.Status
}

func TestUnblock_WhenTheDependencyIsDone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b, _, waiting := blockedOn(t, f)

	_, err := f.svc.Done(f.ctx, b, &ticket.DoneParams{Summary: "finished"})
	require.NoError(t, err)

	assert.Equal(t, ticket.StatusOpen, f.status(t, waiting.ID))
	again, err := f.svc.Claim(f.ctx, a, nil)
	require.NoError(t, err)
	require.NotNil(t, again.Ticket, "claimable again, not stranded")
	assert.Equal(t, waiting.ID, again.Ticket.ID)
}

func TestUnblock_WhenTheDependencyIsCancelledOnTheBoard(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, _, dep, waiting := blockedOn(t, f)

	cancelled := ticket.StatusCancelled
	_, err := f.svc.Update(f.ctx, dep.ID, &ticket.UpdateParams{Status: &cancelled})
	require.NoError(t, err)

	assert.Equal(t, ticket.StatusOpen, f.status(t, waiting.ID))
}

func TestUnblock_WhenTheHumanRemovesTheDependency(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, _, dep, waiting := blockedOn(t, f)

	_, err := f.svc.RemoveDependency(f.ctx, ticket.UserAuthor(f.human), waiting.ID, dep.ID)
	require.NoError(t, err)

	assert.Equal(t, ticket.StatusOpen, f.status(t, waiting.ID))
}

// Only the satisfied ones: a ticket with another unfinished dependency
// stays blocked.
func TestUnblock_LeavesATicketWithAnotherPendingDependencyBlocked(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, b, _, waiting := blockedOn(t, f)
	other := f.ticket(t, "Another dependency", "docs/")
	f.blockedBy(t, waiting.ID, other.ID)

	_, err := f.svc.Done(f.ctx, b, &ticket.DoneParams{Summary: "finished"})
	require.NoError(t, err)

	assert.Equal(t, ticket.StatusBlocked, f.status(t, waiting.ID))
}
