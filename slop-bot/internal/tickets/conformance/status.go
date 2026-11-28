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
	{"Status/IfStatusMakesAChangeConditional", testIfStatus},
}

// A conditional change applies only while the ticket still has the status
// its caller saw; otherwise it is refused and changes nothing, so a stale
// writer can't undo a move made since.
func testIfStatus(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	work := f.ticket("conditional")
	open, inProgress, discussion := tickets.StatusOpen, tickets.StatusInProgress, tickets.StatusDiscussion
	title := "renamed"

	moved, err := f.sys.Update(f.ctx, user, work.ID, &tickets.TicketChange{Status: &inProgress, IfStatus: &open})
	require.NoError(t, err)
	assert.Equal(t, tickets.StatusInProgress, moved.Status)

	_, err = f.sys.Update(f.ctx, user, work.ID, &tickets.TicketChange{Status: &discussion, Title: &title, IfStatus: &open})
	require.ErrorIs(t, err, tickets.ErrStatusChanged)
	got := f.get(work.ID)
	assert.Equal(t, tickets.StatusInProgress, got.Status, "a refused change changes nothing")
	assert.Equal(t, "conditional", got.Title)

	a := f.agent("cas")
	_, err = f.sys.Update(f.ctx, a, a.Ticket, &tickets.TicketChange{Status: &inProgress, IfStatus: &open})
	require.ErrorIs(t, err, tickets.ErrStatusChanged, "a retried move under a stale condition is still refused")
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

// Reopening a finished ticket is a person's call, even the agent's own.
func testOnlyTheUserReopens(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	for _, end := range []tickets.Status{tickets.StatusDone, tickets.StatusCancelled} {
		a := f.agent("builder-" + string(end))
		f.reach(a.Ticket, end)
		_, err := f.setStatus(a, a.Ticket, tickets.StatusOpen)
		require.ErrorIs(t, err, tickets.ErrUserOnly)
		_, err = f.setStatus(a, a.Ticket, end)
		require.NoError(t, err, "a retried %s is no move", end)
		assert.Equal(t, end, f.get(a.Ticket).Status)

		reopened, err := f.setStatus(user, a.Ticket, tickets.StatusOpen)
		require.NoError(t, err)
		assert.Equal(t, tickets.StatusOpen, reopened.Status)
	}
}

// Blockers gate the work, not the refinement: no way into in_progress is
// open while one is.
func testStartingWaitsOnBlockers(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	work, blocker := f.ticket("Work"), f.ticket("Blocker")
	f.blockedBy(work.ID, blocker.ID)

	_, err := f.setStatus(user, work.ID, tickets.StatusInProgress)
	require.ErrorIs(t, err, tickets.ErrBlocked)
	assert.Equal(t, tickets.StatusOpen, f.get(work.ID).Status)
	f.reach(work.ID, tickets.StatusDiscussion)
	_, err = f.setStatus(user, work.ID, tickets.StatusInProgress)
	require.ErrorIs(t, err, tickets.ErrBlocked, "nor by way of a discussion")
	_, err = f.setStatus(user, work.ID, tickets.StatusPreparing)
	require.NoError(t, err, "refining a blocked ticket is fine")
	f.reach(work.ID, tickets.StatusOpen)

	f.reach(blocker.ID, tickets.StatusDone)
	started, err := f.setStatus(user, work.ID, tickets.StatusInProgress)
	require.NoError(t, err, "a done blocker holds nothing up")
	assert.Equal(t, tickets.StatusInProgress, started.Status)

	// Blocked mid-work, it stays in progress; once it leaves, it comes
	// back only when the blocker is done.
	later := f.ticket("Later")
	change, err := f.sys.AddBlocker(f.ctx, user, work.ID, later.ID)
	require.NoError(t, err)
	assert.Equal(t, tickets.StatusInProgress, change.Ticket.Status)
	assert.Equal(t, []string{"Later"}, titles(change.Ticket.BlockedBy))
	_, err = f.sys.Update(f.ctx, user, work.ID, &tickets.TicketChange{
		Status: ptr(tickets.StatusInProgress), Body: ptr("still going"),
	})
	require.NoError(t, err, "restating in_progress, with an edit, is no move into it")
	_, err = f.setStatus(user, work.ID, tickets.StatusDiscussion)
	require.NoError(t, err)
	_, err = f.setStatus(user, work.ID, tickets.StatusInProgress)
	require.ErrorIs(t, err, tickets.ErrBlocked)
	f.reach(later.ID, tickets.StatusDone)
	_, err = f.setStatus(user, work.ID, tickets.StatusInProgress)
	require.NoError(t, err)
}

func titles(refs []tickets.TicketRef) []string {
	out := []string{}
	for _, r := range refs {
		out = append(out, r.Title)
	}
	return out
}
