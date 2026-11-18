package pendingaction_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// proposeOn files n discussion tickets with a pending answer each, a second
// apart so the order is by time, and returns the proposals oldest first.
// titled names the tickets, so the ticket filter has something to match.
func proposeOn(t *testing.T, f *fixture, console uuid.UUID, n int, titled func(i int) string) []uuid.UUID {
	t.Helper()
	ids := make([]uuid.UUID, n)
	for i := range ids {
		created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: titled(i), Status: ticket.StatusOpen})
		require.NoError(t, err)
		f.exec(t, `UPDATE tickets SET status = 'discussion' WHERE id = $1`, created.ID)
		p, err := f.svc.Propose(f.ctx, answer(console, created.ID, "a"))
		require.NoError(t, err)
		ids[i] = p.ID
		f.clock.Advance(time.Second)
	}
	return ids
}

func viewIDs(views []pendingaction.View) []uuid.UUID {
	out := make([]uuid.UUID, len(views))
	for i := range views {
		out[i] = views[i].ID
	}
	return out
}

// walk pages the list to its end and returns every id in the order met.
func walk(t *testing.T, f *fixture, filter pendingaction.ListFilter) (ids []uuid.UUID, pages int) {
	t.Helper()
	for {
		page, err := f.svc.ViewsPage(f.ctx, testPerson(), &filter)
		require.NoError(t, err)
		pages++
		ids = append(ids, viewIDs(page.Views)...)
		if page.Next == nil {
			return ids, pages
		}
		filter.After = page.Next
		require.Less(t, pages, 100, "the walk does not end")
	}
}

func TestViewsPageWalksEveryProposalOnceInEitherOrder(t *testing.T) {
	f := newFixture(t, pendingaction.Config{MaxPendingPerAgent: 100, RatePerMinute: 100})
	console := f.newAgent(t, "console", true)
	oldestFirst := proposeOn(t, f, console, 7, func(int) string { return "a ticket" })
	newestFirst := make([]uuid.UUID, len(oldestFirst))
	for i, id := range oldestFirst {
		newestFirst[len(oldestFirst)-1-i] = id
	}

	got, pages := walk(t, f, pendingaction.ListFilter{Limit: 3})
	assert.Equal(t, newestFirst, got)
	assert.Equal(t, 3, pages)

	got, pages = walk(t, f, pendingaction.ListFilter{Limit: 3, Oldest: true})
	assert.Equal(t, oldestFirst, got)
	assert.Equal(t, 3, pages)

	page, err := f.svc.ViewsPage(f.ctx, testPerson(), &pendingaction.ListFilter{Limit: 7})
	require.NoError(t, err)
	assert.Nil(t, page.Next, "a page that holds the rest has no cursor")
}

func TestViewsPageKeepsCreatedAtTiesInOrderAcrossPages(t *testing.T) {
	f := newFixture(t, pendingaction.Config{MaxPendingPerAgent: 100, RatePerMinute: 100})
	console := f.newAgent(t, "console", true)
	made := proposeOn(t, f, console, 5, func(int) string { return "a ticket" })
	f.exec(t, `UPDATE pending_actions SET created_at = $1`, epoch)
	byID := slices.Clone(made)
	slices.SortFunc(byID, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })

	for _, oldest := range []bool{false, true} {
		got, _ := walk(t, f, pendingaction.ListFilter{Limit: 2, Oldest: oldest})
		assert.Equal(t, byID, got, "ties fall to the id, ascending, with none repeated or skipped (oldest %v)", oldest)
	}
}

func TestViewsPageDefaultsToTheDefaultPageSize(t *testing.T) {
	f := newFixture(t, pendingaction.Config{MaxPendingPerAgent: 100, RatePerMinute: 100})
	console := f.newAgent(t, "console", true)
	proposeOn(t, f, console, pendingaction.DefaultPageSize+1, func(int) string { return "a ticket" })

	page, err := f.svc.ViewsPage(f.ctx, testPerson(), &pendingaction.ListFilter{})
	require.NoError(t, err)
	assert.Len(t, page.Views, pendingaction.DefaultPageSize)
	assert.NotNil(t, page.Next)
}

func TestViewsPageCombinesCursorLimitAndTicketFilter(t *testing.T) {
	f := newFixture(t, pendingaction.Config{MaxPendingPerAgent: 100, RatePerMinute: 100})
	console := f.newAgent(t, "console", true)
	all := proposeOn(t, f, console, 9, func(i int) string {
		if i%3 == 0 {
			return "fix the zebra"
		}
		return "tidy the garden"
	})
	zebras := []uuid.UUID{all[0], all[3], all[6]}

	filter := pendingaction.ListFilter{Limit: 2, Oldest: true, Ticket: &ticket.ListFilter{Text: "zebra"}}
	first, err := f.svc.ViewsPage(f.ctx, testPerson(), &filter)
	require.NoError(t, err)
	assert.Equal(t, zebras[:2], viewIDs(first.Views), "the filter selects before the limit applies")
	require.NotNil(t, first.Next)

	filter.After = first.Next
	second, err := f.svc.ViewsPage(f.ctx, testPerson(), &filter)
	require.NoError(t, err)
	assert.Equal(t, zebras[2:], viewIDs(second.Views))
	assert.Nil(t, second.Next)

	// A proposal whose ticket stops matching between pages is skipped, not
	// repeated, and the walk still ends.
	f.exec(t, `UPDATE tickets SET title = 'tidy' WHERE id = $1`, second.Views[0].TicketID)
	filter.After = first.Next
	third, err := f.svc.ViewsPage(f.ctx, testPerson(), &filter)
	require.NoError(t, err)
	assert.Empty(t, third.Views)
}

func TestViewsPageRefusesACursorFromAnotherListOrABadLimit(t *testing.T) {
	f := newFixture(t, pendingaction.Config{MaxPendingPerAgent: 100, RatePerMinute: 100})
	console := f.newAgent(t, "console", true)
	proposeOn(t, f, console, 3, func(int) string { return "a ticket" })

	page, err := f.svc.ViewsPage(f.ctx, testPerson(), &pendingaction.ListFilter{Limit: 1})
	require.NoError(t, err)
	require.NotNil(t, page.Next)

	for name, filter := range map[string]pendingaction.ListFilter{
		"another order":  {Limit: 1, Oldest: true, After: page.Next},
		"another filter": {Limit: 1, Ticket: &ticket.ListFilter{Text: "zebra"}, After: page.Next},
		"another state":  {Limit: 1, States: []pendingaction.State{pendingaction.StatePending}, After: page.Next},
		"too big":        {Limit: pendingaction.MaxPageSize + 1},
		"negative":       {Limit: -1},
	} {
		_, err := f.svc.ViewsPage(f.ctx, testPerson(), &filter)
		assert.ErrorIs(t, err, ticket.ErrInvalidPage, name)
	}
	for name, c := range map[string]*pendingaction.PageCursor{
		"no time": {Query: page.Next.Query, ID: page.Next.ID},
		"no id":   {Query: page.Next.Query, CreatedAt: page.Next.CreatedAt},
	} {
		_, err := pendingaction.DecodePageCursor(c.Encode())
		assert.ErrorIs(t, err, ticket.ErrInvalidPage, name)
	}
	changedSize := pendingaction.ListFilter{Limit: 2, After: page.Next}
	_, err = f.svc.ViewsPage(f.ctx, testPerson(), &changedSize)
	assert.NoError(t, err, "the page size may change mid-walk")
}

func TestListOverHTTPPagesWithACursor(t *testing.T) {
	w := newWire(t)
	var made []string
	for i := 0; i < 5; i++ {
		made = append(made, w.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{}).ID.String())
		w.clock.Advance(time.Second)
	}
	get := func(query string) (int, pendingaction.ProposalListBody) {
		rec := w.as(t, userToken, http.MethodGet, "/api/proposals?"+query, "")
		var list pendingaction.ProposalListBody
		if rec.Code == http.StatusOK {
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
		}
		return rec.Code, list
	}

	var got []string
	query := "state=pending&order=oldest&limit=2"
	for pages := 0; ; pages++ {
		require.Less(t, pages, 10)
		code, list := get(query)
		require.Equal(t, http.StatusOK, code)
		for _, p := range list.Proposals {
			got = append(got, p.ID)
		}
		if list.NextCursor == "" {
			break
		}
		query = "state=pending&order=oldest&limit=2&cursor=" + list.NextCursor
	}
	assert.Equal(t, made, got)

	for _, bad := range []string{"cursor=garbage", "limit=0", "limit=101", "order=sideways"} {
		code, _ := get(bad)
		assert.Equal(t, http.StatusUnprocessableEntity, code, bad)
	}
	_, first := get("limit=1&order=oldest")
	code, _ := get("limit=1&cursor=" + first.NextCursor)
	assert.Equal(t, http.StatusUnprocessableEntity, code, "a cursor of one order is refused under another")
}
