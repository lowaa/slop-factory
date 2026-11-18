package reports_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reports"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// fakeStore returns what the test gives it and records what it was asked.
// What the queries compute is tested against the real database.
type fakeStore struct {
	summary *reports.Summary
	waiting *reports.WaitingPage
	usage   *reports.UsageReport
	tickets *reports.UsageTicketPage
	err     error

	scope              *reports.ScopeCostInput
	lockHistory        *reports.LockHistoryInput
	shadow             *reports.ShadowDecisions
	scopeFrom, scopeTo time.Time

	usageFilter *reports.UsageFilter
	ticketQuery *reports.UsageTicketQuery

	filter   *reports.Filter
	status   string
	priority string
	limit    int
	offset   int
	called   int
}

func (f *fakeStore) Summary(_ context.Context, filter *reports.Filter) (*reports.Summary, error) {
	f.called++
	f.filter = filter
	return f.summary, f.err
}

func (f *fakeStore) Waiting(_ context.Context, status, priority string, limit, offset int) (*reports.WaitingPage, error) {
	f.called++
	f.status, f.priority, f.limit, f.offset = status, priority, limit, offset
	return f.waiting, f.err
}

func (f *fakeStore) LLMUsage(_ context.Context, filter *reports.UsageFilter) (*reports.UsageReport, error) {
	f.called++
	f.usageFilter = filter
	return f.usage, f.err
}

func (f *fakeStore) ScopeCostRows(_ context.Context, from, to time.Time) (*reports.ScopeCostInput, error) {
	f.called++
	f.scopeFrom, f.scopeTo = from, to
	return f.scope, f.err
}

func (f *fakeStore) LockHistoryRows(_ context.Context, from, to time.Time) (*reports.LockHistoryInput, error) {
	f.called++
	f.scopeFrom, f.scopeTo = from, to
	return f.lockHistory, f.err
}

func (f *fakeStore) ShadowDecisions(_ context.Context, from, to time.Time) (*reports.ShadowDecisions, error) {
	f.called++
	f.scopeFrom, f.scopeTo = from, to
	return f.shadow, f.err
}

func (f *fakeStore) LLMUsageTickets(_ context.Context, filter *reports.UsageFilter, q *reports.UsageTicketQuery) (*reports.UsageTicketPage, error) {
	f.called++
	f.usageFilter, f.ticketQuery = filter, q
	return f.tickets, f.err
}

func serve(t *testing.T, store reports.Store) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	api := humago.New(mux, apiserver.NewConfig("test", "0.0.0"))
	reports.RegisterRoutes(api, reports.NewHandler(reports.NewService(store, func() time.Time { return now }), slog.DiscardHandler))
	return mux
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestGetReportDefaultsToThirtyDaysEndingNow(t *testing.T) {
	t.Parallel()
	store := &fakeStore{summary: &reports.Summary{}}

	rec := get(t, serve(t, store), "/api/reports")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, now, store.filter.To)
	assert.Equal(t, now.AddDate(0, 0, -30), store.filter.From)
}

func TestGetReportRejectsWhatItDoesNotOffer(t *testing.T) {
	t.Parallel()
	store := &fakeStore{summary: &reports.Summary{}}
	h := serve(t, store)

	for _, target := range []string{"/api/reports?days=365", "/api/reports?priority=meh", "/api/reports/waiting?status=done",
		"/api/reports/waiting?status=open&limit=500", "/api/reports/waiting?status=open&offset=2147483647"} {
		assert.Equal(t, http.StatusUnprocessableEntity, get(t, h, target).Code, target)
	}
	assert.Zero(t, store.called, "an invalid request never reaches the store")
}

func TestGetReportShowsEveryStateAndAbsentFiguresAsNull(t *testing.T) {
	t.Parallel()
	store := &fakeStore{summary: &reports.Summary{States: []reports.StateFigure{
		{State: "discussion", Stays: 1, TotalSeconds: 300, Duration: &reports.Percentiles{P50: 300, P90: 300}},
		{State: "review", Stays: 1, TotalSeconds: 100, Duration: &reports.Percentiles{P50: 100, P90: 100}},
	}}}

	rec := get(t, serve(t, store), "/api/reports")

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		HistoryFrom *time.Time `json:"history_from"`
		Lead        struct {
			CreatedToDone *struct{} `json:"created_to_done"`
		}
		States []struct {
			State    string    `json:"state"`
			Duration *struct{} `json:"duration"`
			Share    *float64  `json:"share"`
		}
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Nil(t, body.HistoryFrom)
	assert.Nil(t, body.Lead.CreatedToDone, "no done tickets is absent, not zero")
	require.Len(t, body.States, 7, "parked is left out unless asked for")
	assert.Equal(t, "in_merge_queue", body.States[3].State, "after in_progress")
	for _, s := range body.States {
		switch s.State {
		case "discussion":
			assert.InDelta(t, 0.75, *s.Share, 1e-9)
		case "review":
			assert.InDelta(t, 0.25, *s.Share, 1e-9)
		default:
			assert.Nil(t, s.Duration, s.State)
			assert.Nil(t, s.Share, s.State)
		}
	}
}

func TestIncludeParkedIsPassedAndShowsTheParkedState(t *testing.T) {
	t.Parallel()
	store := &fakeStore{summary: &reports.Summary{IncludeParked: true}}

	rec := get(t, serve(t, store), "/api/reports?include_parked=true")
	assert.Contains(t, rec.Body.String(), `"parked_not_shown":0`)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, store.filter.IncludeParked)
	assert.Contains(t, rec.Body.String(), `"state":"parked"`)

	get(t, serve(t, store), "/api/reports")
	assert.False(t, store.filter.IncludeParked, "off by default")
}

func TestParkedNotShownIsReported(t *testing.T) {
	t.Parallel()
	rec := get(t, serve(t, &fakeStore{summary: &reports.Summary{ParkedNotShown: 3}}), "/api/reports")
	assert.Contains(t, rec.Body.String(), `"parked_not_shown":3`)
}

func TestWaitingRefusesParkedUnlessIncluded(t *testing.T) {
	t.Parallel()
	store := &fakeStore{waiting: &reports.WaitingPage{}}
	h := serve(t, store)

	assert.Equal(t, http.StatusUnprocessableEntity, get(t, h, "/api/reports/waiting?status=parked").Code)
	assert.Zero(t, store.called)
	assert.Equal(t, http.StatusOK, get(t, h, "/api/reports/waiting?status=parked&include_parked=true").Code)
	assert.Equal(t, "parked", store.status)
}

func TestSlowReportIs503(t *testing.T) {
	t.Parallel()
	rec := get(t, serve(t, &fakeStore{err: reports.ErrTooSlow}), "/api/reports")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestWaitingPassesItsPage(t *testing.T) {
	t.Parallel()
	store := &fakeStore{waiting: &reports.WaitingPage{Total: 1, Items: []reports.Waiting{{TicketID: 7, Title: "x", Priority: "high", Since: now}}}}

	rec := get(t, serve(t, store), "/api/reports/waiting?status=discussion&priority=high&limit=5&offset=15")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "discussion", store.status)
	assert.Equal(t, "high", store.priority)
	assert.Equal(t, 5, store.limit)
	assert.Equal(t, 15, store.offset)
	assert.Contains(t, rec.Body.String(), `"id":7`)
}

func TestServiceRefusesInvalidRequestsWithoutAskingTheStore(t *testing.T) {
	t.Parallel()
	store := &fakeStore{summary: &reports.Summary{}, waiting: &reports.WaitingPage{}}
	svc := reports.NewService(store, func() time.Time { return now })
	ctx := context.Background()

	for _, days := range []int{0, 1, 365} {
		_, err := svc.Summary(ctx, days, "", false)
		assert.ErrorIs(t, err, reports.ErrInvalid, "days %d", days)
	}
	_, err := svc.Summary(ctx, 30, "meh", false)
	assert.ErrorIs(t, err, reports.ErrInvalid)
	for _, c := range []struct {
		status string
		limit  int
		offset int
	}{{"parked", 10, 0}, {"done", 10, 0}, {"open", 0, 0}, {"open", reports.MaxWaiting + 1, 0}, {"open", 10, -1}, {"open", 10, reports.MaxWaitingOffset + 1}} {
		_, err := svc.Waiting(ctx, c.status, "", false, c.limit, c.offset)
		assert.ErrorIs(t, err, reports.ErrInvalid, "%+v", c)
	}
	assert.Zero(t, store.called)
}

func TestUnexpectedStoreErrorIs500AndLeaksNothing(t *testing.T) {
	t.Parallel()
	rec := get(t, serve(t, &fakeStore{err: errors.New("pq: secret detail")}), "/api/reports")

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "secret detail")
}

func TestGetLLMUsageReportsThePeriodAndKeepsBillingModesApart(t *testing.T) {
	t.Parallel()
	cost := int64(2_500_000)
	store := &fakeStore{usage: &reports.UsageReport{
		From: now.AddDate(0, 0, -7), To: now,
		Totals: []reports.UsageSums{
			{BillingMode: "metered", Requests: 3, CostMicros: &cost},
			{BillingMode: "subscription", Requests: 9, UnpricedRecords: 2, UnpricedRequests: 4},
		},
	}}

	rec := get(t, serve(t, store), "/api/reports/llm-usage?days=7")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, now.AddDate(0, 0, -7), store.usageFilter.From)
	assert.Equal(t, now, store.usageFilter.To)
	var body struct {
		Totals []struct {
			BillingMode      string `json:"billing_mode"`
			CostMicros       *int64 `json:"cost_micros"`
			UnpricedRequests int64  `json:"unpriced_requests"`
		}
		Days []any `json:"days"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Totals, 2, "one figure per billing mode, never a sum")
	assert.EqualValues(t, 2_500_000, *body.Totals[0].CostMicros)
	assert.Nil(t, body.Totals[1].CostMicros, "unpriced is absent, not zero")
	assert.EqualValues(t, 4, body.Totals[1].UnpricedRequests)
	assert.NotNil(t, body.Days, "lists are empty arrays, not null")
}

func TestGetLLMUsageTicketsPassesItsPageAndFilter(t *testing.T) {
	t.Parallel()
	cost := int64(42)
	store := &fakeStore{tickets: &reports.UsageTicketPage{Total: 11, Next: &reports.UsageTicketCursor{Tokens: 9, TicketID: 7},
		Items: []reports.UsageTicket{{TicketID: 7, Title: "x", Status: "done",
			Modes:    []reports.UsageSums{{BillingMode: "metered", Requests: 3, InputTokens: 9, CostMicros: &cost}},
			Guidance: []reports.UsageSums{{BillingMode: "subscription", Requests: 1, InputTokens: 77}}}}}}
	h := serve(t, store)

	rec := get(t, h, "/api/reports/llm-usage/tickets?days=90&limit=5&status=done&status=open&priority=high&q=parser"+
		"&created_from=2026-01-02&created_to=2026-01-04")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	q := store.ticketQuery
	assert.Equal(t, 5, q.Limit)
	assert.Nil(t, q.After)
	assert.Equal(t, []ticket.Status{"done", "open"}, q.Filter.Statuses)
	assert.Equal(t, []model.Priority{"high"}, q.Filter.Priorities)
	assert.Equal(t, "parser", q.Filter.Text)
	assert.Equal(t, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), *q.Filter.Created.From)
	assert.Equal(t, time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), *q.Filter.Created.Before, "the last day is included")
	assert.Equal(t, now.AddDate(0, 0, -90), store.usageFilter.From)
	var page struct {
		Total      int64  `json:"total"`
		NextCursor string `json:"next_cursor"`
		Tickets    []struct {
			ID     int64  `json:"id"`
			Title  string `json:"title"`
			Status string `json:"status"`
			Modes  []struct {
				BillingMode string `json:"billing_mode"`
				Requests    int64  `json:"requests"`
				InputTokens int64  `json:"input_tokens"`
				CostMicros  *int64 `json:"cost_micros"`
			} `json:"modes"`
			GuidanceModes []struct {
				BillingMode string `json:"billing_mode"`
				InputTokens int64  `json:"input_tokens"`
			} `json:"guidance_modes"`
		} `json:"tickets"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	assert.EqualValues(t, 11, page.Total)
	require.Len(t, page.Tickets, 1)
	assert.EqualValues(t, 7, page.Tickets[0].ID)
	assert.Equal(t, "x", page.Tickets[0].Title)
	assert.Equal(t, "done", page.Tickets[0].Status)
	require.Len(t, page.Tickets[0].Modes, 1)
	assert.Equal(t, "metered", page.Tickets[0].Modes[0].BillingMode)
	require.Len(t, page.Tickets[0].GuidanceModes, 1)
	assert.Equal(t, "subscription", page.Tickets[0].GuidanceModes[0].BillingMode)
	assert.EqualValues(t, 77, page.Tickets[0].GuidanceModes[0].InputTokens, "guidance is its own figure, not the worker's 9")
	assert.EqualValues(t, 3, page.Tickets[0].Modes[0].Requests)
	assert.EqualValues(t, 9, page.Tickets[0].Modes[0].InputTokens)
	assert.EqualValues(t, 42, *page.Tickets[0].Modes[0].CostMicros)

	// The cursor the response hands out is bound to the period and filter it
	// was for, and resumes where it says.
	require.NotEmpty(t, page.NextCursor)
	same := "/api/reports/llm-usage/tickets?days=90&limit=5&status=done&status=open&priority=high&q=parser" +
		"&created_from=2026-01-02&created_to=2026-01-04&cursor="
	rec = get(t, h, same+page.NextCursor)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.EqualValues(t, 9, store.ticketQuery.After.Tokens)
	assert.EqualValues(t, 7, store.ticketQuery.After.TicketID)
}

func TestGetLLMUsageTicketsRefusesWhatItDoesNotOffer(t *testing.T) {
	t.Parallel()
	store := &fakeStore{tickets: &reports.UsageTicketPage{Next: &reports.UsageTicketCursor{Tokens: 9, TicketID: 7}}}
	h := serve(t, store)
	rec := get(t, h, "/api/reports/llm-usage/tickets?days=30&status=done")
	var page struct {
		NextCursor string `json:"next_cursor"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.NotEmpty(t, page.NextCursor)

	before := store.called
	for _, target := range []string{
		"/api/reports/llm-usage?days=365",
		"/api/reports/llm-usage/tickets?limit=500",
		"/api/reports/llm-usage/tickets?status=nonsense",
		"/api/reports/llm-usage/tickets?priority=nonsense",
		"/api/reports/llm-usage/tickets?created_from=yesterday",
		"/api/reports/llm-usage/tickets?q=" + strings.Repeat("x", 201),
		"/api/reports/llm-usage/tickets?cursor=not-a-cursor",
		// A cursor from another filter or period would skip or repeat tickets.
		"/api/reports/llm-usage/tickets?days=30&status=open&cursor=" + page.NextCursor,
		"/api/reports/llm-usage/tickets?days=7&status=done&cursor=" + page.NextCursor,
	} {
		assert.Equal(t, http.StatusUnprocessableEntity, get(t, h, target).Code, target)
	}
	assert.Equal(t, before, store.called, "an invalid request never reaches the store")
}

func TestSlowLLMUsageReportIs503(t *testing.T) {
	t.Parallel()
	rec := get(t, serve(t, &fakeStore{err: reports.ErrTooSlow}), "/api/reports/llm-usage")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestGetLLMUsageMapsEverySection(t *testing.T) {
	t.Parallel()
	cost := int64(5)
	day := now.AddDate(0, 0, -1)
	sums := func(mode string, requests int64) reports.UsageSums {
		return reports.UsageSums{BillingMode: mode, Records: 1, Transcripts: 1, Requests: requests, InputTokens: 10, OutputTokens: 20,
			CacheReadTokens: 30, CacheWriteTokens: 40, ReasoningTokens: 5, CostMicros: &cost, UnpricedRecords: 2, UnpricedRequests: 3}
	}
	store := &fakeStore{usage: &reports.UsageReport{
		From: now.AddDate(0, 0, -30), To: now,
		Totals:       []reports.UsageSums{sums("subscription", 7)},
		Unattributed: []reports.UsageSums{sums("subscription", 2)},
		Guidance:     []reports.UsageSums{sums("subscription", 3)},
		Days:         []reports.UsageDay{{Day: day, UsageSums: sums("subscription", 7)}},
		Agents:       []reports.UsageAgent{{AgentID: "a-1", Name: "alice", UsageSums: sums("subscription", 7)}},
		Models:       []reports.UsageModel{{Provider: "anthropic", Model: "opus", UsageSums: sums("subscription", 7)}},
		Tools:        []reports.UsageTool{{Category: "bash", Calls: 8, Errors: 1, DurationMs: 900}},
		DoneTickets:  []reports.UsageDoneTickets{{BillingMode: "subscription", Tickets: 4, CostMicros: &cost, CostP50Micros: &cost, UnpricedTickets: 1}},
		Tickets:      6,
	}}

	rec := get(t, serve(t, store), "/api/reports/llm-usage")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.EqualValues(t, 6, got["tickets"])
	assert.Equal(t, now.AddDate(0, 0, -30).Format(time.RFC3339), got["from"])
	totals := got["totals"].([]any)[0].(map[string]any)
	assert.Equal(t, map[string]any{
		"billing_mode": "subscription", "records": 1.0, "transcripts": 1.0, "requests": 7.0, "input_tokens": 10.0, "output_tokens": 20.0,
		"cache_read_tokens": 30.0, "cache_write_tokens": 40.0, "reasoning_tokens": 5.0, "cost_micros": 5.0,
		"unpriced_records": 2.0, "unpriced_requests": 3.0,
	}, totals)
	assert.EqualValues(t, 2, got["unattributed"].([]any)[0].(map[string]any)["requests"])
	assert.EqualValues(t, 3, got["guidance"].([]any)[0].(map[string]any)["requests"])
	d := got["days"].([]any)[0].(map[string]any)
	assert.Equal(t, day.Format(time.RFC3339), d["day"])
	assert.EqualValues(t, 7, d["requests"])
	a := got["agents"].([]any)[0].(map[string]any)
	assert.Equal(t, "alice", a["name"])
	assert.Equal(t, "a-1", a["agent_id"])
	m := got["models"].([]any)[0].(map[string]any)
	assert.Equal(t, "opus", m["model"])
	assert.Equal(t, "anthropic", m["provider"])
	assert.Equal(t, map[string]any{"category": "bash", "calls": 8.0, "errors": 1.0, "duration_ms": 900.0}, got["tools"].([]any)[0])
	assert.Equal(t, map[string]any{"billing_mode": "subscription", "tickets": 4.0, "cost_micros": 5.0, "cost_p50_micros": 5.0, "unpriced_tickets": 1.0},
		got["done_tickets"].([]any)[0])
}

func TestGetScopeCostAsksForThePeriodAndRefusesOthers(t *testing.T) {
	t.Parallel()
	store := &fakeStore{scope: &reports.ScopeCostInput{From: now.AddDate(0, 0, -7), To: now}}
	h := serve(t, store)

	rec := get(t, h, "/api/reports/scope-cost?days=7")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, now, store.scopeTo)
	assert.Equal(t, now.AddDate(0, 0, -7), store.scopeFrom)
	assert.Contains(t, rec.Body.String(), `"median_seconds":null`, "no waits is absent, not zero")

	called := store.called
	assert.Equal(t, http.StatusUnprocessableEntity, get(t, h, "/api/reports/scope-cost?days=365").Code)
	assert.Equal(t, http.StatusUnprocessableEntity, get(t, h, "/api/reports/scope-cost?days=90").Code, "only 30 days are kept")
	assert.Equal(t, called, store.called)
}

func TestGetScopeCostMapsEverySection(t *testing.T) {
	t.Parallel()
	denied := now.Add(-2 * hour)
	resolved := denied.Add(time.Minute)
	started := now.Add(-3 * hour)
	store := &fakeStore{scope: &reports.ScopeCostInput{
		From: now.AddDate(0, 0, -7), To: now,
		Blocks: []reports.ScopeBlock{{Span: reports.Span{Start: started}, TicketID: 5, TicketTitle: "Wants", BlockerID: 4,
			BlockerTitle: "Holder", Path: "a/"}},
		Denials:    []reports.ScopeDenial{{TicketID: 5, Path: "docs/", DeniedAt: denied, ResolvedAt: &resolved, Resolution: reports.DenialGranted}},
		Workers:    []reports.Span{{Start: now.AddDate(0, 0, -30)}},
		Narrowings: 2,
	}}

	rec := get(t, serve(t, store), "/api/reports/scope-cost?days=7")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.EqualValues(t, 1, body["intervals"])
	assert.EqualValues(t, (3 * hour).Seconds(), body["blocked_seconds"])
	assert.EqualValues(t, (3 * hour).Seconds(), body["idle_blocked_seconds"])
	assert.EqualValues(t, 2, body["narrowings"])
	assert.Equal(t, []any{map[string]any{"id": 5.0, "title": "Wants", "seconds": (3 * hour).Seconds(), "ongoing": true,
		"claimed": false, "blocker_ids": []any{4.0}}}, body["tickets"])
	assert.Equal(t, []any{map[string]any{"id": 4.0, "title": "Holder", "seconds": (3 * hour).Seconds(), "tickets": 1.0}}, body["blockers"])
	assert.Equal(t, []any{map[string]any{"path": "a/", "seconds": (3 * hour).Seconds(), "tickets": 1.0}}, body["paths"])
	assert.Equal(t, map[string]any{"total": 1.0, "granted": 1.0, "narrowed": 0.0, "gave_up": 0.0, "open": 0.0,
		"median_seconds_to_resolve": 60.0, "paths": []any{map[string]any{"path": "docs/", "count": 1.0}}}, body["denials"])
	assert.Equal(t, map[string]any{"tickets": 1.0, "claimed": 0.0, "ongoing": 1.0, "total_seconds": (3 * hour).Seconds(),
		"median_seconds": (3 * hour).Seconds()}, body["waits"])
}

func TestGetLockHistoryAsksForThePeriodAndRefusesOthers(t *testing.T) {
	t.Parallel()
	store := &fakeStore{lockHistory: &reports.LockHistoryInput{}}
	h := serve(t, store)

	rec := get(t, h, "/api/reports/lock-history?days=7")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, now, store.scopeTo)
	assert.Equal(t, now.AddDate(0, 0, -7), store.scopeFrom)
	assert.Contains(t, rec.Body.String(), `"hold":null`, "no holds is absent, not zero")

	called := store.called
	assert.Equal(t, http.StatusUnprocessableEntity, get(t, h, "/api/reports/lock-history?days=365").Code)
	assert.Equal(t, http.StatusUnprocessableEntity, get(t, h, "/api/reports/lock-history?days=90").Code, "only 30 days are kept")
	assert.Equal(t, called, store.called)
}

func TestGetLockHistoryMapsEveryField(t *testing.T) {
	t.Parallel()
	acquired := now.Add(-2 * hour)
	queued := acquired.Add(-10 * time.Minute)
	released := acquired.Add(time.Hour)
	store := &fakeStore{lockHistory: &reports.LockHistoryInput{From: now.AddDate(0, 0, -7), To: now, Holds: []reports.LockHold{
		{Name: "integration", TicketID: 3, QueuedAt: &queued, AcquiredAt: acquired, ReleasedAt: &released},
	}}}

	rec := get(t, serve(t, store), "/api/reports/lock-history?days=7")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.EqualValues(t, hour.Seconds(), body["held_seconds"])
	assert.Equal(t, map[string]any{"day": nil, "holds": 1.0, "ongoing": 0.0, "queued": 1.0,
		"hold": map[string]any{"p50": hour.Seconds(), "p95": hour.Seconds()},
		"wait": map[string]any{"p50": 600.0, "p95": 600.0}}, body["total"])
	days := body["days"].([]any)
	require.Len(t, days, 1)
	assert.NotNil(t, days[0].(map[string]any)["day"])
	assert.Len(t, body["overlap"], 3)
}

func TestGetShadowDecisionsAsksForThePeriodAndRefusesOthers(t *testing.T) {
	t.Parallel()
	store := &fakeStore{shadow: &reports.ShadowDecisions{}}
	h := serve(t, store)

	rec := get(t, h, "/api/reports/shadow-decisions?days=7")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, now, store.scopeTo)
	assert.Equal(t, now.AddDate(0, 0, -7), store.scopeFrom)
	assert.Contains(t, rec.Body.String(), `"failures":[]`, "none is an empty list")

	called := store.called
	assert.Equal(t, http.StatusUnprocessableEntity, get(t, h, "/api/reports/shadow-decisions?days=365").Code)
	assert.Equal(t, http.StatusUnprocessableEntity, get(t, h, "/api/reports/shadow-decisions?days=90").Code, "7 and 30 only, as the sibling reports")
	assert.Equal(t, called, store.called)
}

func TestGetShadowDecisionsDefaultsToThirtyDays(t *testing.T) {
	t.Parallel()
	store := &fakeStore{shadow: &reports.ShadowDecisions{}}

	require.Equal(t, http.StatusOK, get(t, serve(t, store), "/api/reports/shadow-decisions").Code)

	assert.Equal(t, now.AddDate(0, 0, -30), store.scopeFrom)
}

func TestGetShadowDecisionsMapsEveryField(t *testing.T) {
	t.Parallel()
	decided := now.Add(-2 * hour)
	store := &fakeStore{shadow: &reports.ShadowDecisions{
		WouldAccept: 3, WouldRefuse: 2, AcceptCIPassed: 1, AcceptCIFailed: 1, AcceptAwaitingCI: 1,
		Reasons:  []reports.ShadowReason{{Decision: "would_refuse", Reason: "go.work is not in an independent app", Count: 2}},
		Failures: []reports.ShadowFailure{{TicketID: 9, Title: "T", ChangeID: 4, HeadSHA: "h1", CIHeadSHA: "h2", DriftDirs: []string{"billing/"}, DecidedAt: decided, CIRecordedAt: decided.Add(hour)}},
	}}

	rec := get(t, serve(t, store), "/api/reports/shadow-decisions?days=30")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.EqualValues(t, 3, body["would_accept"])
	assert.EqualValues(t, 2, body["would_refuse"])
	assert.EqualValues(t, 1, body["accept_ci_passed"])
	assert.EqualValues(t, 1, body["accept_ci_failed"])
	assert.EqualValues(t, 1, body["accept_awaiting_ci"])
	assert.Equal(t, []any{map[string]any{"decision": "would_refuse", "reason": "go.work is not in an independent app", "count": 2.0}}, body["reasons"])
	f := body["failures"].([]any)[0].(map[string]any)
	assert.Equal(t, "h2", f["ci_head_sha"])
	assert.Equal(t, []any{"billing/"}, f["drift_dirs"])
	assert.EqualValues(t, 9, f["ticket_id"])
}

func TestGetLLMUsagePassesIncludeUnreliableAndNamesWhatItLeftOut(t *testing.T) {
	t.Parallel()
	store := &fakeStore{usage: &reports.UsageReport{From: now.AddDate(0, 0, -7), To: now, Unreliable: 4}}
	h := serve(t, store)

	rec := get(t, h, "/api/reports/llm-usage?days=7")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.False(t, store.usageFilter.IncludeUnreliable, "left out unless asked")
	var body struct {
		UnreliableRecords int64     `json:"unreliable_records"`
		UnreliableBefore  time.Time `json:"unreliable_before"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.EqualValues(t, 4, body.UnreliableRecords)
	assert.True(t, body.UnreliableBefore.Equal(reports.OvercountCutoff))

	require.Equal(t, http.StatusOK, get(t, h, "/api/reports/llm-usage?days=7&include_unreliable=true").Code)
	assert.True(t, store.usageFilter.IncludeUnreliable)
}

func TestGetLLMUsageTicketsPassesIncludeUnreliableAndBindsItsCursor(t *testing.T) {
	t.Parallel()
	store := &fakeStore{tickets: &reports.UsageTicketPage{Next: &reports.UsageTicketCursor{Tokens: 9, TicketID: 7}}}
	h := serve(t, store)

	rec := get(t, h, "/api/reports/llm-usage/tickets?days=30&include_unreliable=true")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, store.ticketQuery.IncludeUnreliable)
	assert.True(t, store.usageFilter.IncludeUnreliable)
	var page struct {
		NextCursor string `json:"next_cursor"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))

	// A cursor over the included list is not a position in the other one.
	rec = get(t, h, "/api/reports/llm-usage/tickets?days=30&cursor="+page.NextCursor)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	rec = get(t, h, "/api/reports/llm-usage/tickets?days=30&include_unreliable=true&cursor="+page.NextCursor)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}
