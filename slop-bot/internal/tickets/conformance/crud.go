package conformance

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

var crudTests = []test{
	{"CRUD/CreateAndGetRoundTrip", testCreateAndGet},
	{"CRUD/CreateDefaultsAndRefusals", testCreateRefusals},
	{"CRUD/UpdateRoundTrip", testUpdate},
	{"CRUD/UnknownTicketsAreNotFound", testNotFound},
	{"CRUD/Comments", testComments},
	{"CRUD/ListAndQuery", testList},
	{"CRUD/AgentCreatedTicketsRecordTheirParent", testAgentCreated},
	{"CRUD/AgentCreatedAssignmentIsTheCallersCall", testAgentCreatedAssignment},
	{"CRUD/CountOutstandingAgentCreated", testCountOutstanding},
	{"CRUD/Assign", testAssign},
}

func testCreateAndGet(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	created := f.create(user, &tickets.NewTicket{
		Title: "Add the feed", Body: "the body", Status: tickets.StatusOpen, Assignee: "aaron",
		Agent:     tickets.AgentSpec{Harness: "codex", Model: "gpt-x", Effort: "high"},
		AutoMerge: true, Urgent: true, UrgentReason: "main is red",
		Repos: []string{"slop-factory", "go-db"},
	})
	got := f.get(created.ID)
	assert.Equal(t, created, got, "Create answers with what Get reads")

	assert.NotEmpty(t, got.ID)
	assert.Equal(t, "Add the feed", got.Title)
	assert.Equal(t, "the body", got.Body)
	assert.Equal(t, tickets.StatusOpen, got.Status)
	assert.Equal(t, tickets.UserID("aaron"), got.AssigneeID)
	assert.Equal(t, tickets.AgentSpec{Harness: "codex", Model: "gpt-x", Effort: "high"}, got.Agent)
	assert.True(t, got.AutoMerge)
	assert.True(t, got.Urgent)
	assert.Equal(t, "main is red", got.UrgentReason)
	assert.Equal(t, []string{"slop-factory", "go-db"}, got.Repos)
	assert.Equal(t, user, got.CreatedBy)
	assert.False(t, got.AgentCreated())
	assert.Empty(t, got.Parent)
	assert.Empty(t, got.BlockedBy)
	assert.False(t, got.CreatedAt.IsZero())
	assert.False(t, got.UpdatedAt.Before(got.CreatedAt))
	require.Len(t, got.Comments, 1, "an urgent reason is also a comment")
	assert.Contains(t, got.Comments[0].Body, "main is red")
}

func testCreateRefusals(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	created := f.create(user, &tickets.NewTicket{Title: "Defaults"})
	assert.Equal(t, tickets.StatusPreparing, created.Status, "a ticket starts preparing")
	assert.Empty(t, created.AssigneeID)
	assert.Equal(t, tickets.AgentSpec{}, created.Agent, "a blank spec means the defaults")
	assert.False(t, created.AutoMerge)
	assert.Empty(t, created.Comments)

	for name, nt := range map[string]*tickets.NewTicket{
		"a blank title":                {Title: "  "},
		"starting in progress":         {Title: "x", Status: tickets.StatusInProgress},
		"starting done":                {Title: "x", Status: tickets.StatusDone},
		"an unknown status":            {Title: "x", Status: "limbo"},
		"a blank repo":                 {Title: "x", Repos: []string{"a", ""}},
		"an urgent reason, not urgent": {Title: "x", UrgentReason: "why"},
	} {
		_, err := f.sys.Create(f.ctx, user, nt)
		assert.ErrorIs(t, err, tickets.ErrInvalid, name)
	}
	_, err := f.sys.Create(f.ctx, ident.Principal{}, &tickets.NewTicket{Title: "x"})
	require.ErrorIs(t, err, tickets.ErrInvalidPrincipal)

	all, err := f.sys.List(f.ctx, nil)
	require.NoError(t, err)
	assert.Len(t, all, 1, "nothing refused was filed")
}

func testUpdate(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	created := f.ticket("Before")
	spec := tickets.AgentSpec{Harness: "pi", Effort: "low"}
	updated, err := f.sys.Update(f.ctx, user, created.ID, &tickets.TicketChange{
		Title: ptr("After"), Body: ptr("new body"), Agent: &spec, AutoMerge: ptr(true),
		Urgent: &tickets.UrgentChange{Urgent: true}, Repos: &[]string{"r1"},
		Status: ptr(tickets.StatusInProgress),
	})
	require.NoError(t, err)
	assert.Equal(t, updated, f.get(created.ID))
	assert.Equal(t, "After", updated.Title)
	assert.Equal(t, "new body", updated.Body)
	assert.Equal(t, spec, updated.Agent)
	assert.True(t, updated.AutoMerge)
	assert.True(t, updated.Urgent)
	assert.Equal(t, []string{"r1"}, updated.Repos)
	assert.Equal(t, tickets.StatusInProgress, updated.Status)
	assert.Equal(t, created.CreatedAt, updated.CreatedAt)

	// Nil fields are left alone.
	again, err := f.sys.Update(f.ctx, user, created.ID, &tickets.TicketChange{Body: ptr("only the body")})
	require.NoError(t, err)
	assert.Equal(t, "After", again.Title)
	assert.Equal(t, "only the body", again.Body)
	assert.True(t, again.AutoMerge)

	cleared, err := f.sys.Update(f.ctx, user, created.ID, &tickets.TicketChange{
		Urgent: &tickets.UrgentChange{Urgent: false}, Repos: &[]string{},
	})
	require.NoError(t, err)
	assert.False(t, cleared.Urgent)
	assert.Empty(t, cleared.Repos)

	_, err = f.sys.Update(f.ctx, user, created.ID, &tickets.TicketChange{Title: ptr("")})
	require.ErrorIs(t, err, tickets.ErrInvalid)
	_, err = f.sys.Update(f.ctx, user, created.ID, &tickets.TicketChange{Status: ptr(tickets.Status("limbo"))})
	require.ErrorIs(t, err, tickets.ErrInvalid)
}

func testNotFound(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	for _, id := range []ident.TicketID{missing, "not-an-id", ""} {
		_, err := f.sys.Get(f.ctx, id)
		assert.ErrorIs(t, err, tickets.ErrNotFound, "Get %q", id)
		_, err = f.sys.Update(f.ctx, user, id, &tickets.TicketChange{Title: ptr("x")})
		assert.ErrorIs(t, err, tickets.ErrNotFound, "Update %q", id)
		_, err = f.sys.Comment(f.ctx, user, id, "hello")
		assert.ErrorIs(t, err, tickets.ErrNotFound, "Comment %q", id)
		_, err = f.sys.Assign(f.ctx, user, id, "aaron")
		assert.ErrorIs(t, err, tickets.ErrNotFound, "Assign %q", id)
		_, err = f.sys.Blockers(f.ctx, id)
		assert.ErrorIs(t, err, tickets.ErrNotFound, "Blockers %q", id)
	}
}

func testComments(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	a := f.agent("builder-1")
	work := f.ticket("Work")

	first, err := f.sys.Comment(f.ctx, user, work.ID, "first")
	require.NoError(t, err)
	assert.Equal(t, work.ID, first.Ticket)
	assert.Equal(t, user, first.Author)
	assert.NotEmpty(t, first.ID)
	// An agent may comment on any ticket: it's how it asks for a change it
	// may not make.
	second, err := f.sys.Comment(f.ctx, a, work.ID, "second")
	require.NoError(t, err)
	assert.Equal(t, a, second.Author)
	assert.NotEqual(t, first.ID, second.ID)

	got := f.comments(work.ID)
	require.Len(t, got, 2)
	assert.Equal(t, *first, got[0])
	assert.Equal(t, *second, got[1])

	_, err = f.sys.Comment(f.ctx, user, work.ID, " ")
	require.ErrorIs(t, err, tickets.ErrInvalid)
	_, err = f.sys.Comment(f.ctx, ident.AgentPrincipal("ghost", missing), work.ID, "boo")
	require.ErrorIs(t, err, tickets.ErrInvalidPrincipal, "an agent is an agent of a real ticket")
	assert.Len(t, f.comments(work.ID), 2)
}

func testList(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	a := f.agent("builder-1") // files an in-progress ticket of its own
	open := f.create(user, &tickets.NewTicket{Title: "open", Status: tickets.StatusOpen, Assignee: "aaron"})
	urgent := f.create(user, &tickets.NewTicket{Title: "urgent", Urgent: true})
	filed := f.filedBy(a, "filed")
	f.blockedBy(open.ID, urgent.ID)
	home := a.Ticket

	all, err := f.sys.List(f.ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, []ident.TicketID{home, open.ID, urgent.ID, filed.ID}, refIDs(all), "in creation order")
	for _, tk := range all {
		assert.Nil(t, tk.Comments, "List leaves comments to Get")
	}
	assert.Equal(t, []ident.TicketID{urgent.ID}, ids(all[1].BlockedBy), "List carries the open blockers")

	for name, tc := range map[string]struct {
		q    *tickets.Query
		want []ident.TicketID
	}{
		"by status":  {&tickets.Query{Statuses: []tickets.Status{tickets.StatusOpen, tickets.StatusInProgress}}, []ident.TicketID{home, open.ID}},
		"assigned":   {&tickets.Query{Assignee: ptr(tickets.UserID("aaron"))}, []ident.TicketID{open.ID}},
		"unassigned": {&tickets.Query{Assignee: ptr(tickets.UserID(""))}, []ident.TicketID{home, urgent.ID, filed.ID}},
		"urgent":     {&tickets.Query{Urgent: ptr(true)}, []ident.TicketID{urgent.ID}},
		"agents'":    {&tickets.Query{AgentCreated: ptr(true)}, []ident.TicketID{filed.ID}},
		"people's":   {&tickets.Query{AgentCreated: ptr(false)}, []ident.TicketID{home, open.ID, urgent.ID}},
		"combined": {&tickets.Query{Statuses: []tickets.Status{tickets.StatusPreparing}, AgentCreated: ptr(false)},
			[]ident.TicketID{urgent.ID}},
		"nothing": {&tickets.Query{Statuses: []tickets.Status{tickets.StatusDone}}, []ident.TicketID{}},
	} {
		got, err := f.sys.List(f.ctx, tc.q)
		require.NoError(t, err)
		assert.Equal(t, tc.want, refIDs(got), name)
	}
}

func refIDs(ts []*tickets.Ticket) []ident.TicketID {
	out := []ident.TicketID{}
	for _, t := range ts {
		out = append(out, t.ID)
	}
	return out
}

// An agent's ticket records who filed it and from which ticket, and
// inherits that ticket's AutoMerge.
func testAgentCreated(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	for _, autoMerge := range []bool{true, false} {
		home := f.create(user, &tickets.NewTicket{Title: "home", Status: tickets.StatusOpen, AutoMerge: autoMerge})
		f.reach(home.ID, tickets.StatusInProgress)
		a := ident.AgentPrincipal("builder", home.ID)

		filed := f.create(a, &tickets.NewTicket{Title: "follow-up"})
		assert.Equal(t, a, filed.CreatedBy)
		assert.True(t, filed.AgentCreated())
		assert.Equal(t, home.ID, filed.Parent)
		assert.Equal(t, autoMerge, filed.AutoMerge, "inherits the creating ticket's AutoMerge")
		assert.Equal(t, tickets.StatusPreparing, filed.Status)
	}
}

// Autonomy on assigns an agent's ticket to the bot's user; off leaves it
// unassigned for a person to accept. The bot decides which; the system
// records what it is told.
func testAgentCreatedAssignment(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	a := f.agent("builder-1")
	on := f.create(a, &tickets.NewTicket{Title: "autonomy on", Assignee: "aaron"})
	off := f.create(a, &tickets.NewTicket{Title: "autonomy off"})
	assert.Equal(t, tickets.UserID("aaron"), on.AssigneeID)
	assert.Empty(t, off.AssigneeID)

	unassigned, err := f.sys.List(f.ctx, &tickets.Query{Assignee: ptr(tickets.UserID("")), AgentCreated: ptr(true)})
	require.NoError(t, err)
	assert.Equal(t, []ident.TicketID{off.ID}, refIDs(unassigned), "the review stream's query")
}

func testCountOutstanding(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	count := func() int {
		t.Helper()
		n, err := f.sys.CountOutstandingAgentCreated(f.ctx)
		require.NoError(t, err)
		return n
	}
	a := f.agent("builder-1")
	f.ticket("a person's ticket never counts")
	assert.Equal(t, 0, count())

	byStatus := map[tickets.Status]*tickets.Ticket{}
	for _, s := range tickets.Statuses() {
		byStatus[s] = f.filedBy(a, string(s))
		f.reach(byStatus[s].ID, s)
	}
	assert.Equal(t, len(tickets.Statuses())-2, count(), "every agent-filed ticket but the done and the cancelled")

	f.reach(byStatus[tickets.StatusOpen].ID, tickets.StatusCancelled)
	f.reach(byStatus[tickets.StatusPreparing].ID, tickets.StatusDone)
	assert.Equal(t, len(tickets.Statuses())-4, count())
	f.reach(byStatus[tickets.StatusDone].ID, tickets.StatusOpen)
	assert.Equal(t, len(tickets.Statuses())-3, count(), "a reopened ticket is outstanding again")
}

func testAssign(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	a := f.agent("builder-1")
	work := f.ticket("Work")

	assigned, err := f.sys.Assign(f.ctx, user, work.ID, "aaron")
	require.NoError(t, err)
	assert.Equal(t, tickets.UserID("aaron"), assigned.AssigneeID)
	assert.Equal(t, assigned, f.get(work.ID))

	_, err = f.sys.Assign(f.ctx, a, work.ID, "someone-else")
	require.ErrorIs(t, err, tickets.ErrNotPermitted, "assignment is the user's")
	_, err = f.sys.Assign(f.ctx, a, a.Ticket, "")
	require.ErrorIs(t, err, tickets.ErrNotPermitted, "even of its own ticket")

	unassigned, err := f.sys.Assign(f.ctx, user, work.ID, "")
	require.NoError(t, err)
	assert.Empty(t, unassigned.AssigneeID)
}
