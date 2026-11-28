package conformance

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

var permissionTests = []test{
	{"Permissions/AutoMergeIsTheUsers", testAutoMergeIsTheUsers},
	{"Permissions/UrgentWhenAgentsMaySetIt", testUrgentAllowed},
	{"Permissions/UrgentWhenAgentsMayNot", testUrgentNotAllowed},
	{"Permissions/AgentsEditOnlyTheirOwnWork", testAgentEditScope},
	{"Permissions/AgentsStartTheirTicketsPreparing", testAgentStartsPreparing},
	{"Permissions/AgentOfAMissingTicket", testAgentOfAMissingTicket},
	{"Permissions/AgentsMoveOnlyTheirOwnTicketsStatus", testAgentStatusAuthority},
	{"Permissions/AFilingWithAutoMergeIsTheFilersNoMore", testAutoMergeLocksTheFilingsScope},
}

// The user accepts a filing and grants it AutoMerge, having read it: its
// filer may not then rewrite it. The agent working it, and blockers, are
// unaffected.
func testAutoMergeLocksTheFilingsScope(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	a := f.agent("builder-1")
	filed := f.filedBy(a, "follow-up")
	_, err := f.sys.Update(f.ctx, user, filed.ID, &tickets.TicketChange{AutoMerge: ptr(true)})
	require.NoError(t, err)

	for name, change := range map[string]*tickets.TicketChange{
		"title": {Title: ptr("bigger")}, "body": {Body: ptr("more")},
		"repos": {Repos: &[]string{"elsewhere"}}, "agent": {Agent: &tickets.AgentSpec{Harness: "pi"}},
	} {
		_, err := f.sys.Update(f.ctx, a, filed.ID, change)
		require.ErrorIs(t, err, tickets.ErrAutoMergeScopeLocked, name)
	}
	got := f.get(filed.ID)
	assert.Equal(t, "follow-up", got.Title)
	assert.Empty(t, got.Repos)

	blocker := f.ticket("Blocker")
	_, err = f.sys.AddBlocker(f.ctx, a, filed.ID, blocker.ID)
	require.NoError(t, err, "it may still chain it")
	refiner := ident.AgentPrincipal("builder-2", filed.ID)
	_, err = f.sys.Update(f.ctx, refiner, filed.ID, &tickets.TicketChange{Body: ptr("refined")})
	require.NoError(t, err, "the agent working it refines it")
}

// A filing is refined by the agent working it, not pushed on by its filer.
func testAgentStatusAuthority(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	a := f.agent("builder-1")
	filed := f.filedBy(a, "follow-up")
	for _, to := range []tickets.Status{tickets.StatusOpen, tickets.StatusDiscussion, tickets.StatusMisconfigured} {
		_, err := f.setStatus(a, filed.ID, to)
		require.ErrorIs(t, err, tickets.ErrStatusNotPermitted, "the filer moving it to %s", to)
	}
	f.reach(filed.ID, tickets.StatusOpen)
	_, err := f.setStatus(a, filed.ID, tickets.StatusInProgress)
	require.ErrorIs(t, err, tickets.ErrStatusNotPermitted, "nor starting it")
	assert.Equal(t, tickets.StatusOpen, f.get(filed.ID).Status)

	second := f.filedBy(a, "second")
	refiner := ident.AgentPrincipal("builder-2", second.ID)
	moved, err := f.setStatus(refiner, second.ID, tickets.StatusOpen)
	require.NoError(t, err, "the agent working it moves it on")
	assert.Equal(t, tickets.StatusOpen, moved.Status)

	_, err = f.setStatus(a, filed.ID, tickets.StatusOpen)
	require.NoError(t, err, "restating the status it has is no move")
	_, err = f.sys.Update(f.ctx, a, second.ID, &tickets.TicketChange{Status: ptr(tickets.StatusOpen), Body: ptr("notes")})
	require.NoError(t, err, "nor, with an edit of a filing it may edit")
	cancelled, err := f.setStatus(a, filed.ID, tickets.StatusCancelled)
	require.NoError(t, err, "a filer may drop its own filing")
	assert.Equal(t, tickets.StatusCancelled, cancelled.Status)
}

func testAutoMergeIsTheUsers(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	a := f.agent("builder-1")

	for _, v := range []bool{true, false} {
		_, err := f.sys.Update(f.ctx, a, a.Ticket, &tickets.TicketChange{AutoMerge: ptr(v)})
		require.ErrorIs(t, err, tickets.ErrAutoMergeUserOnly, "not even on its own ticket, to %v", v)
	}
	_, err := f.sys.Create(f.ctx, a, &tickets.NewTicket{Title: "grant myself", AutoMerge: true})
	require.ErrorIs(t, err, tickets.ErrAutoMergeUserOnly)
	assert.False(t, f.get(a.Ticket).AutoMerge)

	set, err := f.sys.Update(f.ctx, user, a.Ticket, &tickets.TicketChange{AutoMerge: ptr(true)})
	require.NoError(t, err)
	assert.True(t, set.AutoMerge)
	// What an agent may change alongside is still refused whole: the change
	// is atomic.
	_, err = f.sys.Update(f.ctx, a, a.Ticket, &tickets.TicketChange{Title: ptr("renamed"), AutoMerge: ptr(false)})
	require.ErrorIs(t, err, tickets.ErrAutoMergeUserOnly)
	got := f.get(a.Ticket)
	assert.True(t, got.AutoMerge)
	assert.NotEqual(t, "renamed", got.Title)
}

func testUrgentAllowed(t *testing.T, nb NewBackend) {
	f := newFixtureWith(t, nb, &tickets.Config{AgentsMaySetUrgent: true})
	a := f.agent("builder-1")

	_, err := f.sys.Create(f.ctx, a, &tickets.NewTicket{Title: "no reason", Urgent: true})
	require.ErrorIs(t, err, tickets.ErrUrgentReasonRequired)
	_, err = f.sys.Create(f.ctx, a, &tickets.NewTicket{Title: "blank reason", Urgent: true, UrgentReason: "  "})
	require.ErrorIs(t, err, tickets.ErrUrgentReasonRequired)

	filed := f.create(a, &tickets.NewTicket{Title: "main is red", Urgent: true, UrgentReason: "main is red: e2e"})
	assert.True(t, filed.Urgent)
	assert.Equal(t, "main is red: e2e", filed.UrgentReason)
	requireUrgentComment(t, filed.Comments, a, "main is red: e2e")

	_, err = f.sys.Update(f.ctx, a, a.Ticket, &tickets.TicketChange{Urgent: &tickets.UrgentChange{Urgent: true}})
	require.ErrorIs(t, err, tickets.ErrUrgentReasonRequired)
	marked, err := f.sys.Update(f.ctx, a, a.Ticket, &tickets.TicketChange{
		Urgent: &tickets.UrgentChange{Urgent: true, Reason: "the build is broken"},
	})
	require.NoError(t, err)
	assert.True(t, marked.Urgent)
	assert.Equal(t, "the build is broken", marked.UrgentReason)
	requireUrgentComment(t, marked.Comments, a, "the build is broken")
	edited, err := f.sys.Update(f.ctx, a, a.Ticket, &tickets.TicketChange{Body: ptr("progress notes")})
	require.NoError(t, err)
	assert.Len(t, edited.Comments, len(marked.Comments), "an edit of an urgent ticket doesn't repeat the reason")

	cleared, err := f.sys.Update(f.ctx, a, a.Ticket, &tickets.TicketChange{Urgent: &tickets.UrgentChange{}})
	require.NoError(t, err)
	assert.False(t, cleared.Urgent)
	assert.Empty(t, cleared.UrgentReason)
}

func requireUrgentComment(t *testing.T, comments []tickets.Comment, by ident.Principal, reason string) {
	t.Helper()
	require.NotEmpty(t, comments)
	last := comments[len(comments)-1]
	assert.Equal(t, by, last.Author)
	assert.Contains(t, last.Body, reason)
}

func testUrgentNotAllowed(t *testing.T, nb NewBackend) {
	f := newFixtureWith(t, nb, &tickets.Config{AgentsMaySetUrgent: false})
	a := f.agent("builder-1")

	_, err := f.sys.Create(f.ctx, a, &tickets.NewTicket{Title: "urgent", Urgent: true, UrgentReason: "main is red"})
	require.ErrorIs(t, err, tickets.ErrUrgentNotAllowed)
	_, err = f.sys.Update(f.ctx, a, a.Ticket, &tickets.TicketChange{
		Urgent: &tickets.UrgentChange{Urgent: true, Reason: "main is red"},
	})
	require.ErrorIs(t, err, tickets.ErrUrgentNotAllowed)
	assert.False(t, f.get(a.Ticket).Urgent)
	f.create(a, &tickets.NewTicket{Title: "not urgent"}) // the flag only gates Urgent

	// The user may always, with or without a reason.
	marked, err := f.sys.Update(f.ctx, user, a.Ticket, &tickets.TicketChange{Urgent: &tickets.UrgentChange{Urgent: true}})
	require.NoError(t, err)
	assert.True(t, marked.Urgent)
	_, err = f.sys.Update(f.ctx, a, a.Ticket, &tickets.TicketChange{Urgent: &tickets.UrgentChange{}})
	require.ErrorIs(t, err, tickets.ErrUrgentNotAllowed, "nor may it clear one")
	f.create(user, &tickets.NewTicket{Title: "users may", Urgent: true})

	padded, err := f.sys.Update(f.ctx, user, a.Ticket, &tickets.TicketChange{
		Urgent: &tickets.UrgentChange{Urgent: true, Reason: "  main is red  "},
	})
	require.NoError(t, err)
	assert.Equal(t, "main is red", padded.UrgentReason, "stored trimmed")
	blank := f.create(user, &tickets.NewTicket{Title: "blank reason", Urgent: true, UrgentReason: "  "})
	assert.Empty(t, blank.UrgentReason)
	assert.Empty(t, blank.Comments, "no reason, no comment")
	cleared, err := f.sys.Update(f.ctx, user, a.Ticket, &tickets.TicketChange{
		Urgent: &tickets.UrgentChange{Urgent: false, Reason: "  "},
	})
	require.NoError(t, err)
	assert.Empty(t, cleared.UrgentReason, "clearing clears the reason")
}

// The same scope as blockers: its own live ticket, or one it filed that
// nobody has started.
func testAgentEditScope(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	a, b := f.agent("builder-1"), f.agent("builder-2")

	_, err := f.sys.Update(f.ctx, a, a.Ticket, &tickets.TicketChange{Body: ptr("notes")})
	require.NoError(t, err, "its own ticket")
	filed := f.filedBy(a, "follow-up")
	_, err = f.sys.Update(f.ctx, a, filed.ID, &tickets.TicketChange{Body: ptr("more")})
	require.NoError(t, err, "one it filed, preparing")

	started := f.filedBy(a, "started")
	f.reach(started.ID, tickets.StatusInProgress)
	for name, id := range map[string]ident.TicketID{
		"a person's ticket":      f.ticket("person's").ID,
		"another agent's ticket": b.Ticket,
		"another agent's filing": f.filedBy(b, "b's").ID,
		"its filing, started":    started.ID,
	} {
		_, err := f.sys.Update(f.ctx, a, id, &tickets.TicketChange{Body: ptr("meddling")})
		require.ErrorIs(t, err, tickets.ErrNotEditable, name)
		assert.NotEqual(t, "meddling", f.get(id).Body, name)
	}
}

func testAgentStartsPreparing(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	a := f.agent("builder-1")
	_, err := f.sys.Create(f.ctx, a, &tickets.NewTicket{Title: "straight to work", Status: tickets.StatusOpen})
	require.ErrorIs(t, err, tickets.ErrNotPermitted, "another agent refines it first")
	assert.Equal(t, tickets.StatusPreparing, f.create(a, &tickets.NewTicket{Title: "x", Status: tickets.StatusPreparing}).Status)
}

func testAgentOfAMissingTicket(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	ghost := ident.AgentPrincipal("ghost", missing)
	work := f.ticket("Work")
	blocker := f.ticket("Blocker")

	_, err := f.sys.Create(f.ctx, ghost, &tickets.NewTicket{Title: "x"})
	require.ErrorIs(t, err, tickets.ErrInvalidPrincipal)
	_, err = f.sys.Update(f.ctx, ghost, work.ID, &tickets.TicketChange{Body: ptr("x")})
	require.ErrorIs(t, err, tickets.ErrInvalidPrincipal)
	_, err = f.sys.AddBlocker(f.ctx, ghost, work.ID, blocker.ID)
	require.ErrorIs(t, err, tickets.ErrInvalidPrincipal)
	_, err = f.sys.AddBlocker(f.ctx, ident.Principal{Kind: ident.Agent, Agent: "no-ticket"}, work.ID, blocker.ID)
	require.ErrorIs(t, err, tickets.ErrInvalidPrincipal)
}
