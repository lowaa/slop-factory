package conformance

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

var statusTests = []test{
	{"Status/EveryTransitionAsTheTableSays", testEveryTransition},
	{"Status/OnlyTheUserReopens", testOnlyTheUserReopens},
	{"Status/StartingTheWorkWaitsOnBlockers", testStartingWaitsOnBlockers},
}

// Every pair of statuses, through the implementation: each move the table
// has is made, and each it lacks is refused and changes nothing.
func testEveryTransition(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	for _, from := range tickets.Statuses() {
		for _, to := range tickets.Statuses() {
			work := f.ticket(fmt.Sprintf("%s to %s", from, to))
			f.reach(work.ID, from)
			got, err := f.setStatus(user, work.ID, to)
			if tickets.CanTransition(from, to) {
				require.NoError(t, err, "%s to %s", from, to)
				assert.Equal(t, to, got.Status)
				continue
			}
			require.ErrorIs(t, err, tickets.ErrInvalidTransition, "%s to %s", from, to)
			assert.Equal(t, from, f.get(work.ID).Status, "a refused move changes nothing")
		}
	}
}

// Reopening a finished ticket is a person's call. An agent's own ticket
// stops being its to edit once it is finished, so the refusal comes either
// way.
func testOnlyTheUserReopens(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	for _, end := range []tickets.Status{tickets.StatusDone, tickets.StatusCancelled} {
		a := f.agent("builder-" + string(end))
		f.reach(a.Ticket, end)
		_, err := f.setStatus(a, a.Ticket, tickets.StatusOpen)
		require.ErrorIs(t, err, tickets.ErrNotPermitted)
		assert.Equal(t, end, f.get(a.Ticket).Status)

		reopened, err := f.setStatus(user, a.Ticket, tickets.StatusOpen)
		require.NoError(t, err)
		assert.Equal(t, tickets.StatusOpen, reopened.Status)
	}
}

// Blockers gate starting the work, not the refinement, and not work under
// way.
func testStartingWaitsOnBlockers(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	work, blocker := f.ticket("Work"), f.ticket("Blocker")
	f.blockedBy(work.ID, blocker.ID)

	_, err := f.setStatus(user, work.ID, tickets.StatusInProgress)
	require.ErrorIs(t, err, tickets.ErrBlocked)
	assert.Equal(t, tickets.StatusOpen, f.get(work.ID).Status)
	_, err = f.setStatus(user, work.ID, tickets.StatusPreparing)
	require.NoError(t, err, "refining a blocked ticket is fine")
	f.reach(work.ID, tickets.StatusOpen)

	f.reach(blocker.ID, tickets.StatusDone)
	started, err := f.setStatus(user, work.ID, tickets.StatusInProgress)
	require.NoError(t, err, "a done blocker holds nothing up")
	assert.Equal(t, tickets.StatusInProgress, started.Status)

	// Blocked mid-work, it keeps going, and resumes after a discussion.
	later := f.ticket("Later")
	f.blockedBy(work.ID, later.ID)
	_, err = f.setStatus(user, work.ID, tickets.StatusDiscussion)
	require.NoError(t, err)
	resumed, err := f.setStatus(user, work.ID, tickets.StatusInProgress)
	require.NoError(t, err)
	assert.Equal(t, []string{"Later"}, titles(resumed.BlockedBy))
}

func titles(refs []tickets.TicketRef) []string {
	out := []string{}
	for _, r := range refs {
		out = append(out, r.Title)
	}
	return out
}
