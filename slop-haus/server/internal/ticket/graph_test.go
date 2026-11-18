package ticket_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// blockedBy wires "a is blocked by b", which is the direction everything
// here is named from: the blocker holds up the blocked. Added as the
// fixture's human, for tests whose subject is not who adds the edge.
func (f *fixture) blockedBy(t *testing.T, blocked, blocker int64) {
	t.Helper()
	_, err := f.svc.AddDependency(f.ctx, ticket.UserAuthor(f.human), blocked, blocker)
	require.NoError(t, err)
}

func (f *fixture) graphNode(t *testing.T, g *ticket.Graph, id int64) ticket.GraphNode {
	t.Helper()
	for _, n := range g.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("#%d is not in the graph", id)
	return ticket.GraphNode{}
}

func TestGraph_RecordsBothDirectionsOfAnEdge(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.ticket(t, "needs the other")
	b := f.ticket(t, "must go first")
	f.blockedBy(t, a.ID, b.ID)

	g, err := f.svc.Graph(f.ctx, &ticket.GraphQuery{})
	require.NoError(t, err)

	assert.Equal(t, []int64{b.ID}, f.graphNode(t, g, a.ID).BlockedBy)
	assert.Equal(t, []int64{a.ID}, f.graphNode(t, g, b.ID).Blocks,
		"the reverse edge is what makes 'what am I holding up' answerable")
	assert.Contains(t, g.Edges, ticket.GraphEdge{Blocker: b.ID, Blocked: a.ID})
}

// The headline requirement: a ticket may be held up by several others.
func TestGraph_ATicketCanHaveManyBlockers(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.ticket(t, "waits on three things")
	blockers := []int64{}
	for range 3 {
		b := f.ticket(t, "a blocker")
		blockers = append(blockers, b.ID)
		f.blockedBy(t, target.ID, b.ID)
	}

	g, err := f.svc.Graph(f.ctx, &ticket.GraphQuery{})
	require.NoError(t, err)

	node := f.graphNode(t, g, target.ID)
	assert.ElementsMatch(t, blockers, node.BlockedBy)
	assert.True(t, node.Blocked)
	assert.ElementsMatch(t, blockers, node.TransitiveBlockers)
}

// Blocking is transitive: a chain means the ticket at the end is waiting on
// everything upstream, not just its immediate blocker. Tracing that by eye
// is exactly what the graph exists to avoid.
func TestGraph_BlockersAreTransitive(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	c := f.ticket(t, "last")
	b := f.ticket(t, "middle")
	a := f.ticket(t, "first")
	f.blockedBy(t, c.ID, b.ID)
	f.blockedBy(t, b.ID, a.ID)

	g, err := f.svc.Graph(f.ctx, &ticket.GraphQuery{})
	require.NoError(t, err)

	assert.ElementsMatch(t, []int64{b.ID, a.ID}, f.graphNode(t, g, c.ID).TransitiveBlockers,
		"the far end of a chain waits on the whole chain")
	assert.Equal(t, []int64{b.ID}, f.graphNode(t, g, c.ID).BlockedBy,
		"but only one of them is a direct blocker")
}

// A finished blocker holds up nothing, and neither does anything behind
// it. This has to match the claim path's rule, or the board would show a
// ticket as blocked while the server happily hands it out.
func TestGraph_TerminalBlockersStopBlocking(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	work := f.ticket(t, "the work")
	blocker := f.ticket(t, "will be done")
	behind := f.ticket(t, "behind the blocker")
	f.blockedBy(t, work.ID, blocker.ID)
	f.blockedBy(t, blocker.ID, behind.ID)

	done := ticket.StatusDone
	_, err := f.svc.Update(f.ctx, blocker.ID, &ticket.UpdateParams{Status: &done})
	require.NoError(t, err)

	g, err := f.svc.Graph(f.ctx, &ticket.GraphQuery{})
	require.NoError(t, err)

	node := f.graphNode(t, g, work.ID)
	assert.False(t, node.Blocked, "its only blocker is done")
	assert.Empty(t, node.TransitiveBlockers,
		"and nothing behind a finished blocker still counts")
	assert.Equal(t, 0, node.Layer, "so it moves to the front of the graph")
}

// Cancelled counts as terminal too -- the same rule the claim query uses.
func TestGraph_CancelledBlockersStopBlocking(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	work := f.ticket(t, "the work")
	blocker := f.ticket(t, "will be cancelled")
	f.blockedBy(t, work.ID, blocker.ID)

	cancelled := ticket.StatusCancelled
	_, err := f.svc.Update(f.ctx, blocker.ID, &ticket.UpdateParams{Status: &cancelled})
	require.NoError(t, err)

	g, err := f.svc.Graph(f.ctx, &ticket.GraphQuery{})
	require.NoError(t, err)
	assert.False(t, f.graphNode(t, g, work.ID).Blocked)
}

// Unblocks is the number worth sorting by: finishing this frees that many
// tickets, directly or further downstream.
func TestGraph_UnblocksCountsEverythingDownstream(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	root := f.ticket(t, "holds up everything")
	mid := f.ticket(t, "middle")
	leafA := f.ticket(t, "leaf a")
	leafB := f.ticket(t, "leaf b")
	f.blockedBy(t, mid.ID, root.ID)
	f.blockedBy(t, leafA.ID, mid.ID)
	f.blockedBy(t, leafB.ID, root.ID)

	g, err := f.svc.Graph(f.ctx, &ticket.GraphQuery{})
	require.NoError(t, err)

	assert.Equal(t, 3, f.graphNode(t, g, root.ID).Unblocks,
		"mid, leaf a (via mid) and leaf b")
	assert.Equal(t, 1, f.graphNode(t, g, mid.ID).Unblocks)
	assert.Equal(t, 0, f.graphNode(t, g, leafA.ID).Unblocks)
}

// A finished ticket in the middle of a chain frees everything behind it, so
// the ticket upstream of it is no longer holding those up and should not be
// sorted as though it were. This is the same rule Blocked and
// TransitiveBlockers use, read from the other end.
func TestGraph_UnblocksIgnoresWhatAFinishedTicketAlreadyFreed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	root := f.ticket(t, "upstream of the finished one")
	mid := f.ticket(t, "will be done")
	leaf := f.ticket(t, "behind the finished one")
	f.blockedBy(t, mid.ID, root.ID)
	f.blockedBy(t, leaf.ID, mid.ID)

	done := ticket.StatusDone
	_, err := f.svc.Update(f.ctx, mid.ID, &ticket.UpdateParams{Status: &done})
	require.NoError(t, err)

	g, err := f.svc.Graph(f.ctx, &ticket.GraphQuery{})
	require.NoError(t, err)

	require.False(t, f.graphNode(t, g, leaf.ID).Blocked, "its only blocker is done")
	assert.Equal(t, 0, f.graphNode(t, g, root.ID).Unblocks,
		"mid is finished and leaf is already free, so root is holding up nothing")
}

// Finishing a ticket does nothing for work that is itself already finished.
func TestGraph_UnblocksIgnoresFinishedTicketsDownstream(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	blocker := f.ticket(t, "still open")
	finished := f.ticket(t, "closed early")
	f.blockedBy(t, finished.ID, blocker.ID)

	done := ticket.StatusDone
	_, err := f.svc.Update(f.ctx, finished.ID, &ticket.UpdateParams{Status: &done})
	require.NoError(t, err)

	g, err := f.svc.Graph(f.ctx, &ticket.GraphQuery{})
	require.NoError(t, err)

	assert.Equal(t, 0, f.graphNode(t, g, blocker.ID).Unblocks,
		"nothing unfinished is waiting on it")
}

// Layers are what let the browser draw the graph without doing a
// topological sort of its own.
func TestGraph_LayersOrderTheGraphForDrawing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	first := f.ticket(t, "first")
	second := f.ticket(t, "second")
	third := f.ticket(t, "third")
	f.blockedBy(t, second.ID, first.ID)
	f.blockedBy(t, third.ID, second.ID)

	g, err := f.svc.Graph(f.ctx, &ticket.GraphQuery{})
	require.NoError(t, err)

	assert.Equal(t, 0, f.graphNode(t, g, first.ID).Layer)
	assert.Equal(t, 1, f.graphNode(t, g, second.ID).Layer)
	assert.Equal(t, 2, f.graphNode(t, g, third.ID).Layer)

	require.GreaterOrEqual(t, len(g.Layers), 3)
	assert.Contains(t, g.Layers[0], first.ID)
	assert.Contains(t, g.Layers[1], second.ID)
	assert.Contains(t, g.Layers[2], third.ID)

	// Every node appears exactly once across the layers, or something has
	// been dropped or duplicated by the traversal.
	seen := map[int64]int{}
	for _, layer := range g.Layers {
		for _, id := range layer {
			seen[id]++
		}
	}
	assert.Len(t, seen, len(g.Nodes))
	for id, count := range seen {
		assert.Equal(t, 1, count, "#%d appears %d times in the layers", id, count)
	}
}

// Blockers narrows the graph to one ticket and what it is waiting on --
// the view for "why can't I start this".
func TestBlockers_ReturnsOnlyWhatHoldsTheTicketUp(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	work := f.ticket(t, "the work")
	blocker := f.ticket(t, "blocker")
	upstream := f.ticket(t, "upstream of the blocker")
	unrelated := f.ticket(t, "nothing to do with it")
	downstream := f.ticket(t, "waiting on the work")
	f.blockedBy(t, work.ID, blocker.ID)
	f.blockedBy(t, blocker.ID, upstream.ID)
	// Downstream is deliberately connected to the ticket in question, in
	// the other direction: "why can't I start this" is upstream only, so
	// its edge must not survive the narrowing either.
	f.blockedBy(t, downstream.ID, work.ID)

	g, err := f.svc.Blockers(f.ctx, work.ID)
	require.NoError(t, err)

	ids := []int64{}
	for _, n := range g.Nodes {
		ids = append(ids, n.ID)
	}
	assert.ElementsMatch(t, []int64{work.ID, blocker.ID, upstream.ID}, ids)
	assert.NotContains(t, ids, unrelated.ID)
	assert.NotContains(t, ids, downstream.ID)

	// Narrowing has to keep the graph drawable, not just list the nodes:
	// the edges between what is kept, and the layer order, are what the
	// view renders.
	assert.ElementsMatch(t, []ticket.GraphEdge{
		{Blocker: blocker.ID, Blocked: work.ID},
		{Blocker: upstream.ID, Blocked: blocker.ID},
	}, g.Edges)
	assert.Equal(t, [][]int64{{upstream.ID}, {blocker.ID}, {work.ID}}, g.Layers,
		"deepest blocker first, the ticket itself last")
	assert.ElementsMatch(t, []int64{blocker.ID, upstream.ID},
		f.graphNode(t, g, work.ID).TransitiveBlockers)
}

func TestBlockers_MissingTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.svc.Blockers(f.ctx, 9999)
	require.ErrorIs(t, err, ticket.ErrNotFound)
}

func TestGraph_EmptyBoard(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	g, err := f.svc.Graph(f.ctx, &ticket.GraphQuery{})
	require.NoError(t, err)
	assert.Empty(t, g.Nodes)
	assert.Empty(t, g.Edges)
	assert.Empty(t, g.Layers)
}

// The test the advisory lock in AddDependency exists for: two opposite
// edges written at the same instant.
//
// Without the lock each writer checks an acyclic graph, neither sees the
// other's uncommitted row, and both inserts land -- leaving #a and #b
// blocked by each other and neither ever claimable. No constraint can
// express that, because it is a property of the whole graph rather than of
// either row.
//
// It has to force a genuine overlap for the same reason the claim test
// does: an edge write finishes in about a millisecond, so two goroutines
// started together still run one after the other and the second sees the
// first's committed edge. Hence depsBarrier. With the lock in place the
// second writer never reaches the barrier while the first is waiting, so
// the barrier must time out rather than block forever.
func TestAddDependency_ConcurrentOppositeEdgesCannotFormACycle(t *testing.T) {
	f := newFixture(t)
	a := f.ticket(t, "one end")
	b := f.ticket(t, "the other end")

	var (
		mu      sync.Mutex
		waiting int
	)
	restore := ticket.SetDepsBarrier(func() {
		mu.Lock()
		waiting++
		mu.Unlock()

		deadline := time.After(2 * time.Second)
		for {
			mu.Lock()
			both := waiting >= 2
			mu.Unlock()
			if both {
				return
			}
			select {
			case <-deadline:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	})
	defer restore()

	var (
		wg     sync.WaitGroup
		errsMu sync.Mutex
		errs   []error
	)
	for _, edge := range [][2]int64{{a.ID, b.ID}, {b.ID, a.ID}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.svc.AddDependency(f.ctx, ticket.UserAuthor(f.human), edge[0], edge[1])
			errsMu.Lock()
			errs = append(errs, err)
			errsMu.Unlock()
		}()
	}
	wg.Wait()

	rejected := 0
	for _, err := range errs {
		if err != nil {
			require.ErrorIs(t, err, ticket.ErrDependencyCycle)
			rejected++
		}
	}
	assert.Equal(t, 1, rejected, "exactly one of two opposite edges may be accepted")

	// And the stored graph agrees: one edge, one blocked ticket.
	g, err := f.svc.Graph(f.ctx, &ticket.GraphQuery{})
	require.NoError(t, err)
	assert.Len(t, g.Edges, 1)
	blocked := 0
	for _, n := range g.Nodes {
		if n.Blocked {
			blocked++
		}
	}
	assert.Equal(t, 1, blocked, "a cycle would leave both ends blocked forever")
}

func candidateIDs(candidates []ticket.BlockerCandidate) []int64 {
	ids := []int64{}
	for _, c := range candidates {
		ids = append(ids, c.ID)
	}
	return ids
}

func TestBlockerCandidates_OffersWhatCanActuallyBeAdded(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	work := f.ticket(t, "the work")
	free := f.ticket(t, "unrelated and available")
	existing := f.ticket(t, "already blocking it")
	downstream := f.ticket(t, "waiting on the work")
	f.blockedBy(t, work.ID, existing.ID)
	f.blockedBy(t, downstream.ID, work.ID)

	candidates, err := f.svc.BlockerCandidates(f.ctx, work.ID)
	require.NoError(t, err)

	ids := candidateIDs(candidates)
	assert.Contains(t, ids, free.ID)
	assert.NotContains(t, ids, work.ID, "a ticket cannot block itself")
	assert.NotContains(t, ids, existing.ID, "it already blocks this ticket")
	assert.NotContains(t, ids, downstream.ID, "it waits on this ticket, so the edge would close a cycle")
}

// The candidate list and AddDependency have to agree, or the list is worse
// than useless: it teaches a human that a ticket is available and then the
// server refuses it. Every candidate is added here for real.
func TestBlockerCandidates_EveryCandidateIsAccepted(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	work := f.ticket(t, "the work")
	for i := range 3 {
		other := f.ticket(t, "another ticket")
		if i == 0 {
			// Give the graph some shape, so the candidates are not simply
			// "every other ticket".
			f.blockedBy(t, other.ID, work.ID)
		}
	}
	deep := f.ticket(t, "further downstream")
	f.blockedBy(t, deep.ID, work.ID)

	candidates, err := f.svc.BlockerCandidates(f.ctx, work.ID)
	require.NoError(t, err)
	require.NotEmpty(t, candidates)

	for _, c := range candidates {
		_, err := f.svc.AddDependency(f.ctx, ticket.UserAuthor(f.human), work.ID, c.ID)
		assert.NoError(t, err, "#%d was offered as a blocker of #%d", c.ID, work.ID)
	}
}

// The cycle check in SQL walks every edge regardless of status, so a
// candidate whose only route back to this ticket runs through a *finished*
// one is still refused. The graph's own TransitiveBlockers stops at a
// terminal blocker, so deriving candidates from it would offer exactly
// this ticket and have the add fail.
func TestBlockerCandidates_ExcludesARouteBackThroughAFinishedTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	work := f.ticket(t, "the work")
	finished := f.ticket(t, "done, in the middle")
	far := f.ticket(t, "downstream of the finished one")
	f.blockedBy(t, finished.ID, work.ID)
	f.blockedBy(t, far.ID, finished.ID)

	done := ticket.StatusDone
	_, err := f.svc.Update(f.ctx, finished.ID, &ticket.UpdateParams{Status: &done})
	require.NoError(t, err)

	candidates, err := f.svc.BlockerCandidates(f.ctx, work.ID)
	require.NoError(t, err)
	assert.NotContains(t, candidateIDs(candidates), far.ID)

	// And the reason it is excluded: the API really does refuse it.
	_, err = f.svc.AddDependency(f.ctx, ticket.UserAuthor(f.human), work.ID, far.ID)
	require.ErrorIs(t, err, ticket.ErrDependencyCycle)
}

// A finished ticket blocks nothing -- the edge would be accepted and then
// hold nothing up, which in a picker is an action that appears to do
// nothing.
func TestBlockerCandidates_ExcludesFinishedTickets(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	work := f.ticket(t, "the work")
	finished := f.ticket(t, "already done")

	done := ticket.StatusDone
	_, err := f.svc.Update(f.ctx, finished.ID, &ticket.UpdateParams{Status: &done})
	require.NoError(t, err)

	candidates, err := f.svc.BlockerCandidates(f.ctx, work.ID)
	require.NoError(t, err)
	assert.NotContains(t, candidateIDs(candidates), finished.ID)
}

func TestBlockerCandidates_MissingTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.ticket(t, "something exists, just not the one asked for")

	_, err := f.svc.BlockerCandidates(f.ctx, 9999)
	require.ErrorIs(t, err, ticket.ErrNotFound)
}
