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

func blockedIDs(p *ticket.Page) []int64 {
	ids := []int64{}
	for _, tk := range p.Tickets {
		ids = append(ids, tk.ID)
	}
	return ids
}

// chainOfThree files a <- b <- c (c waits on b, b on a) plus d, which waits
// on a alone. Finishing a frees b, c and d; finishing b frees c.
func (f *fixture) chainOfThree(t *testing.T) (a, b, c, d *ticket.Ticket) {
	t.Helper()
	a, b, c, d = f.ticket(t, "a"), f.ticket(t, "b"), f.ticket(t, "c"), f.ticket(t, "d")
	f.blockedBy(t, b.ID, a.ID)
	f.blockedBy(t, c.ID, b.ID)
	f.blockedBy(t, d.ID, a.ID)
	return a, b, c, d
}

// The list holds the blocked tickets, the one holding up the most first,
// and the count it carries is the graph's, not a second definition.
func TestBlockedPage_RanksBlockedTicketsByWhatEachHoldsUp(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b, c, d := f.chainOfThree(t)
	free := f.ticket(t, "waits on nothing")

	page, err := f.svc.BlockedPage(f.ctx, &ticket.ListFilter{})
	require.NoError(t, err)

	// a is not blocked, so it is not listed however much it holds up. b
	// frees c; c and d free nothing and tie, newest first.
	assert.Equal(t, []int64{b.ID, d.ID, c.ID}, blockedIDs(page))
	assert.NotContains(t, blockedIDs(page), free.ID)
	assert.NotContains(t, blockedIDs(page), a.ID)

	graph, err := f.svc.Graph(f.ctx, &ticket.GraphQuery{})
	require.NoError(t, err)
	for _, tk := range page.Tickets {
		assert.Equal(t, f.graphNode(t, graph, tk.ID).Unblocks, tk.Unblocks, "#%d", tk.ID)
	}
	assert.Equal(t, 1, page.Tickets[0].Unblocks)
}

// A blocker counts only while unfinished: a ticket whose blockers are all
// finished is free, and a finished ticket is never listed.
func TestBlockedPage_FinishedBlockersAndFinishedTicketsAreLeftOut(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	blocker := f.ticket(t, "blocker")
	freed := f.ticket(t, "its blocker is done")
	finished := f.ticket(t, "done, with an open blocker")
	open := f.ticket(t, "still waiting")
	other := f.ticket(t, "other blocker")
	f.blockedBy(t, freed.ID, blocker.ID)
	f.blockedBy(t, finished.ID, other.ID)
	f.blockedBy(t, open.ID, other.ID)
	_, err := f.setStatus(t, blocker.ID, ticket.StatusDone)
	require.NoError(t, err)
	_, err = f.setStatus(t, finished.ID, ticket.StatusDone)
	require.NoError(t, err)

	page, err := f.svc.BlockedPage(f.ctx, &ticket.ListFilter{})
	require.NoError(t, err)
	assert.Equal(t, []int64{open.ID}, blockedIDs(page))
}

func TestBlockedPage_ACancelledBlockerNoLongerBlocks(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	blocker := f.ticket(t, "blocker")
	freed := f.ticket(t, "its blocker is cancelled")
	f.blockedBy(t, freed.ID, blocker.ID)
	_, err := f.setStatus(t, blocker.ID, ticket.StatusCancelled)
	require.NoError(t, err)

	page, err := f.svc.BlockedPage(f.ctx, &ticket.ListFilter{})
	require.NoError(t, err)
	assert.NotContains(t, blockedIDs(page), freed.ID)
}

func TestBlockedPage_AppliesTheFilter(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b, _, _ := f.chainOfThree(t)
	c := f.parked(t, "parked, and waiting")
	f.blockedBy(t, c.ID, a.ID)

	page, err := f.svc.BlockedPage(f.ctx, &ticket.ListFilter{Statuses: []ticket.Status{ticket.StatusBlocked}})
	require.NoError(t, err)
	assert.NotContains(t, blockedIDs(page), c.ID, "parked is filtered out")
	assert.Contains(t, blockedIDs(page), b.ID)

	// Blocked is by its dependencies, not its status column: with no status
	// filter the parked ticket waiting on an unfinished ticket is listed.
	page, err = f.svc.BlockedPage(f.ctx, &ticket.ListFilter{})
	require.NoError(t, err)
	assert.Contains(t, blockedIDs(page), c.ID)

	page, err = f.svc.BlockedPage(f.ctx, &ticket.ListFilter{Text: "no such words"})
	require.NoError(t, err)
	assert.Empty(t, page.Tickets)
}

// A keyset walk through ties visits each ticket once, in order, however
// small the pages.
func TestBlockedPage_PagesWithoutRepeatsOrGaps(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	root := f.ticket(t, "root")
	var want []int64
	// Two of them hold one ticket up each, so the walk crosses a change of
	// rank as well as ties.
	for i := range 5 {
		w := f.ticket(t, fmt.Sprintf("waiting %d", i))
		f.blockedBy(t, w.ID, root.ID)
		if i < 2 {
			tail := f.ticket(t, fmt.Sprintf("tail %d", i))
			f.blockedBy(t, tail.ID, w.ID)
			want = append(want, tail.ID)
		}
		want = append(want, w.ID)
	}
	full, err := f.svc.BlockedPage(f.ctx, &ticket.ListFilter{})
	require.NoError(t, err)
	require.Len(t, full.Tickets, len(want))
	require.Nil(t, full.Next)

	var walked []int64
	filter := &ticket.ListFilter{Limit: 2}
	for pages := 0; ; pages++ {
		require.Less(t, pages, len(want), "the walk does not end")
		page, err := f.svc.BlockedPage(f.ctx, filter)
		require.NoError(t, err)
		walked = append(walked, blockedIDs(page)...)
		if page.Next == nil {
			break
		}
		filter.After = page.Next
	}
	assert.Equal(t, blockedIDs(full), walked)
	assert.ElementsMatch(t, want, walked)
}

func TestBlockedPage_RefusesACursorFromAnotherList(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.chainOfThree(t)
	board, err := f.svc.ListPage(f.ctx, &ticket.ListFilter{Limit: 1})
	require.NoError(t, err)
	require.NotNil(t, board.Next)

	_, err = f.svc.BlockedPage(f.ctx, &ticket.ListFilter{Limit: 1, After: board.Next})
	require.ErrorIs(t, err, ticket.ErrInvalidPage)

	blocked, err := f.svc.BlockedPage(f.ctx, &ticket.ListFilter{Limit: 1})
	require.NoError(t, err)
	require.NotNil(t, blocked.Next)
	_, err = f.svc.ListPage(f.ctx, &ticket.ListFilter{Limit: 1, After: blocked.Next})
	require.ErrorIs(t, err, ticket.ErrInvalidPage, "and the other way round")

	_, err = f.svc.BlockedPage(f.ctx, &ticket.ListFilter{Limit: 1, Text: "other", After: blocked.Next})
	require.ErrorIs(t, err, ticket.ErrInvalidPage, "another filter")
}

func TestBlockedTickets_Wire(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, b, c, d := f.chainOfThree(t)
	mux := f.boardMux()

	rec := send(t, mux, http.MethodGet, "/api/tickets/blocked?limit=2", "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var body struct {
		Tickets []struct {
			ID       int64 `json:"id"`
			Unblocks int   `json:"unblocks"`
		} `json:"tickets"`
		NextCursor string `json:"next_cursor"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Tickets, 2)
	assert.Equal(t, b.ID, body.Tickets[0].ID)
	assert.Equal(t, 1, body.Tickets[0].Unblocks)
	assert.Equal(t, d.ID, body.Tickets[1].ID)
	require.NotEmpty(t, body.NextCursor)

	rec = send(t, mux, http.MethodGet, "/api/tickets/blocked?limit=2&cursor="+body.NextCursor, "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	body.NextCursor = ""
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Tickets, 1)
	assert.Equal(t, c.ID, body.Tickets[0].ID)
	assert.Empty(t, body.NextCursor)

	rec = send(t, mux, http.MethodGet, "/api/tickets/blocked?cursor=garbage", "")
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
}

// What a ticket's own graph shows is what is in its way, with nothing
// marked as hidden by a filter nobody applied.
func TestBlockers_ParkedUpstreamIsPartOfTheAnswer(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	parked := f.parked(t, "parked blocker")
	work := f.ticket(t, "work")
	unrelated := f.ticket(t, "unrelated chain")
	f.blockedBy(t, work.ID, parked.ID)
	f.blockedBy(t, unrelated.ID, f.ticket(t, "its blocker").ID)

	g, err := f.svc.Blockers(f.ctx, work.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []int64{work.ID, parked.ID}, nodeIDs(g))
	assert.False(t, f.graphNode(t, g, parked.ID).Hidden)
	assert.True(t, f.graphNode(t, g, work.ID).Blocked)
}

// Unblocks in a ticket's own graph is the board's, not the graph drawn's.
func TestBlockers_UnblocksCountsDependantsOutsideTheGraph(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b, _, _ := f.chainOfThree(t)

	g, err := f.svc.Blockers(f.ctx, b.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []int64{a.ID, b.ID}, nodeIDs(g))
	assert.Equal(t, 3, f.graphNode(t, g, a.ID).Unblocks, "b, c and d, though only b is drawn")
}
