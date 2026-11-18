package ticket_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// The blocking graph over the wire, against the real store: whether an edge
// is accepted, and what adding one does to the blocked ticket's status, are
// PostgresStore's to decide (the cycle check is atomic with the insert), so
// a fake of them would be a second copy that could drift.

// graphBody reuses the domain's own GraphNode/GraphEdge types rather than
// re-declaring the wire shape, the same way TicketBody is reused elsewhere
// -- it's still a wire assertion, since json.Unmarshal only succeeds if the
// handler actually serialized "nodes"/"edges"/"layers"/"blocker"/"blocked"
// etc. under those names.
type graphBody struct {
	Nodes     []ticket.GraphNode `json:"nodes"`
	Edges     []ticket.GraphEdge `json:"edges"`
	Layers    [][]int64          `json:"layers"`
	Truncated bool               `json:"truncated"`
}

func (b graphBody) node(t *testing.T, id int64) ticket.GraphNode {
	t.Helper()
	for _, n := range b.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("#%d not in graph body", id)
	return ticket.GraphNode{}
}

// blockerList is the body the add and remove blocker routes answer with.
type blockerList struct {
	BlockedBy []int64                `json:"blocked_by"`
	Blockers  []ticket.TicketRefBody `json:"blockers"`
}

// The raw JSON rather than TicketBody: an absent or null "blockers" would
// unmarshal to a nil slice and compare equal to nothing, but the ticket
// view reads it as an array.
func TestGetTicket_NamesItsBlockers(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	blocker := f.ticket(t, "Build the API")
	work := f.ticket(t, "Build the UI")
	f.blockedBy(t, work.ID, blocker.ID)

	get := func(id int64) string {
		rec := send(t, mux, http.MethodGet, fmt.Sprintf("/api/tickets/%d", id), "")
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		var body struct {
			Blockers json.RawMessage `json:"blockers"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		return string(body.Blockers)
	}

	assert.JSONEq(t, fmt.Sprintf(`[{"id": %d, "title": "Build the API", "status": "open"}]`, blocker.ID), get(work.ID))
	assert.JSONEq(t, `[]`, get(blocker.ID))
}

func TestGraph_ReturnsNodesEdgesAndLayers(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	blocker := f.ticket(t, "blocker")
	work := f.ticket(t, "work")
	f.blockedBy(t, work.ID, blocker.ID)

	rec := send(t, f.boardMux(), http.MethodGet, "/api/tickets/graph", "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var body graphBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Nodes, 2)
	// Blocker is the one in the way, Blocked is the one waiting -- see
	// GraphEdge's doc comment in graph.go.
	assert.Contains(t, body.Edges, ticket.GraphEdge{Blocker: blocker.ID, Blocked: work.ID})
	assert.Equal(t, []int64{blocker.ID}, body.node(t, work.ID).BlockedBy)
	assert.Equal(t, []int64{work.ID}, body.node(t, blocker.ID).Blocks)
	assert.Equal(t, [][]int64{{blocker.ID}, {work.ID}}, body.Layers, "the blocker's layer comes first")
}

// The narrowing itself is covered in graph_test.go; this pins the wire half
// of it -- that the route is mounted, that {id} binds, and that an unknown
// ticket is the handler's 404 rather than a route that isn't there at all.
func TestTicketBlockers_ReturnsTheNarrowedGraph(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	blocker := f.ticket(t, "blocker")
	work := f.ticket(t, "work")
	unrelated := f.ticket(t, "unrelated")
	f.blockedBy(t, work.ID, blocker.ID)

	rec := send(t, mux, http.MethodGet, fmt.Sprintf("/api/tickets/%d/blockers", work.ID), "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var body graphBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	ids := []int64{}
	for _, n := range body.Nodes {
		ids = append(ids, n.ID)
	}
	assert.ElementsMatch(t, []int64{work.ID, blocker.ID}, ids)
	assert.NotContains(t, ids, unrelated.ID)

	rec = send(t, mux, http.MethodGet, fmt.Sprintf("/api/tickets/%d/blockers", unrelated.ID+1000), "")
	assert.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())
}

// Adding a blocker can move a ticket from open to blocked, which allow
// different moves, so the blocker list's status comes with its moves.
func TestAddBlocker_ReturnsTheBlockersAndTheMovesOfTheNewStatus(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	blocker := f.ticket(t, "blocker")
	work := f.ticket(t, "work")

	rec := send(t, f.boardMux(), http.MethodPost, fmt.Sprintf("/api/tickets/%d/blockers", work.ID),
		fmt.Sprintf(`{"blocker_id": %d}`, blocker.ID))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var list blockerList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	assert.Equal(t, []int64{blocker.ID}, list.BlockedBy)
	assert.Equal(t, []ticket.TicketRefBody{{ID: blocker.ID, Title: "blocker", Status: "open"}}, list.Blockers)

	var moves struct {
		Status       string   `json:"status"`
		AllowedMoves []string `json:"allowed_moves"`
		CanReady     *bool    `json:"can_ready"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &moves))
	require.Equal(t, "blocked", moves.Status)
	require.NotEqual(t, ticket.AllowedMoves(ticket.StatusOpen), ticket.AllowedMoves(ticket.StatusBlocked),
		"premise: moves read before the change would pass for moves read after it")
	assert.Equal(t, wireStatuses(ticket.AllowedMoves(ticket.StatusBlocked)), moves.AllowedMoves)
	require.NotNil(t, moves.CanReady)
	assert.Equal(t, ticket.CanReady(ticket.StatusBlocked), *moves.CanReady)
}

// The store refuses an edge that would close a cycle, and the refusal
// reaches the board as a request it can't make, not a server error.
func TestAddBlocker_ACycleIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.ticket(t, "a")
	b := f.ticket(t, "b")
	f.blockedBy(t, a.ID, b.ID)

	rec := send(t, f.boardMux(), http.MethodPost, fmt.Sprintf("/api/tickets/%d/blockers", b.ID),
		fmt.Sprintf(`{"blocker_id": %d}`, a.ID))

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "cycle")
	refused := f.get(t, b.ID)
	assert.Equal(t, ticket.StatusOpen, refused.Status, "a refused edge changes nothing")
	assert.Empty(t, refused.DependsOn, "a refused edge is not recorded")
}

// DELETE's {blockerId} path param is the thing most likely to get bound
// wrong (swapped with {id}, or silently ignored), so this ticket carries
// two blockers and only one is removed -- a broken bind either removes
// nothing (both blockers survive) or removes the wrong one, and either way
// the assertion on the exact survivor catches it.
func TestRemoveBlocker_RemovesOnlyTheGivenBlocker(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	blockerA := f.ticket(t, "blocker a")
	blockerB := f.ticket(t, "blocker b")
	work := f.ticket(t, "work")
	f.blockedBy(t, work.ID, blockerA.ID)
	f.blockedBy(t, work.ID, blockerB.ID)

	rec := send(t, f.boardMux(), http.MethodDelete,
		fmt.Sprintf("/api/tickets/%d/blockers/%d", work.ID, blockerA.ID), "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var list blockerList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	assert.Equal(t, []int64{blockerB.ID}, list.BlockedBy, "only blocker a should have been removed")
	assert.Equal(t, []ticket.TicketRefBody{{ID: blockerB.ID, Title: "blocker b", Status: "open"}}, list.Blockers)
}

// The picker's whole job is to offer only what the server will accept, so
// the wire shape it reads carries the ids and the labels.
//
// The graph is shaped so that each ticket in it has a *different* answer:
// upstream is a legitimate blocker of work but already blocks blocker, and
// blocker itself is downstream of upstream. A route that ignored {id}
// would otherwise answer with the right list by accident.
func TestBlockerCandidates_ExcludesTheTicketItselfAndItsBlockers(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	upstream := f.ticket(t, "upstream")
	blocker := f.ticket(t, "blocker")
	work := f.ticket(t, "work")
	free := f.ticket(t, "free")
	f.blockedBy(t, blocker.ID, upstream.ID)
	f.blockedBy(t, work.ID, blocker.ID)

	rec := send(t, mux, http.MethodGet, fmt.Sprintf("/api/tickets/%d/blocker_candidates", work.ID), "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var body struct {
		Tickets []ticket.BlockerCandidate `json:"tickets"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	ids := []int64{}
	titles := map[int64]string{}
	for _, c := range body.Tickets {
		ids = append(ids, c.ID)
		titles[c.ID] = c.Title
	}
	assert.ElementsMatch(t, []int64{upstream.ID, free.ID}, ids)
	assert.Equal(t, "free", titles[free.ID], "a picker needs a label, not just an id")

	// Tickets exist, just not the one asked for -- otherwise an empty board
	// would 404 for any id at all and this would prove nothing.
	rec = send(t, mux, http.MethodGet, fmt.Sprintf("/api/tickets/%d/blocker_candidates", free.ID+1000), "")
	assert.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())
}
