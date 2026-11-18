package ticket_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Who may change which ticket's blockers, and what each change leaves
// behind: the status it implies and a comment saying who made it.

// filedBy is a ticket the agent filed, in the given creation status.
func (f *fixture) filedBy(t *testing.T, agentID uuid.UUID, title string, status ticket.Status, blockedOn ...int64) *ticket.Ticket {
	t.Helper()
	created, err := f.svc.Create(f.ctx, &ticket.CreateParams{
		Title: title, CreatedByAgentID: &agentID, Status: status, BlockedOn: blockedOn,
	})
	require.NoError(t, err)
	return created
}

func (f *fixture) comments(t *testing.T, id int64) []ticket.Comment {
	t.Helper()
	got, err := f.svc.ListComments(f.ctx, id)
	require.NoError(t, err)
	return got
}

func TestAgentBlockers_OnTheHeldTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	held := f.ticket(t, "Held")
	blocker := f.ticket(t, "Blocker")
	_, err := f.svc.Claim(f.ctx, a, &held.ID)
	require.NoError(t, err)

	change, err := f.svc.AddDependency(f.ctx, ticket.AgentAuthor(a), held.ID, blocker.ID)
	require.NoError(t, err)
	assert.True(t, change.Changed)
	assert.Equal(t, []int64{blocker.ID}, change.Ticket.DependsOn)
	assert.Equal(t, ticket.StatusOpen, change.Ticket.Blockers[0].Status, "the blocker's status comes with it")
	assert.Equal(t, ticket.StatusInProgress, change.Ticket.Status,
		"a blocker gates claims, not the lease: the held ticket stays in progress")

	comments := f.comments(t, held.ID)
	require.Len(t, comments, 1)
	assert.Equal(t, &a, comments[0].AuthorAgentID)
	assert.Contains(t, comments[0].Body, "Blocked on #")
	assert.Contains(t, comments[0].Body, "Blocker")

	change, err = f.svc.RemoveDependency(f.ctx, ticket.AgentAuthor(a), held.ID, blocker.ID)
	require.NoError(t, err)
	assert.True(t, change.Changed)
	assert.Empty(t, change.Ticket.DependsOn)
	assert.Equal(t, ticket.StatusInProgress, change.Ticket.Status, "nor does lifting one end the lease")
	comments = f.comments(t, held.ID)
	require.Len(t, comments, 2)
	assert.Contains(t, comments[1].Body, "No longer blocked on #")
}

// The follow-up case: an agent splits work and chains the halves. Adding
// the first unfinished blocker takes an open ticket out of the queue, and
// the agent can still correct its own edit on the now-blocked ticket.
func TestAgentBlockers_OnATicketItFiled(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	first := f.filedBy(t, a, "First half", ticket.StatusOpen)
	second := f.filedBy(t, a, "Second half", ticket.StatusOpen)

	change, err := f.svc.AddDependency(f.ctx, ticket.AgentAuthor(a), second.ID, first.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusBlocked, change.Ticket.Status)

	change, err = f.svc.RemoveDependency(f.ctx, ticket.AgentAuthor(a), second.ID, first.ID)
	require.NoError(t, err)
	assert.True(t, change.Changed)
	assert.Equal(t, ticket.StatusOpen, change.Ticket.Status, "the last blocker gone, it is back in the queue")

	drafted := f.filedBy(t, a, "Draft", ticket.StatusPreparing)
	change, err = f.svc.AddDependency(f.ctx, ticket.AgentAuthor(a), drafted.ID, first.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusPreparing, change.Ticket.Status, "Ready decides open or blocked, not the edge")
}

func TestAgentBlockers_RefusedOutsideItsOwnWork(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	blocker := f.ticket(t, "Blocker")

	humans := f.ticket(t, "A person's ticket")
	anothers := f.filedBy(t, b, "Another agent's", ticket.StatusOpen)
	startedByOther := f.filedBy(t, a, "Filed by a, taken by b", ticket.StatusOpen)
	_, err := f.svc.Claim(f.ctx, b, &startedByOther.ID)
	require.NoError(t, err)
	finished := f.filedBy(t, a, "Filed by a, finished", ticket.StatusOpen)
	_, err = f.setStatus(t, finished.ID, ticket.StatusDone)
	require.NoError(t, err)
	discussing := f.filedBy(t, a, "Filed by a, in discussion", ticket.StatusOpen)
	_, err = f.svc.Claim(f.ctx, a, &discussing.ID)
	require.NoError(t, err)
	f.discuss(t, a)
	inReview := f.filedBy(t, a, "Filed by a, in review", ticket.StatusOpen)
	_, err = f.setStatus(t, inReview.ID, ticket.StatusReview)
	require.NoError(t, err)

	for name, target := range map[string]*ticket.Ticket{
		"a person's ticket":         humans,
		"another agent's ticket":    anothers,
		"its own, held by another":  startedByOther,
		"its own, already finished": finished,
		"its own, in discussion":    discussing,
		"its own, in review":        inReview,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.svc.AddDependency(f.ctx, ticket.AgentAuthor(a), target.ID, blocker.ID)
			require.ErrorIs(t, err, ticket.ErrBlockersNotEditable)
			_, err = f.svc.RemoveDependency(f.ctx, ticket.AgentAuthor(a), target.ID, blocker.ID)
			require.ErrorIs(t, err, ticket.ErrBlockersNotEditable)
			assert.Empty(t, f.get(t, target.ID).DependsOn)
			for _, c := range f.comments(t, target.ID) {
				assert.NotContains(t, c.Body, "Blocked on")
			}
		})
	}
}

// A person's edge is often a deliberate hold; another agent's is that
// agent's. Neither may be lifted by this one, even on its own ticket.
func TestAgentBlockers_RemovesOnlyEdgesItAdded(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	mine := f.filedBy(t, a, "Mine", ticket.StatusOpen)
	byPerson, byOther := f.ticket(t, "Held back by a person"), f.ticket(t, "Held back by b")
	f.blockedBy(t, mine.ID, byPerson.ID)
	_, err := f.database.Exec(f.ctx, `
		INSERT INTO ticket_deps (ticket_id, depends_on_ticket_id, added_by_agent_id) VALUES ($1, $2, $3)`,
		mine.ID, byOther.ID, b)
	require.NoError(t, err)

	for _, dep := range []int64{byPerson.ID, byOther.ID} {
		_, err := f.svc.RemoveDependency(f.ctx, ticket.AgentAuthor(a), mine.ID, dep)
		require.ErrorIs(t, err, ticket.ErrBlockerNotRemovable)
	}
	assert.ElementsMatch(t, []int64{byPerson.ID, byOther.ID}, f.get(t, mine.ID).DependsOn)

	// Re-adding a person's edge must not make it the agent's to remove.
	change, err := f.svc.AddDependency(f.ctx, ticket.AgentAuthor(a), mine.ID, byPerson.ID)
	require.NoError(t, err)
	assert.False(t, change.Changed)
	_, err = f.svc.RemoveDependency(f.ctx, ticket.AgentAuthor(a), mine.ID, byPerson.ID)
	require.ErrorIs(t, err, ticket.ErrBlockerNotRemovable)

	// A person can lift any of them.
	change, err = f.svc.RemoveDependency(f.ctx, ticket.UserAuthor(f.human), mine.ID, byOther.ID)
	require.NoError(t, err)
	assert.True(t, change.Changed)
}

func TestAgentBlockers_ANoOpRecordsNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	mine := f.filedBy(t, a, "Mine", ticket.StatusOpen)
	blocker := f.ticket(t, "Blocker")

	change, err := f.svc.RemoveDependency(f.ctx, ticket.AgentAuthor(a), mine.ID, blocker.ID)
	require.NoError(t, err)
	assert.False(t, change.Changed)

	_, err = f.svc.AddDependency(f.ctx, ticket.AgentAuthor(a), mine.ID, blocker.ID)
	require.NoError(t, err)
	change, err = f.svc.AddDependency(f.ctx, ticket.AgentAuthor(a), mine.ID, blocker.ID)
	require.NoError(t, err)
	assert.False(t, change.Changed)
	assert.Len(t, f.comments(t, mine.ID), 1, "only the change that changed something is recorded")
}

func TestAgentBlockers_CycleAndMissingTicketAreRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	first := f.filedBy(t, a, "First", ticket.StatusOpen)
	second := f.filedBy(t, a, "Second", ticket.StatusOpen, first.ID)

	_, err := f.svc.AddDependency(f.ctx, ticket.AgentAuthor(a), first.ID, second.ID)
	require.ErrorIs(t, err, ticket.ErrDependencyCycle)
	_, err = f.svc.AddDependency(f.ctx, ticket.AgentAuthor(a), first.ID, 9999)
	require.ErrorIs(t, err, ticket.ErrNotFound)
	_, err = f.svc.AddDependency(f.ctx, ticket.AgentAuthor(a), 9999, first.ID)
	require.ErrorIs(t, err, ticket.ErrNotFound)
	assert.Empty(t, f.comments(t, first.ID))
}

// A person's board edit is recorded as theirs, and -- unlike a person's
// comment -- is not the answer to a discussion.
func TestPersonBlockers_RecordedWithoutEndingADiscussion(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	asking := f.ticket(t, "Asking")
	blocker := f.ticket(t, "Blocker")
	_, err := f.svc.Claim(f.ctx, a, &asking.ID)
	require.NoError(t, err)
	f.discuss(t, a)

	change, err := f.svc.AddDependency(f.ctx, ticket.UserAuthor(f.human), asking.ID, blocker.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDiscussion, change.Ticket.Status)
	comments := f.comments(t, asking.ID)
	last := comments[len(comments)-1]
	assert.Equal(t, &f.human, last.AuthorUserID)
	assert.Contains(t, last.Body, "Blocked on #")
}

// A blocker added to an open ticket takes it out of the
// queue visibly, and lifting it puts it back.
func TestPersonBlockers_OpenBecomesBlockedOnlyWhileTheBlockerIsUnfinished(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	work, pending, finished := f.ticket(t, "Work"), f.ticket(t, "Pending"), f.ticket(t, "Finished")
	_, err := f.setStatus(t, finished.ID, ticket.StatusDone)
	require.NoError(t, err)

	f.blockedBy(t, work.ID, finished.ID)
	assert.Equal(t, ticket.StatusOpen, f.status(t, work.ID), "a finished blocker holds nothing up")

	f.blockedBy(t, work.ID, pending.ID)
	assert.Equal(t, ticket.StatusBlocked, f.status(t, work.ID))

	_, err = f.svc.RemoveDependency(f.ctx, ticket.UserAuthor(f.human), work.ID, pending.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusOpen, f.status(t, work.ID))
}

func TestCreate_BlockedOn(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	blocker := f.ticket(t, "Blocker")

	t.Run("an open ticket starts blocked, and the edges are the filer's", func(t *testing.T) {
		created := f.filedBy(t, a, "Follow-up", ticket.StatusOpen, blocker.ID, blocker.ID)
		assert.Equal(t, ticket.StatusBlocked, created.Status)
		assert.Equal(t, []int64{blocker.ID}, created.DependsOn, "a repeated id is one edge")
		comments := f.comments(t, created.ID)
		require.Len(t, comments, 1)
		assert.Equal(t, &a, comments[0].AuthorAgentID)

		change, err := f.svc.RemoveDependency(f.ctx, ticket.AgentAuthor(a), created.ID, blocker.ID)
		require.NoError(t, err)
		assert.True(t, change.Changed, "an edge filed with the ticket is the filing agent's to remove")
	})

	t.Run("a preparing ticket stays preparing", func(t *testing.T) {
		created := f.filedBy(t, a, "Drafted follow-up", ticket.StatusPreparing, blocker.ID)
		assert.Equal(t, ticket.StatusPreparing, created.Status)
		assert.Equal(t, []int64{blocker.ID}, created.DependsOn)
	})

	t.Run("a missing blocker files nothing", func(t *testing.T) {
		before, err := f.svc.List(f.ctx, nil)
		require.NoError(t, err)
		_, err = f.svc.Create(f.ctx, &ticket.CreateParams{
			Title: "Doomed", CreatedByAgentID: &a, Status: ticket.StatusOpen, BlockedOn: []int64{blocker.ID, 9999},
		})
		require.ErrorIs(t, err, ticket.ErrNotFound)
		after, err := f.svc.List(f.ctx, nil)
		require.NoError(t, err)
		assert.Len(t, after, len(before))
	})

	t.Run("blockers need a filer to record them as", func(t *testing.T) {
		_, err := f.svc.Create(f.ctx, &ticket.CreateParams{Title: "Anonymous", BlockedOn: []int64{blocker.ID}})
		require.ErrorIs(t, err, ticket.ErrInvalidAuthor)
	})
}

// An agent releasing blocked on something owns that edge like any other it
// adds, and it is recorded the same way.
func TestRelease_BlockedOnIsTheAgentsEdge(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	mine := f.filedBy(t, a, "Mine", ticket.StatusOpen)
	blocker := f.ticket(t, "Blocker")
	_, err := f.svc.Claim(f.ctx, a, &mine.ID)
	require.NoError(t, err)
	released, err := f.svc.Release(f.ctx, a, "waiting", &blocker.ID)
	require.NoError(t, err)
	require.Equal(t, ticket.StatusBlocked, released.Status)
	assert.Len(t, f.comments(t, mine.ID), 1)

	change, err := f.svc.RemoveDependency(f.ctx, ticket.AgentAuthor(a), mine.ID, blocker.ID)
	require.NoError(t, err)
	assert.True(t, change.Changed)
	assert.Equal(t, ticket.StatusOpen, change.Ticket.Status)
}

// Tenancy comes from RLS and the composite foreign keys, the same as every
// other edge: another tenant's ticket is simply not there.
func TestAgentBlockers_AnotherTenantsTicketIsNotFound(t *testing.T) {
	t.Parallel()
	a, b := newTwoTenants(t)
	aAgent := a.agentID
	mine, err := a.svc.Create(a.ctx, &ticket.CreateParams{Title: "Mine", CreatedByAgentID: &aAgent, Status: ticket.StatusOpen})
	require.NoError(t, err)
	theirs := b.ticket(t, "Theirs")
	theirs2 := b.ticket(t, "Theirs too")

	// Both tenants number from #1, so pick an id that exists only in b.
	_, err = a.svc.AddDependency(a.ctx, ticket.AgentAuthor(a.agentID), mine.ID, theirs2.ID)
	require.ErrorIs(t, err, ticket.ErrNotFound)
	_, err = a.svc.AddDependency(a.ctx, ticket.AgentAuthor(a.agentID), theirs2.ID, mine.ID)
	require.ErrorIs(t, err, ticket.ErrNotFound)
	got, err := b.svc.Get(b.ctx, theirs.ID)
	require.NoError(t, err)
	assert.Empty(t, got.DependsOn)
}

// The board's routes over the wire: the answer carries the status the
// change moved the ticket to, and the change is recorded as the person's.
func TestBlockerRoutes_Wire(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	work, blocker := f.ticket(t, "Work"), f.ticket(t, "Blocker")

	rec := send(t, mux, http.MethodPost, fmt.Sprintf("/api/tickets/%d/blockers", work.ID),
		fmt.Sprintf(`{"blocker_id": %d}`, blocker.ID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var added struct {
		BlockedBy []int64 `json:"blocked_by"`
		Status    string  `json:"status"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &added))
	assert.Equal(t, []int64{blocker.ID}, added.BlockedBy)
	assert.Equal(t, "blocked", added.Status)
	comments := f.comments(t, work.ID)
	require.Len(t, comments, 1)
	assert.Equal(t, &f.human, comments[0].AuthorUserID)

	rec = send(t, mux, http.MethodDelete, fmt.Sprintf("/api/tickets/%d/blockers/%d", work.ID, blocker.ID), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &added))
	assert.Empty(t, added.BlockedBy)
	assert.Equal(t, "open", added.Status)
	assert.Len(t, f.comments(t, work.ID), 2)
}

// A blocker added while the ticket was held still holds it once released
// without naming one: the board must not show open what the queue skips.
func TestRelease_KeepsABlockerAddedWhileHeld(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	held, blocker := f.ticket(t, "Held"), f.ticket(t, "Blocker")
	_, err := f.svc.Claim(f.ctx, a, &held.ID)
	require.NoError(t, err)
	_, err = f.svc.AddDependency(f.ctx, ticket.AgentAuthor(a), held.ID, blocker.ID)
	require.NoError(t, err)

	released, err := f.svc.Release(f.ctx, a, "handing back", nil)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusBlocked, released.Status)
}

// However a held ticket comes back to the queue, a blocker added while it
// was held keeps it blocked.
func TestLeaseEnd_KeepsABlockerAddedWhileHeld(t *testing.T) {
	t.Parallel()
	for name, end := range map[string]func(f *fixture, a uuid.UUID){
		"lease expired": func(f *fixture, a uuid.UUID) {
			f.expire(t, a)
			_, err := f.svc.Claim(f.ctx, f.agent(t, "builder-2"), nil)
			require.NoError(t, err)
		},
		"agent disabled": func(f *fixture, a uuid.UUID) {
			require.NoError(t, f.svc.ReleaseLeasesFor(f.ctx, a, "disabled", time.Now().Add(time.Hour)))
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			a := f.agent(t, "builder-1")
			held, blocker := f.ticket(t, "Held"), f.ticket(t, "Blocker")
			_, err := f.svc.Claim(f.ctx, a, &held.ID)
			require.NoError(t, err)
			_, err = f.svc.AddDependency(f.ctx, ticket.AgentAuthor(a), held.ID, blocker.ID)
			require.NoError(t, err)

			end(f, a)
			assert.Equal(t, ticket.StatusBlocked, f.status(t, held.ID))
		})
	}
}

func TestPersonBlockers_RemovingOneOfTwoPendingKeepsItBlocked(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	work, first, second := f.ticket(t, "Work"), f.ticket(t, "First"), f.ticket(t, "Second")
	f.blockedBy(t, work.ID, first.ID)
	f.blockedBy(t, work.ID, second.ID)

	_, err := f.svc.RemoveDependency(f.ctx, ticket.UserAuthor(f.human), work.ID, first.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusBlocked, f.status(t, work.ID))
}
