package ticket_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

func (f *fixture) listIDs(t *testing.T, filter ticket.ListFilter) []int64 {
	t.Helper()
	tickets, err := f.svc.List(f.ctx, &filter)
	require.NoError(t, err)
	ids := []int64{}
	for _, tk := range tickets {
		ids = append(ids, tk.ID)
	}
	return ids
}

// walk pages through every ticket of filter, round-tripping each cursor
// through its wire form as a client does.
func (f *fixture) walk(t *testing.T, filter ticket.ListFilter) (ids []int64, pages int) {
	t.Helper()
	for {
		page, err := f.svc.ListPage(f.ctx, &filter)
		require.NoError(t, err)
		ids = append(ids, pageIDs(page)...)
		pages++
		if page.Next == nil {
			return ids, pages
		}
		filter.After, err = ticket.DecodePageCursor(page.Next.Encode())
		require.NoError(t, err)
	}
}

func TestTicketPredicate_CombinesEveryField(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	open := f.ticket(t, "Fix widget")
	urgent := f.ticket(t, "Fix widget urgently")
	other := f.ticket(t, "Gadget")
	f.run(t, `UPDATE tickets SET priority = 'urgent' WHERE id = $1`, urgent.ID)
	f.run(t, `UPDATE tickets SET status = 'blocked' WHERE id = $1`, other.ID)
	f.run(t, `UPDATE tickets SET created_at = '2020-01-02T10:00:00Z', updated_at = '2021-05-05T00:00:00Z' WHERE id = $1`, open.ID)
	at := func(s string) *time.Time {
		v, err := time.Parse(time.RFC3339, s)
		require.NoError(t, err)
		return &v
	}

	assert.Equal(t, []int64{urgent.ID}, f.listIDs(t, ticket.ListFilter{Priorities: []model.Priority{model.PriorityUrgent}}))
	assert.Equal(t, []int64{other.ID}, f.listIDs(t, ticket.ListFilter{Statuses: []ticket.Status{ticket.StatusBlocked}}))
	assert.Equal(t, []int64{urgent.ID, open.ID}, f.listIDs(t, ticket.ListFilter{Text: "widget"}))
	assert.Equal(t, []int64{open.ID}, f.listIDs(t, ticket.ListFilter{
		Created: ticket.TimeRange{From: at("2020-01-02T00:00:00Z"), Before: at("2020-01-03T00:00:00Z")}}))
	assert.Equal(t, []int64{open.ID}, f.listIDs(t, ticket.ListFilter{
		Updated: ticket.TimeRange{Before: at("2022-01-01T00:00:00Z")}}))
	assert.Equal(t, []int64{open.ID}, f.listIDs(t, ticket.ListFilter{
		Text: "widget", Statuses: []ticket.Status{ticket.StatusOpen},
		Priorities: []model.Priority{model.PriorityNormal}, Created: ticket.TimeRange{Before: at("2021-01-01T00:00:00Z")}}))
}

func TestListPage_NewestWalksEveryTicketOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var want []int64
	for i := 0; i < 5; i++ {
		want = append([]int64{f.ticket(t, fmt.Sprintf("T%d", i)).ID}, want...)
	}
	got, pages := f.walk(t, ticket.ListFilter{Limit: 2})
	assert.Equal(t, want, got)
	assert.Equal(t, 3, pages)
}

// A sort with mixed directions and a nullable column: the case the (a, b) <
// ($1, $2) row comparison cannot express, and the shape Approvals' order
// (created_at DESC, id ASC) has.
func TestListPage_MixedDirectionsAndNulls(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var ids []int64
	for i := 0; i < 6; i++ {
		ids = append(ids, f.ticket(t, fmt.Sprintf("T%d", i)).ID)
	}
	// Ties on closed_at, and two NULLs.
	f.run(t, `UPDATE tickets SET closed_at = '2026-01-02T00:00:00Z' WHERE id = ANY($1)`, []int64{ids[0], ids[1]})
	f.run(t, `UPDATE tickets SET closed_at = '2026-01-01T00:00:00Z' WHERE id = ANY($1)`, []int64{ids[2], ids[3]})
	id := func(desc bool) ticket.SortKey {
		return ticket.SortKey{Column: "id", Desc: desc, Kind: ticket.KeyInt, Value: func(tk *ticket.Ticket) any { return tk.ID }}
	}
	closed := func(desc bool) ticket.SortKey {
		return ticket.SortKey{Column: "closed_at", Desc: desc, Nullable: true, Kind: ticket.KeyTime, Value: func(tk *ticket.Ticket) any { return tk.ClosedAt }}
	}
	closedExpr := ticket.SortKey{Expr: "t.closed_at", Desc: true, Nullable: true, Kind: ticket.KeyTime, Value: func(tk *ticket.Ticket) any { return tk.ClosedAt }}
	for name, tc := range map[string]struct {
		keys []ticket.SortKey
		want []int64
	}{
		"nullable expression key": {[]ticket.SortKey{closedExpr, id(false)}, []int64{ids[0], ids[1], ids[2], ids[3], ids[4], ids[5]}},
		"closed desc, id asc":     {[]ticket.SortKey{closed(true), id(false)}, []int64{ids[0], ids[1], ids[2], ids[3], ids[4], ids[5]}},
		"closed asc, id desc":     {[]ticket.SortKey{closed(false), id(true)}, []int64{ids[3], ids[2], ids[1], ids[0], ids[5], ids[4]}},
	} {
		t.Run(name, func(t *testing.T) {
			got, _ := f.walk(t, ticket.ListFilter{Limit: 2, Sort: &ticket.Sort{Name: name, Keys: tc.keys}})
			assert.Equal(t, tc.want, got)
			// Every page size agrees with the unpaged order.
			for _, size := range []int{1, 3, 5} {
				got, _ := f.walk(t, ticket.ListFilter{Limit: size, Sort: &ticket.Sort{Name: name, Keys: tc.keys}})
				assert.Equal(t, tc.want, got, "limit %d", size)
			}
		})
	}
}

func TestListPage_RejectsACursorFromAnotherQuery(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for i := 0; i < 4; i++ {
		f.ticket(t, fmt.Sprintf("T%d", i))
	}
	first := ticket.ListFilter{Limit: 1, Statuses: []ticket.Status{ticket.StatusOpen}}
	page, err := f.svc.ListPage(f.ctx, &first)
	require.NoError(t, err)
	cursor, err := ticket.DecodePageCursor(page.Next.Encode())
	require.NoError(t, err)

	recently, err := ticket.SortByName(ticket.SortRecentlyClosed)
	require.NoError(t, err)
	for name, other := range map[string]ticket.ListFilter{
		"status":    {Statuses: []ticket.Status{ticket.StatusBlocked}},
		"no status": {},
		"text":      {Statuses: first.Statuses, Text: "T1"},
		"priority":  {Statuses: first.Statuses, Priorities: []model.Priority{model.PriorityLow}},
		"date":      {Statuses: first.Statuses, Created: ticket.TimeRange{From: &time.Time{}}},
		"sort":      {Statuses: first.Statuses, Sort: recently},
	} {
		other.After = cursor
		_, err := f.svc.ListPage(f.ctx, &other)
		assert.ErrorIs(t, err, ticket.ErrInvalidPage, name)
	}

	// The page size is not part of the query.
	again := first
	again.Limit = 3
	again.After = cursor
	_, err = f.svc.ListPage(f.ctx, &again)
	assert.NoError(t, err)
}

func TestListPage_LimitsAreEnforced(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for i := 0; i < ticket.DefaultPageSize+1; i++ {
		f.ticket(t, fmt.Sprintf("T%d", i))
	}
	page, err := f.svc.ListPage(f.ctx, &ticket.ListFilter{})
	require.NoError(t, err)
	assert.Len(t, page.Tickets, ticket.DefaultPageSize)
	assert.NotNil(t, page.Next)

	for _, limit := range []int{-1, ticket.MaxPageSize + 1} {
		_, err := f.svc.ListPage(f.ctx, &ticket.ListFilter{Limit: limit})
		assert.ErrorIs(t, err, ticket.ErrInvalidPage, "limit %d", limit)
	}
}

func TestListPage_SummaryLeavesOutTheBody(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created, err := f.svc.Create(f.ctx, &ticket.CreateParams{Title: "T", Body: "the long body"})
	require.NoError(t, err)

	full, err := f.svc.ListPage(f.ctx, &ticket.ListFilter{})
	require.NoError(t, err)
	assert.Equal(t, "the long body", full.Tickets[0].Body)
	summary, err := f.svc.ListPage(f.ctx, &ticket.ListFilter{Summary: true})
	require.NoError(t, err)
	assert.Equal(t, created.ID, summary.Tickets[0].ID)
	assert.Empty(t, summary.Tickets[0].Body)
}

func TestListBoard_CountsEveryColumnAndPagesEach(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var open []int64
	for i := 0; i < 3; i++ {
		open = append([]int64{f.ticket(t, fmt.Sprintf("Open %d", i)).ID}, open...)
	}
	blocked := f.ticket(t, "Blocked one")
	f.run(t, `UPDATE tickets SET status = 'blocked' WHERE id = $1`, blocked.ID)

	filter := ticket.ListFilter{Limit: 2, Statuses: []ticket.Status{ticket.StatusOpen, ticket.StatusBlocked, ticket.StatusDone}}
	columns, err := f.svc.ListBoard(f.ctx, &filter)
	require.NoError(t, err)
	require.Len(t, columns, 3)
	assert.Equal(t, ticket.StatusOpen, columns[0].Status)
	assert.Equal(t, 3, columns[0].Count)
	assert.Equal(t, open[:2], pageIDs(columns[0].Page))
	require.NotNil(t, columns[0].Page.Next)
	assert.Equal(t, 1, columns[1].Count)
	assert.Equal(t, []int64{blocked.ID}, pageIDs(columns[1].Page))
	assert.Nil(t, columns[1].Page.Next)
	assert.Equal(t, 0, columns[2].Count)
	assert.Empty(t, columns[2].Page.Tickets)

	// The column's cursor continues it under the column's own filter.
	more := ticket.ListFilter{Limit: 2, Statuses: []ticket.Status{ticket.StatusOpen}}
	more.After, err = ticket.DecodePageCursor(columns[0].Page.Next.Encode())
	require.NoError(t, err)
	rest, err := f.svc.ListPage(f.ctx, &more)
	require.NoError(t, err)
	assert.Equal(t, open[2:], pageIDs(rest))

	// The count follows the filter.
	filtered, err := f.svc.ListBoard(f.ctx, &ticket.ListFilter{Statuses: []ticket.Status{ticket.StatusOpen}, Text: "Open 1"})
	require.NoError(t, err)
	assert.Equal(t, 1, filtered[0].Count)
}

func TestListBoard_CountsHonourEveryFilterField(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	urgent := f.ticket(t, "Urgent")
	low := f.ticket(t, "Low")
	f.run(t, `UPDATE tickets SET priority = 'urgent' WHERE id = $1`, urgent.ID)
	f.run(t, `UPDATE tickets SET priority = 'low', created_at = '2020-01-01', updated_at = '2020-01-01' WHERE id = $1`, low.ID)

	count := func(filter ticket.ListFilter) int {
		columns, err := f.svc.ListBoard(f.ctx, &filter)
		require.NoError(t, err)
		for _, c := range columns {
			if c.Status == ticket.StatusOpen {
				return c.Count
			}
		}
		t.Fatal("no open column")
		return 0
	}
	cutoff := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	assert.Equal(t, 2, count(ticket.ListFilter{}))
	assert.Equal(t, 1, count(ticket.ListFilter{Priorities: []model.Priority{model.PriorityUrgent}}))
	assert.Equal(t, 1, count(ticket.ListFilter{Created: ticket.TimeRange{From: &cutoff}}))
	assert.Equal(t, 1, count(ticket.ListFilter{Created: ticket.TimeRange{Before: &cutoff}}))
	assert.Equal(t, 1, count(ticket.ListFilter{Updated: ticket.TimeRange{From: &cutoff}}))
}

func TestListBoard_NoStatusIsAColumnPerStatus(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.ticket(t, "One")
	columns, err := f.svc.ListBoard(f.ctx, &ticket.ListFilter{})
	require.NoError(t, err)
	var got []ticket.Status
	for _, c := range columns {
		got = append(got, c.Status)
		if c.Status == ticket.StatusOpen {
			assert.Equal(t, 1, c.Count)
			assert.Len(t, c.Page.Tickets, 1)
		} else {
			assert.Equal(t, 0, c.Count, c.Status)
		}
	}
	assert.Equal(t, model.Statuses(), got)
}

func TestListTicketsRoute_IsAlwaysBounded(t *testing.T) {
	t.Parallel()
	mux := newTestStack(t).mux
	for i := 0; i < ticket.DefaultPageSize+1; i++ {
		rec := send(t, mux, http.MethodPost, "/api/tickets", fmt.Sprintf(`{"title": "T%d"}`, i))
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	}

	rec := send(t, mux, http.MethodGet, "/api/tickets", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list struct {
		Tickets    []json.RawMessage `json:"tickets"`
		NextCursor string            `json:"next_cursor"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	assert.Len(t, list.Tickets, ticket.DefaultPageSize)
	assert.NotEmpty(t, list.NextCursor)

	rec = send(t, mux, http.MethodGet, "/api/tickets?limit=201", "")
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	rec = send(t, mux, http.MethodGet, "/api/tickets?sort=bogus", "")
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	rec = send(t, mux, http.MethodGet, "/api/tickets?cursor=!!", "")
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func (f *fixture) run(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := f.database.Exec(f.ctx, sql, args...)
	require.NoError(t, err)
}

func TestListTicketsRoute_TranslatesTheQuery(t *testing.T) {
	t.Parallel()
	stack := newTestStack(t)
	mux := stack.mux

	rec := send(t, mux, http.MethodGet, "/api/tickets?status=open&status=blocked&priority=high&priority=urgent"+
		"&created_from=2026-01-02&created_to=2026-01-03&updated_from=2026-02-01&closed_to=2026-03-04"+
		"&sort=recently_closed&limit=7&summary=true", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got := stack.store.listedWith()
	require.NotNil(t, got)
	assert.Equal(t, []ticket.Status{ticket.StatusOpen, ticket.StatusBlocked}, got.Statuses)
	assert.Equal(t, []model.Priority{model.PriorityHigh, model.PriorityUrgent}, got.Priorities)
	day := func(s string) *time.Time {
		v, err := time.Parse(time.DateOnly, s)
		require.NoError(t, err)
		return &v
	}
	// "to" is inclusive of its whole day.
	assert.Equal(t, ticket.TimeRange{From: day("2026-01-02"), Before: day("2026-01-04")}, got.Created)
	assert.Equal(t, ticket.TimeRange{From: day("2026-02-01")}, got.Updated)
	assert.Equal(t, ticket.TimeRange{Before: day("2026-03-05")}, got.Closed)
	assert.Equal(t, ticket.SortRecentlyClosed, got.Sort.Name)
	assert.Equal(t, 8, got.Limit, "the page size plus the one read to know whether more follows")
	assert.True(t, got.Summary)

	for _, bad := range []string{"priority=bogus", "status=bogus", "created_from=soon", "updated_to=2026-13-01"} {
		rec := send(t, mux, http.MethodGet, "/api/tickets?"+bad, "")
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "%s: %s", bad, rec.Body.String())
	}
}

func TestGetBoardRoute(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	first := f.ticket(t, "Open one")
	second := f.ticket(t, "Open two")
	f.ticket(t, "Open three")

	type column struct {
		Status     string              `json:"status"`
		Count      int                 `json:"count"`
		Tickets    []ticket.TicketBody `json:"tickets"`
		NextCursor string              `json:"next_cursor"`
	}
	get := func(query string) (int, []column) {
		rec := send(t, mux, http.MethodGet, "/api/board?"+query, "")
		var body struct {
			Columns []column `json:"columns"`
		}
		if rec.Code == http.StatusOK {
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		}
		return rec.Code, body.Columns
	}

	// A status named twice is one column.
	code, columns := get("status=open&status=open&status=done&limit=2&summary=true")
	require.Equal(t, http.StatusOK, code)
	require.Len(t, columns, 2)
	assert.Equal(t, "open", columns[0].Status)
	assert.Equal(t, 3, columns[0].Count)
	require.Len(t, columns[0].Tickets, 2)
	assert.Equal(t, []int64{columns[0].Tickets[0].ID, columns[0].Tickets[1].ID}, []int64{second.ID + 1, second.ID})
	assert.NotEqual(t, first.ID, columns[0].Tickets[0].ID)
	assert.NotEmpty(t, columns[0].NextCursor)
	assert.Equal(t, 0, columns[1].Count)

	// The column's cursor continues it through the list route.
	rec := send(t, mux, http.MethodGet, "/api/tickets?status=open&limit=2&summary=true&cursor="+columns[0].NextCursor, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var rest struct {
		Tickets []ticket.TicketBody `json:"tickets"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rest))
	require.Len(t, rest.Tickets, 1)
	assert.Equal(t, first.ID, rest.Tickets[0].ID)

	for _, bad := range []string{"cursor=" + columns[0].NextCursor, "status=bogus", "sort=bogus"} {
		code, _ := get(bad)
		assert.Equal(t, http.StatusUnprocessableEntity, code, bad)
	}
}
