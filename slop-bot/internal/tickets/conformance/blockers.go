package conformance

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

// Who may change which ticket's blockers, and what each change leaves
// behind. Ported from slop-haus's blocker tests
// (slop-haus/server/internal/ticket/agent_blockers_test.go and
// blockers_handler_test.go). slop-haus's "the ticket it holds" is here the
// agent's own ticket, and its "blocked" status is a ticket with open
// blockers.
var blockerTests = []test{
	{"Blockers/OnTheAgentsOwnTicket", testBlockersOnOwnTicket},
	{"Blockers/OnATicketItFiled", testBlockersOnATicketItFiled},
	{"Blockers/RefusedOutsideItsOwnWork", testBlockersRefusedOutsideOwnWork},
	{"Blockers/AgentRemovesOnlyEdgesItAdded", testBlockersRemovesOnlyOwnEdges},
	{"Blockers/ANoOpRecordsNothing", testBlockersNoOp},
	{"Blockers/CycleAndMissingTicketAreRefused", testBlockersCycleAndMissing},
	{"Blockers/LongerCyclesAreRefusedDiamondsAreNot", testBlockersLongerCycles},
	{"Blockers/ConcurrentOppositeEdgesCantBothLand", testBlockersConcurrentOpposite},
	{"Blockers/APersonsEditKeepsTheStatus", testBlockersPersonKeepsStatus},
	{"Blockers/OnlyAnUnfinishedBlockerBlocks", testBlockersOnlyUnfinishedBlocks},
	{"Blockers/ACancelledBlockerStillBlocks", testBlockersCancelledStillBlocks},
	{"Blockers/RemovingOneOfTwoKeepsItBlocked", testBlockersRemoveOneOfTwo},
	{"Blockers/CreateBlockedOn", testBlockersCreateBlockedOn},
	{"Blockers/BothDirections", testBlockersBothDirections},
}

func testBlockersOnOwnTicket(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	a := f.agent("builder-1")
	blocker := f.ticket("Blocker")

	change, err := f.sys.AddBlocker(f.ctx, a, a.Ticket, blocker.ID)
	require.NoError(t, err)
	assert.True(t, change.Changed)
	assert.Equal(t, []tickets.TicketRef{{ID: blocker.ID, Title: "Blocker", Status: tickets.StatusOpen}},
		change.Ticket.BlockedBy, "the blocker's status comes with it")
	assert.Equal(t, tickets.StatusInProgress, change.Ticket.Status,
		"a blocker gates starting the work, not work under way")

	comments := f.comments(a.Ticket)
	require.Len(t, comments, 1)
	assert.Equal(t, a, comments[0].Author)
	assert.Contains(t, comments[0].Body, "Blocked on #")
	assert.Contains(t, comments[0].Body, "Blocker")

	change, err = f.sys.RemoveBlocker(f.ctx, a, a.Ticket, blocker.ID)
	require.NoError(t, err)
	assert.True(t, change.Changed)
	assert.Empty(t, change.Ticket.BlockedBy)
	assert.Equal(t, tickets.StatusInProgress, change.Ticket.Status)
	comments = f.comments(a.Ticket)
	require.Len(t, comments, 2)
	assert.Contains(t, comments[1].Body, "No longer blocked on #")
}

// The follow-up case: an agent splits work and chains the halves, and can
// correct its own edit.
func testBlockersOnATicketItFiled(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	a := f.agent("builder-1")
	first := f.filedBy(a, "First half")
	second := f.filedBy(a, "Second half")

	change, err := f.sys.AddBlocker(f.ctx, a, second.ID, first.ID)
	require.NoError(t, err)
	assert.Equal(t, []ident.TicketID{first.ID}, ids(change.Ticket.BlockedBy))
	assert.Equal(t, tickets.StatusPreparing, change.Ticket.Status, "the edge doesn't move the status")

	change, err = f.sys.RemoveBlocker(f.ctx, a, second.ID, first.ID)
	require.NoError(t, err)
	assert.True(t, change.Changed)
	assert.Empty(t, change.Ticket.BlockedBy)

	// Refined and open, nobody has started it: still the agent's to chain.
	f.reach(second.ID, tickets.StatusOpen)
	change, err = f.sys.AddBlocker(f.ctx, a, second.ID, first.ID)
	require.NoError(t, err)
	assert.Equal(t, tickets.StatusOpen, change.Ticket.Status)
	assert.Len(t, change.Ticket.BlockedBy, 1)
}

func testBlockersRefusedOutsideOwnWork(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	a, b := f.agent("builder-1"), f.agent("builder-2")
	blocker := f.ticket("Blocker")

	inStatus := func(title string, s tickets.Status) *tickets.Ticket {
		filed := f.filedBy(a, title)
		f.reach(filed.ID, s)
		return filed
	}
	finishedHome := f.agent("builder-3")
	f.reach(finishedHome.Ticket, tickets.StatusDone)

	targets := map[string]*tickets.Ticket{
		"a person's ticket":              f.ticket("A person's ticket"),
		"another agent's ticket":         f.get(b.Ticket),
		"another agent's filing":         f.filedBy(b, "Another agent's"),
		"its filing, started":            inStatus("Filed by a, started", tickets.StatusInProgress),
		"its filing, already finished":   inStatus("Filed by a, finished", tickets.StatusDone),
		"its filing, cancelled":          inStatus("Filed by a, cancelled", tickets.StatusCancelled),
		"its filing, in discussion":      inStatus("Filed by a, in discussion", tickets.StatusDiscussion),
		"its filing, misconfigured":      inStatus("Filed by a, misconfigured", tickets.StatusMisconfigured),
		"its own ticket, once it's done": f.get(finishedHome.Ticket),
	}
	for name, target := range targets {
		by := a
		if target.ID == finishedHome.Ticket {
			by = finishedHome
		}
		_, err := f.sys.AddBlocker(f.ctx, by, target.ID, blocker.ID)
		require.ErrorIs(t, err, tickets.ErrBlockersNotEditable, name)
		_, err = f.sys.RemoveBlocker(f.ctx, by, target.ID, blocker.ID)
		require.ErrorIs(t, err, tickets.ErrBlockersNotEditable, name)
		assert.Empty(t, f.get(target.ID).BlockedBy, name)
		for _, c := range f.comments(target.ID) {
			assert.NotContains(t, c.Body, "Blocked on", name)
		}
	}
}

// A person's edge is often a deliberate hold; another agent's is that
// agent's. Neither may be lifted by this one, even on a ticket it filed.
func testBlockersRemovesOnlyOwnEdges(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	a := f.agent("builder-1")
	mine := f.filedBy(a, "Mine")
	// b is the agent refining mine: it may edit its own ticket's blockers.
	b := ident.AgentPrincipal("builder-2", mine.ID)
	byPerson, byOther := f.ticket("Held back by a person"), f.ticket("Held back by b")
	f.blockedBy(mine.ID, byPerson.ID)
	_, err := f.sys.AddBlocker(f.ctx, b, mine.ID, byOther.ID)
	require.NoError(t, err)

	for _, dep := range []ident.TicketID{byPerson.ID, byOther.ID} {
		_, err := f.sys.RemoveBlocker(f.ctx, a, mine.ID, dep)
		require.ErrorIs(t, err, tickets.ErrBlockerNotRemovable)
	}
	assert.ElementsMatch(t, []ident.TicketID{byPerson.ID, byOther.ID}, ids(f.get(mine.ID).BlockedBy))

	// Re-adding a person's edge must not make it the agent's to remove.
	change, err := f.sys.AddBlocker(f.ctx, a, mine.ID, byPerson.ID)
	require.NoError(t, err)
	assert.False(t, change.Changed)
	_, err = f.sys.RemoveBlocker(f.ctx, a, mine.ID, byPerson.ID)
	require.ErrorIs(t, err, tickets.ErrBlockerNotRemovable)

	// b may lift its own; a person can lift any.
	change, err = f.sys.RemoveBlocker(f.ctx, b, mine.ID, byOther.ID)
	require.NoError(t, err)
	assert.True(t, change.Changed)
	change, err = f.sys.RemoveBlocker(f.ctx, user, mine.ID, byPerson.ID)
	require.NoError(t, err)
	assert.True(t, change.Changed)
	assert.Empty(t, change.Ticket.BlockedBy)
}

func testBlockersNoOp(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	a := f.agent("builder-1")
	mine := f.filedBy(a, "Mine")
	blocker := f.ticket("Blocker")

	change, err := f.sys.RemoveBlocker(f.ctx, a, mine.ID, blocker.ID)
	require.NoError(t, err)
	assert.False(t, change.Changed)

	_, err = f.sys.AddBlocker(f.ctx, a, mine.ID, blocker.ID)
	require.NoError(t, err)
	change, err = f.sys.AddBlocker(f.ctx, a, mine.ID, blocker.ID)
	require.NoError(t, err)
	assert.False(t, change.Changed)
	assert.Len(t, f.comments(mine.ID), 1, "only the change that changed something is recorded")
}

func testBlockersCycleAndMissing(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	a := f.agent("builder-1")
	first := f.filedBy(a, "First")
	second := f.filedBy(a, "Second", first.ID)

	_, err := f.sys.AddBlocker(f.ctx, a, first.ID, second.ID)
	require.ErrorIs(t, err, tickets.ErrCycle)
	_, err = f.sys.AddBlocker(f.ctx, a, first.ID, first.ID)
	require.ErrorIs(t, err, tickets.ErrCycle, "a ticket can't wait on itself")
	_, err = f.sys.AddBlocker(f.ctx, a, first.ID, missing)
	require.ErrorIs(t, err, tickets.ErrNotFound)
	_, err = f.sys.AddBlocker(f.ctx, user, missing, first.ID)
	require.ErrorIs(t, err, tickets.ErrNotFound)
	_, err = f.sys.RemoveBlocker(f.ctx, user, missing, first.ID)
	require.ErrorIs(t, err, tickets.ErrNotFound)
	assert.Empty(t, f.comments(first.ID))
	assert.Empty(t, f.get(first.ID).BlockedBy, "a refused edge is not recorded")
}

func testBlockersLongerCycles(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	x, y, z, w := f.ticket("x"), f.ticket("y"), f.ticket("z"), f.ticket("w")
	f.blockedBy(x.ID, y.ID)
	f.blockedBy(y.ID, z.ID)

	_, err := f.sys.AddBlocker(f.ctx, user, z.ID, x.ID)
	require.ErrorIs(t, err, tickets.ErrCycle, "x already waits on z, through y")
	assert.Empty(t, f.get(z.ID).BlockedBy)

	// Two paths to one blocker is a diamond, not a cycle.
	f.blockedBy(x.ID, w.ID)
	f.blockedBy(w.ID, z.ID)
	assert.ElementsMatch(t, []ident.TicketID{y.ID, w.ID}, ids(f.get(x.ID).BlockedBy))
}

// The cycle check commits with the insert. Two opposite edges, each acyclic
// on its own, raced over many pairs: exactly one of each pair lands.
func testBlockersConcurrentOpposite(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	const pairs = 16
	type pair struct{ x, y *tickets.Ticket }
	ps := make([]pair, pairs)
	for i := range ps {
		ps[i] = pair{f.ticket("x"), f.ticket("y")}
	}
	errs := make([][2]error, pairs)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, p := range ps {
		wg.Go(func() {
			<-start
			_, errs[i][0] = f.sys.AddBlocker(f.ctx, user, p.x.ID, p.y.ID)
		})
		wg.Go(func() {
			<-start
			_, errs[i][1] = f.sys.AddBlocker(f.ctx, user, p.y.ID, p.x.ID)
		})
	}
	close(start)
	wg.Wait()
	for i, p := range ps {
		landed := 0
		for _, err := range errs[i] {
			if err == nil {
				landed++
			} else {
				require.ErrorIs(t, err, tickets.ErrCycle)
			}
		}
		assert.Equal(t, 1, landed, "pair %d", i)
		assert.Len(t, append(f.get(p.x.ID).BlockedBy, f.get(p.y.ID).BlockedBy...), 1, "pair %d", i)
	}
}

// A person's edit is recorded as theirs, and leaves the status alone.
func testBlockersPersonKeepsStatus(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	asking, blocker := f.ticket("Asking"), f.ticket("Blocker")
	f.reach(asking.ID, tickets.StatusDiscussion)

	change, err := f.sys.AddBlocker(f.ctx, user, asking.ID, blocker.ID)
	require.NoError(t, err)
	assert.Equal(t, tickets.StatusDiscussion, change.Ticket.Status)
	comments := f.comments(asking.ID)
	last := comments[len(comments)-1]
	assert.Equal(t, user, last.Author)
	assert.Contains(t, last.Body, "Blocked on #")
}

func testBlockersOnlyUnfinishedBlocks(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	work, pending, finished := f.ticket("Work"), f.ticket("Pending"), f.ticket("Finished")
	f.reach(finished.ID, tickets.StatusDone)

	f.blockedBy(work.ID, finished.ID)
	assert.Empty(t, f.get(work.ID).BlockedBy, "a finished blocker holds nothing up")

	f.blockedBy(work.ID, pending.ID)
	assert.Equal(t, []ident.TicketID{pending.ID}, ids(f.get(work.ID).BlockedBy))

	_, err := f.sys.RemoveBlocker(f.ctx, user, work.ID, pending.ID)
	require.NoError(t, err)
	assert.Empty(t, f.get(work.ID).BlockedBy)

	// Reopening a finished blocker blocks again.
	f.reach(finished.ID, tickets.StatusOpen)
	assert.Equal(t, []ident.TicketID{finished.ID}, ids(f.get(work.ID).BlockedBy))
}

// A cancelled blocker isn't done: the ticket stays blocked until a person
// decides what it should wait on instead.
func testBlockersCancelledStillBlocks(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	work, blocker := f.ticket("Work"), f.ticket("Blocker")
	f.blockedBy(work.ID, blocker.ID)
	f.reach(blocker.ID, tickets.StatusCancelled)

	got := f.get(work.ID)
	require.Equal(t, []tickets.TicketRef{{ID: blocker.ID, Title: "Blocker", Status: tickets.StatusCancelled}}, got.BlockedBy)
	_, err := f.setStatus(user, work.ID, tickets.StatusInProgress)
	require.ErrorIs(t, err, tickets.ErrBlocked)

	_, err = f.sys.RemoveBlocker(f.ctx, user, work.ID, blocker.ID)
	require.NoError(t, err)
	_, err = f.setStatus(user, work.ID, tickets.StatusInProgress)
	require.NoError(t, err, "once a person lifts it")
}

func testBlockersRemoveOneOfTwo(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	work, first, second := f.ticket("Work"), f.ticket("First"), f.ticket("Second")
	f.blockedBy(work.ID, first.ID)
	f.blockedBy(work.ID, second.ID)

	// The removed one must be the one named: a swapped pair of ids would
	// remove nothing, or the wrong one.
	change, err := f.sys.RemoveBlocker(f.ctx, user, work.ID, first.ID)
	require.NoError(t, err)
	assert.Equal(t, []tickets.TicketRef{{ID: second.ID, Title: "Second", Status: tickets.StatusOpen}}, change.Ticket.BlockedBy)
}

func testBlockersCreateBlockedOn(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	a := f.agent("builder-1")
	blocker := f.ticket("Blocker")

	created := f.filedBy(a, "Follow-up", blocker.ID, blocker.ID)
	assert.Equal(t, []ident.TicketID{blocker.ID}, ids(created.BlockedBy), "a repeated id is one edge")
	assert.Equal(t, tickets.StatusPreparing, created.Status)
	comments := f.comments(created.ID)
	require.Len(t, comments, 1)
	assert.Equal(t, a, comments[0].Author)
	change, err := f.sys.RemoveBlocker(f.ctx, a, created.ID, blocker.ID)
	require.NoError(t, err)
	assert.True(t, change.Changed, "an edge filed with the ticket is the filing agent's to remove")

	byUser := f.create(user, &tickets.NewTicket{Title: "open", Status: tickets.StatusOpen, BlockedOn: []ident.TicketID{blocker.ID}})
	assert.Equal(t, []ident.TicketID{blocker.ID}, ids(byUser.BlockedBy))

	before, err := f.sys.List(f.ctx, nil)
	require.NoError(t, err)
	_, err = f.sys.Create(f.ctx, a, &tickets.NewTicket{Title: "Doomed", BlockedOn: []ident.TicketID{blocker.ID, missing}})
	require.ErrorIs(t, err, tickets.ErrNotFound)
	after, err := f.sys.List(f.ctx, nil)
	require.NoError(t, err)
	assert.Len(t, after, len(before), "a missing blocker files nothing")
}

func testBlockersBothDirections(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	a := f.agent("builder-1")
	upstream, work, done := f.ticket("upstream"), f.ticket("work"), f.ticket("done")
	f.reach(done.ID, tickets.StatusDone)
	f.blockedBy(work.ID, upstream.ID)
	f.blockedBy(work.ID, done.ID)
	_, err := f.sys.AddBlocker(f.ctx, a, a.Ticket, work.ID)
	require.NoError(t, err)

	set, err := f.sys.Blockers(f.ctx, work.ID)
	require.NoError(t, err)
	assert.Equal(t, []tickets.BlockerEdge{
		{Ticket: tickets.TicketRef{ID: upstream.ID, Title: "upstream", Status: tickets.StatusOpen}, AddedBy: user},
		{Ticket: tickets.TicketRef{ID: done.ID, Title: "done", Status: tickets.StatusDone}, AddedBy: user},
	}, set.BlockedBy, "done blockers too")
	assert.Equal(t, []tickets.BlockerEdge{
		{Ticket: tickets.TicketRef{ID: a.Ticket, Title: "builder-1's ticket", Status: tickets.StatusInProgress}, AddedBy: a},
	}, set.Blocks)

	set, err = f.sys.Blockers(f.ctx, upstream.ID)
	require.NoError(t, err)
	assert.Empty(t, set.BlockedBy)
	assert.Equal(t, []ident.TicketID{work.ID}, edgeIDs(set.Blocks))
}

func edgeIDs(es []tickets.BlockerEdge) []ident.TicketID {
	out := []ident.TicketID{}
	for _, e := range es {
		out = append(out, e.Ticket.ID)
	}
	return out
}
