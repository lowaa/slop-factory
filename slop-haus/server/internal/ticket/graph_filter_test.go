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

// Hiding parked, cancelled and done is the Blocked page's default.
func withoutResting() ticket.ListFilter {
	return ticket.ListFilter{Statuses: []ticket.Status{
		ticket.StatusPreparing, ticket.StatusOpen, ticket.StatusInProgress, ticket.StatusBlocked,
		ticket.StatusDiscussion, ticket.StatusReview,
	}}
}

func nodeIDs(g *ticket.Graph) []int64 {
	ids := []int64{}
	for _, n := range g.Nodes {
		ids = append(ids, n.ID)
	}
	return ids
}

// A parked ticket still holds up what waits on it. Dropping it because the
// filter hides parked tickets would draw the waiting ticket as free to
// start, which is the one thing the filter must not imply.
func TestGraphFilter_AHiddenUnfinishedBlockerStaysAsAMarker(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	parked := f.parked(t, "parked, but real")
	upstream := f.ticket(t, "holds the parked one up")
	work := f.ticket(t, "waits on the parked one")
	f.blockedBy(t, work.ID, parked.ID)
	f.blockedBy(t, parked.ID, upstream.ID)

	filter := withoutResting()
	g, err := f.svc.Graph(f.ctx, &ticket.GraphQuery{Filter: filter})
	require.NoError(t, err)

	assert.ElementsMatch(t, []int64{work.ID, parked.ID, upstream.ID}, nodeIDs(g))
	marker := f.graphNode(t, g, parked.ID)
	assert.True(t, marker.Hidden)
	assert.Equal(t, ticket.StatusParked, marker.Status)
	assert.False(t, f.graphNode(t, g, upstream.ID).Hidden, "it matches the filter")
	assert.True(t, f.graphNode(t, g, work.ID).Blocked)
	assert.Contains(t, g.Edges, ticket.GraphEdge{Blocker: parked.ID, Blocked: work.ID})
	assert.Equal(t, 2, f.graphNode(t, g, work.ID).Layer, "layers count the hidden chain")
}

// A finished blocker holds nothing up, so a hidden one is dropped with its
// edge and the ticket really is free.
func TestGraphFilter_AHiddenFinishedBlockerIsDropped(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	finished := f.ticket(t, "cancelled blocker")
	work := f.ticket(t, "was waiting on it")
	f.blockedBy(t, work.ID, finished.ID)
	_, err := f.setStatus(t, finished.ID, ticket.StatusCancelled)
	require.NoError(t, err)

	g, err := f.svc.Graph(f.ctx, &ticket.GraphQuery{Filter: withoutResting()})
	require.NoError(t, err)

	assert.Equal(t, []int64{work.ID}, nodeIDs(g))
	assert.Empty(t, g.Edges)
	assert.False(t, f.graphNode(t, g, work.ID).Blocked)
}

// Unfiltered, a shown finished blocker keeps its edge, as before the filter.
func TestGraphFilter_EmptyFilterKeepsFinishedBlockers(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	finished := f.ticket(t, "done blocker")
	work := f.ticket(t, "work")
	f.blockedBy(t, work.ID, finished.ID)
	_, err := f.setStatus(t, finished.ID, ticket.StatusDone)
	require.NoError(t, err)

	g, err := f.svc.Graph(f.ctx, &ticket.GraphQuery{})
	require.NoError(t, err)
	assert.ElementsMatch(t, []int64{work.ID, finished.ID}, nodeIDs(g))
	assert.Len(t, g.Edges, 1)
}

// What finishing a ticket frees counts the dependants the filter hid.
func TestGraphFilter_UnblocksCountsHiddenDependants(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	blocker := f.ticket(t, "blocker")
	shown := f.ticket(t, "shown dependant")
	hidden := f.ticket(t, "parked dependant")
	_, err := f.setStatus(t, hidden.ID, ticket.StatusParked)
	require.NoError(t, err)
	f.blockedBy(t, shown.ID, blocker.ID)
	f.blockedBy(t, hidden.ID, blocker.ID)

	full, err := f.svc.Graph(f.ctx, &ticket.GraphQuery{})
	require.NoError(t, err)
	filtered, err := f.svc.Graph(f.ctx, &ticket.GraphQuery{Filter: withoutResting()})
	require.NoError(t, err)

	assert.NotContains(t, nodeIDs(filtered), hidden.ID, "dependants are not drawn")
	assert.Equal(t, 2, f.graphNode(t, full, blocker.ID).Unblocks)
	assert.Equal(t, 2, f.graphNode(t, filtered, blocker.ID).Unblocks)
}

func TestGraphFilter_LimitKeepsTheNewestAndSaysSo(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	oldest := f.ticket(t, "oldest")
	middle := f.ticket(t, "middle")
	newest := f.ticket(t, "newest")

	g, err := f.svc.Graph(f.ctx, &ticket.GraphQuery{Limit: 2})
	require.NoError(t, err)
	assert.True(t, g.Truncated)
	assert.ElementsMatch(t, []int64{middle.ID, newest.ID}, nodeIDs(g))
	assert.NotContains(t, nodeIDs(g), oldest.ID)

	g, err = f.svc.Graph(f.ctx, &ticket.GraphQuery{Limit: 3})
	require.NoError(t, err)
	assert.False(t, g.Truncated)

	_, err = f.svc.Graph(f.ctx, &ticket.GraphQuery{Limit: ticket.MaxGraphLimit + 1})
	assert.ErrorIs(t, err, ticket.ErrInvalidPage)
}

func TestGraphFilter_TextFilterOverTheWire(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	blocker := f.ticket(t, "unrelated blocker")
	match := f.ticket(t, "needle work")
	other := f.ticket(t, "haystack")
	f.blockedBy(t, match.ID, blocker.ID)

	rec := send(t, mux, http.MethodGet, "/api/tickets/graph?q=needle", "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var body graphBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	ids := []int64{}
	for _, n := range body.Nodes {
		ids = append(ids, n.ID)
	}
	assert.ElementsMatch(t, []int64{match.ID, blocker.ID}, ids)
	assert.NotContains(t, ids, other.ID)
	assert.True(t, body.node(t, blocker.ID).Hidden)
	assert.False(t, body.node(t, match.ID).Hidden)

	assert.False(t, body.Truncated)

	rec = send(t, mux, http.MethodGet, "/api/tickets/graph?limit=1", "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var capped graphBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &capped))
	assert.True(t, capped.Truncated)

	rec = send(t, mux, http.MethodGet, fmt.Sprintf("/api/tickets/graph?limit=%d", ticket.MaxGraphLimit+1), "")
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

// A typo in a filter is refused, not answered with an empty graph.
func TestGraphFilter_AnInvalidFilterIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()

	_, err := f.svc.Graph(f.ctx, &ticket.GraphQuery{Filter: ticket.ListFilter{Statuses: []ticket.Status{"typo"}}})
	assert.ErrorIs(t, err, ticket.ErrInvalidEnum)

	rec := send(t, mux, http.MethodGet, "/api/tickets/graph?priority=typo", "")
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
}
