package reports_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	usagelib "github.com/lowaa/slop-factory/go-llm-usage"
	"github.com/lowaa/slop-factory/go-llm-usage/pricing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/llmusage"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reports"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

type toolSpec struct {
	category, tool       string
	calls, errors, durMs int64
}

// usageSpec is one llm_usage row. Cost is the reported figure when set, else
// the estimate; neither is an unpriced row.
type usageSpec struct {
	agent    uuid.UUID
	key      string
	model    string // defaults to "m1"
	mode     string // defaults to subscription
	requests int64
	in, out  int64
	cacheR   int64
	reasoned int64
	reported *int64
	est      *int64
	ticket   *int64
	scope    string // defaults to ticket when ticket is set, else overhead
	ended    time.Time
	tools    []toolSpec
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func costOf(reported, est *int64) *int64 {
	if reported != nil {
		return reported
	}
	return est
}

func micros(v int64) *int64 { return &v }

func (f *fixture) agentRow(t *testing.T, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.database.Exec(f.ctx, `INSERT INTO agents (id, name) VALUES ($1, $2)`, id, name)
	require.NoError(t, err)
	return id
}

func (f *fixture) plainTicket(t *testing.T, status string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, f.database.QueryRow(f.ctx, `INSERT INTO tickets (title, status) VALUES ('t', $1) RETURNING id`, status).Scan(&id))
	return id
}

func (f *fixture) usage(t *testing.T, ctx context.Context, s usageSpec) {
	t.Helper()
	if s.model == "" {
		s.model = "m1"
	}
	if s.mode == "" {
		s.mode = reports.BillingSubscription
	}
	if s.scope == "" {
		s.scope = "overhead"
		if s.ticket != nil {
			s.scope = "ticket"
		}
	}
	_, err := f.database.Exec(ctx, `
INSERT INTO llm_usage (agent_id, transcript_key, harness, session_id, scope_hint, scope, ticket_id, provider, model,
    billing_mode, requests, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens,
    reported_cost_microdollars, estimated_cost_microdollars, price_version, unpriced, started_at, ended_at)
VALUES ($1, $2, 'pi', 's', 'main', $3, $4, 'p', $5, $6, $7, $8, $9, $10, 0, $11, $12, $13, 'v', $14, $15, $15)`,
		s.agent, s.key, s.scope, s.ticket, s.model, s.mode, s.requests, s.in, s.out, s.cacheR, s.reasoned,
		s.reported, s.est, s.reported == nil && s.est == nil, s.ended)
	require.NoError(t, err)
	// The first report of a transcript: its whole total is the day's delta.
	_, err = f.database.Exec(ctx, `
INSERT INTO llm_usage_daily (agent_id, day, provider, model, billing_mode, records, unpriced_records, requests, unpriced_requests,
    input_tokens, output_tokens, cache_read_tokens, reasoning_tokens, cost_microdollars)
VALUES ($1, date_trunc('day', $2::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', 'p', $3, $4, 1, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (tenant_id, day, agent_id, provider, model, billing_mode, unreliable) DO UPDATE SET
    records = llm_usage_daily.records + 1, unpriced_records = llm_usage_daily.unpriced_records + EXCLUDED.unpriced_records,
    requests = llm_usage_daily.requests + EXCLUDED.requests, unpriced_requests = llm_usage_daily.unpriced_requests + EXCLUDED.unpriced_requests,
    input_tokens = llm_usage_daily.input_tokens + EXCLUDED.input_tokens, output_tokens = llm_usage_daily.output_tokens + EXCLUDED.output_tokens,
    cache_read_tokens = llm_usage_daily.cache_read_tokens + EXCLUDED.cache_read_tokens,
    reasoning_tokens = llm_usage_daily.reasoning_tokens + EXCLUDED.reasoning_tokens,
    cost_microdollars = COALESCE(llm_usage_daily.cost_microdollars, 0) + COALESCE(EXCLUDED.cost_microdollars, 0)`,
		s.agent, s.ended, s.model, s.mode, boolInt(s.reported == nil && s.est == nil),
		s.requests, boolInt(s.reported == nil && s.est == nil)*s.requests, s.in, s.out, s.cacheR, s.reasoned, costOf(s.reported, s.est))
	require.NoError(t, err)
	for _, tl := range s.tools {
		_, err := f.database.Exec(ctx, `
INSERT INTO llm_tool_usage (agent_id, transcript_key, provider, model, harness, billing_mode, category, tool, calls, errors, duration_ms, result_bytes, ended_at)
VALUES ($1, $2, 'p', $3, 'pi', $4, $5, $6, $7, $8, $9, 0, $10)`,
			s.agent, s.key, s.model, s.mode, tl.category, tl.tool, tl.calls, tl.errors, tl.durMs, s.ended)
		require.NoError(t, err)
	}
}

func usageFilter(days int) *reports.UsageFilter {
	return &reports.UsageFilter{From: now.AddDate(0, 0, -days), To: now}
}

func sumsFor(t *testing.T, sums []reports.UsageSums, mode string) reports.UsageSums {
	t.Helper()
	for _, s := range sums {
		if s.BillingMode == mode {
			return s
		}
	}
	require.Failf(t, "no figure", "billing mode %s", mode)
	return reports.UsageSums{}
}

func TestLLMUsageFigures(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	alice, bob := f.agentRow(t, "alice"), f.agentRow(t, "bob")
	done, open := f.plainTicket(t, "done"), f.plainTicket(t, "open")
	doneMetered, doneUnpriced := f.plainTicket(t, "done"), f.plainTicket(t, "done")
	at := func(h time.Duration) time.Time { return now.Add(-h * hour) }

	// Alice: a subscription transcript on the done ticket, in two models.
	f.usage(t, f.ctx, usageSpec{agent: alice, key: "a1", model: "opus", requests: 10, in: 100, out: 50, cacheR: 1000, reasoned: 5,
		est: micros(3_000_000), ticket: &done, ended: at(30), tools: []toolSpec{
			{"bash", "Bash", 8, 1, 4000}, {"file_read", "Read", 20, 0, 1000}}})
	f.usage(t, f.ctx, usageSpec{agent: alice, key: "a1", model: "haiku", requests: 4, in: 40, out: 20,
		est: micros(500_000), ticket: &done, ended: at(30), tools: []toolSpec{{"bash", "Bash", 2, 1, 1000}}})
	// Bob: metered, reported cost, on the open ticket and not on one.
	f.usage(t, f.ctx, usageSpec{agent: bob, key: "b1", model: "gpt", mode: reports.BillingMetered, requests: 6, in: 60, out: 30,
		reported: micros(2_000_000), est: micros(1_900_000), ticket: &open, ended: at(50)})
	f.usage(t, f.ctx, usageSpec{agent: bob, key: "b2", model: "gpt", mode: reports.BillingMetered, requests: 2, in: 20, out: 10,
		est: micros(700_000), ended: at(60), tools: []toolSpec{{"search", "Grep", 5, 0, 500}}})
	// A second done ticket, metered only, and a third with a priced and an
	// unpriced row.
	f.usage(t, f.ctx, usageSpec{agent: bob, key: "b4", model: "gpt", mode: reports.BillingMetered, requests: 1,
		reported: micros(300_000), ticket: &doneMetered, ended: at(80)})
	f.usage(t, f.ctx, usageSpec{agent: bob, key: "b5", model: "gpt", requests: 1, est: micros(100), ticket: &doneUnpriced, ended: at(81)})
	f.usage(t, f.ctx, usageSpec{agent: bob, key: "b5", model: "mystery", requests: 1, ticket: &doneUnpriced, ended: at(81)})
	// Unpriced: a model the price table does not know.
	f.usage(t, f.ctx, usageSpec{agent: bob, key: "b3", model: "mystery", requests: 7, in: 70, out: 70, ended: at(70)})

	got, err := reports.NewPostgresStore(f.database, 0).LLMUsage(f.ctx, usageFilter(7))
	require.NoError(t, err)

	sub := sumsFor(t, got.Totals, reports.BillingSubscription)
	assert.EqualValues(t, 3, sub.Transcripts, "a1 (two models), b3 and b5 (two models)")
	assert.EqualValues(t, 5, sub.Records)
	assert.EqualValues(t, 23, sub.Requests)
	assert.EqualValues(t, 210, sub.InputTokens)
	assert.EqualValues(t, 140, sub.OutputTokens)
	assert.EqualValues(t, 1000, sub.CacheReadTokens)
	assert.EqualValues(t, 5, sub.ReasoningTokens)
	require.NotNil(t, sub.CostMicros)
	assert.EqualValues(t, 3_500_100, *sub.CostMicros, "the unpriced rows add nothing")
	assert.EqualValues(t, 2, sub.UnpricedRecords)
	assert.EqualValues(t, 8, sub.UnpricedRequests)

	// Never summed with subscription: metered is its own figure, preferring
	// what the harness reported over the estimate.
	met := sumsFor(t, got.Totals, reports.BillingMetered)
	require.NotNil(t, met.CostMicros)
	assert.EqualValues(t, 3_000_000, *met.CostMicros, "reported 2.0 + estimated 0.7 + reported 0.3")
	assert.EqualValues(t, 0, met.UnpricedRecords)
	assert.Len(t, got.Totals, 2)

	// Not tied to a ticket: b2 (metered) and b3 (subscription).
	assert.EqualValues(t, 2, sumsFor(t, got.Unattributed, reports.BillingMetered).Requests)
	assert.EqualValues(t, 7, sumsFor(t, got.Unattributed, reports.BillingSubscription).Requests)

	assert.EqualValues(t, 4, got.Tickets)
	require.Len(t, got.DoneTickets, 2, "done tickets are per billing mode too")
	m := got.DoneTickets[0]
	assert.Equal(t, reports.BillingMetered, m.BillingMode)
	assert.EqualValues(t, 1, m.Tickets)
	assert.EqualValues(t, 300_000, *m.CostMicros)
	assert.Zero(t, m.UnpricedTickets)
	d := got.DoneTickets[1]
	assert.Equal(t, reports.BillingSubscription, d.BillingMode)
	assert.EqualValues(t, 2, d.Tickets, "the open ticket is not done; the one with an unpriced row is")
	assert.EqualValues(t, 3_500_100, *d.CostMicros)
	assert.EqualValues(t, 100, *d.CostP50Micros)
	assert.EqualValues(t, 1, d.UnpricedTickets, "its cost is understated, and says so")

	require.Len(t, got.Agents, 3, "alice, and bob in each billing mode")
	byAgent := map[string]reports.UsageAgent{}
	for _, a := range got.Agents {
		byAgent[a.Name+"/"+a.BillingMode] = a
	}
	assert.EqualValues(t, 14, byAgent["alice/subscription"].Requests)
	assert.EqualValues(t, 9, byAgent["bob/metered"].Requests)
	assert.EqualValues(t, 9, byAgent["bob/subscription"].Requests)

	models := map[string]reports.UsageModel{}
	for _, m := range got.Models {
		models[m.Model+"/"+m.BillingMode] = m
	}
	assert.EqualValues(t, 10, models["opus/subscription"].Requests)
	assert.EqualValues(t, 9, models["gpt/metered"].Requests)

	tools := map[string]reports.UsageTool{}
	for _, tl := range got.Tools {
		tools[tl.Category] = tl
	}
	assert.Equal(t, reports.UsageTool{Category: "bash", Calls: 10, Errors: 2, DurationMs: 5000}, tools["bash"])
	assert.Equal(t, reports.UsageTool{Category: "file_read", Calls: 20, DurationMs: 1000}, tools["file_read"])
	assert.EqualValues(t, 5, tools["search"].Calls)

	// The day's trend adds up to the totals, per mode.
	var perDay int64
	for _, day := range got.Days {
		if day.BillingMode == reports.BillingSubscription {
			perDay += day.Requests
		}
	}
	assert.EqualValues(t, sub.Requests, perDay)
}

func TestLLMUsageNothingIsAbsentNotZero(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	got, err := reports.NewPostgresStore(f.database, 0).LLMUsage(f.ctx, usageFilter(30))
	require.NoError(t, err)
	assert.Empty(t, got.Totals)
	assert.Empty(t, got.Days)
	assert.Empty(t, got.DoneTickets)
	assert.Zero(t, got.Tickets)
}

func TestLLMUsageAllUnpricedHasNoCostNotZero(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agentRow(t, "a")
	f.usage(t, f.ctx, usageSpec{agent: a, key: "k", requests: 3, in: 1, out: 1, ended: now.Add(-hour)})

	got, err := reports.NewPostgresStore(f.database, 0).LLMUsage(f.ctx, usageFilter(7))
	require.NoError(t, err)
	sub := sumsFor(t, got.Totals, reports.BillingSubscription)
	assert.Nil(t, sub.CostMicros)
	assert.EqualValues(t, 3, sub.UnpricedRequests)
}

// A period is [from, to) on ended_at, so a transcript is in exactly one of
// two adjacent periods: the boundary instant belongs to the later one.
func TestLLMUsagePeriodBoundary(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agentRow(t, "a")
	store := reports.NewPostgresStore(f.database, 0)
	edge := now.AddDate(0, 0, -7)
	f.usage(t, f.ctx, usageSpec{agent: a, key: "before", requests: 1, est: micros(1), ended: edge.Add(-time.Nanosecond),
		tools: []toolSpec{{"bash", "Bash", 1, 0, 1}}})
	f.usage(t, f.ctx, usageSpec{agent: a, key: "on-from", requests: 10, est: micros(1), ended: edge,
		tools: []toolSpec{{"bash", "Bash", 10, 0, 1}}})
	f.usage(t, f.ctx, usageSpec{agent: a, key: "on-to", requests: 100, est: micros(1), ended: now,
		tools: []toolSpec{{"bash", "Bash", 100, 0, 1}}})

	requests := func(from, to time.Time) int64 {
		t.Helper()
		got, err := store.LLMUsage(f.ctx, &reports.UsageFilter{From: from, To: to})
		require.NoError(t, err)
		if len(got.Totals) == 0 {
			return 0
		}
		// The tools are found through the same rows, so they follow the
		// same rule.
		require.Len(t, got.Tools, 1)
		assert.Equal(t, got.Totals[0].Requests, got.Tools[0].Calls)
		return got.Totals[0].Requests
	}
	earlier := requests(edge.AddDate(0, 0, -7), edge)
	later := requests(edge, now)
	assert.EqualValues(t, 1, earlier, "the row ending a hair before the edge, not the one on it")
	assert.EqualValues(t, 10, later, "the row on the edge, not the one on the end")
	assert.EqualValues(t, 100, requests(now, now.AddDate(0, 0, 7)))
	assert.EqualValues(t, 111, requests(edge.AddDate(0, 0, -7), now.AddDate(0, 0, 7)), "adjacent periods add up to the whole: nothing twice, nothing lost")
}

// A transcript's totals grow while it runs and each report replaces the last,
// so however many times it is reported it counts once, and in the period of
// its latest activity.
func TestLLMUsageReReportedTranscriptCountsOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agentRow(t, "a")
	svc := llmusage.NewPostgresStore(f.database, pricing.Default())
	store := reports.NewPostgresStore(f.database, 0)
	tr := &llmusage.Transcript{AgentID: a, Key: "t1", Harness: usagelib.HarnessPi, SessionID: "s", ScopeHint: "main"}
	report := func(requests int64, ended time.Time) {
		t.Helper()
		r := usagelib.Record{
			SchemaVersion: usagelib.SchemaVersion, Provider: "p", Model: "m", Harness: usagelib.HarnessPi,
			BillingMode: usagelib.BillingSubscription, Requests: requests, InputTokens: requests * 10, OutputTokens: requests,
			EstimatedCostMicros: micros(requests * 1000), PriceVersion: "v", StartedAt: now.AddDate(0, 0, -9), EndedAt: ended,
			Tools: []usagelib.ToolUsage{{Category: "bash", Tool: "Bash", Calls: requests}},
		}
		_, _, err := svc.Upsert(f.ctx, tr, []usagelib.Record{r})
		require.NoError(t, err)
	}
	lastWeek := now.AddDate(0, 0, -8)
	thisWeek := now.AddDate(0, 0, -2)

	report(5, lastWeek)
	report(8, lastWeek.Add(hour))
	report(8, lastWeek.Add(hour)) // a hook firing twice
	report(13, thisWeek)          // and then it ran on

	prev, err := store.LLMUsage(f.ctx, &reports.UsageFilter{From: now.AddDate(0, 0, -14), To: now.AddDate(0, 0, -7)})
	require.NoError(t, err)
	assert.Empty(t, prev.Totals, "the transcript moved to the later period with its latest report")
	assert.Empty(t, prev.Tools, "and so did its tool use")

	cur, err := store.LLMUsage(f.ctx, usageFilter(7))
	require.NoError(t, err)
	sub := sumsFor(t, cur.Totals, reports.BillingSubscription)
	assert.EqualValues(t, 13, sub.Requests, "its latest totals, not the sum of its reports")
	assert.EqualValues(t, 1, sub.Records)
	assert.EqualValues(t, 13_000, *sub.CostMicros)
	require.Len(t, cur.Tools, 1)
	assert.EqualValues(t, 13, cur.Tools[0].Calls)
}

func usageQuery(limit int, filter *ticket.ListFilter) *reports.UsageTicketQuery {
	if filter == nil {
		filter = &ticket.ListFilter{}
	}
	return &reports.UsageTicketQuery{Filter: filter, Limit: limit}
}

func ids(page *reports.UsageTicketPage) []int64 {
	out := make([]int64, len(page.Items))
	for i, item := range page.Items {
		out[i] = item.TicketID
	}
	return out
}

func TestLLMUsageTicketsPageByTokensAndKeepModesApart(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agentRow(t, "a")
	big, small, mixed := f.plainTicket(t, "done"), f.plainTicket(t, "done"), f.plainTicket(t, "open")
	f.usage(t, f.ctx, usageSpec{agent: a, key: "k1", in: 1000, est: micros(10), ticket: &big, ended: now.Add(-hour)})
	f.usage(t, f.ctx, usageSpec{agent: a, key: "k2", in: 10, est: micros(1), ticket: &small, ended: now.Add(-hour)})
	f.usage(t, f.ctx, usageSpec{agent: a, key: "k3", in: 100, est: micros(5), ticket: &mixed, ended: now.Add(-hour)})
	f.usage(t, f.ctx, usageSpec{agent: a, key: "k4", in: 100, mode: reports.BillingMetered, reported: micros(7), ticket: &mixed, ended: now.Add(-hour)})
	f.usage(t, f.ctx, usageSpec{agent: a, key: "k5", in: 5000, ended: now.Add(-hour)}) // not a ticket's
	store := reports.NewPostgresStore(f.database, 0)

	page, err := store.LLMUsageTickets(f.ctx, usageFilter(7), usageQuery(2, nil))
	require.NoError(t, err)
	assert.EqualValues(t, 3, page.Total, "usage without a ticket is not a ticket")
	require.Len(t, page.Items, 2)
	assert.Equal(t, big, page.Items[0].TicketID)
	assert.Equal(t, mixed, page.Items[1].TicketID)
	require.Len(t, page.Items[1].Modes, 2)
	assert.Equal(t, reports.BillingMetered, page.Items[1].Modes[0].BillingMode)
	assert.EqualValues(t, 7, *page.Items[1].Modes[0].CostMicros)
	assert.EqualValues(t, 5, *page.Items[1].Modes[1].CostMicros, "not summed with the metered 7")
	require.NotNil(t, page.Next)
	assert.Equal(t, mixed, page.Next.TicketID)
	assert.EqualValues(t, 200, page.Next.Tokens, "the ticket's tokens across modes, which is what ranks it")

	q := usageQuery(2, nil)
	q.After = page.Next
	next, err := store.LLMUsageTickets(f.ctx, usageFilter(7), q)
	require.NoError(t, err)
	require.Len(t, next.Items, 1)
	assert.Equal(t, small, next.Items[0].TicketID)
	assert.Nil(t, next.Next, "the last page has no cursor")
	assert.EqualValues(t, 3, next.Total, "the total is of the list, not of the page")
}

// Tickets with equal tokens are ordered by id, so a cursor between them names
// one position and a walk neither repeats nor skips any.
func TestLLMUsageTicketsKeysetWalkKeepsTiesApart(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agentRow(t, "a")
	store := reports.NewPostgresStore(f.database, 0)
	var want []int64
	for i := 0; i < 7; i++ {
		id := f.plainTicket(t, "open")
		tokens := int64(100)
		if i < 2 {
			tokens = 500
		}
		f.usage(t, f.ctx, usageSpec{agent: a, key: fmt.Sprint("k", i), in: tokens, est: micros(1), ticket: &id, ended: now.Add(-hour)})
		want = append(want, id)
	}
	// Most tokens first, then the lower id.
	want = append(want[:2:2], want[2:]...)

	var got []int64
	q := usageQuery(3, nil)
	for pages := 0; ; pages++ {
		require.Less(t, pages, 10, "the walk ends")
		page, err := store.LLMUsageTickets(f.ctx, usageFilter(7), q)
		require.NoError(t, err)
		got = append(got, ids(page)...)
		if page.Next == nil {
			break
		}
		q.After = page.Next
	}
	assert.Equal(t, want, got)
}

func TestLLMUsageTicketsAreFilteredLikeEveryTicketList(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agentRow(t, "a")
	store := reports.NewPostgresStore(f.database, 0)
	day := func(d int) time.Time { return time.Date(2026, 1, d, 12, 0, 0, 0, time.UTC) }
	n := int64(0)
	mk := func(title, status, priority string, created time.Time, comment string) int64 {
		var id int64
		require.NoError(t, f.database.QueryRow(f.ctx,
			`INSERT INTO tickets (title, status, priority, created_at) VALUES ($1, $2, $3, $4) RETURNING id`,
			title, status, priority, created).Scan(&id))
		if comment != "" {
			_, err := f.database.Exec(f.ctx, `INSERT INTO ticket_comments (ticket_id, author_agent_id, body) VALUES ($1, $2, $3)`, id, a, comment)
			require.NoError(t, err)
		}
		n++
		f.usage(t, f.ctx, usageSpec{agent: a, key: fmt.Sprint("k", id), in: 1000 - n, est: micros(1), ticket: &id, ended: now.Add(-hour)})
		return id
	}
	alpha := mk("fix alpha parser", "done", "high", day(1), "")
	beta := mk("beta rollout", "open", "normal", day(5), "mentions the alpha parser")
	gamma := mk("gamma cleanup", "done", "low", day(9), "")

	list := func(filter *ticket.ListFilter) *reports.UsageTicketPage {
		page, err := store.LLMUsageTickets(f.ctx, usageFilter(7), usageQuery(10, filter))
		require.NoError(t, err)
		return page
	}
	everyone := list(nil)
	assert.Equal(t, []int64{alpha, beta, gamma}, ids(everyone))

	byStatus := list(&ticket.ListFilter{Statuses: []ticket.Status{"done"}})
	assert.Equal(t, []int64{alpha, gamma}, ids(byStatus))
	assert.EqualValues(t, 2, byStatus.Total, "the total follows the filter")

	assert.Equal(t, []int64{gamma}, ids(list(&ticket.ListFilter{Priorities: []model.Priority{"low"}})))
	assert.Equal(t, []int64{alpha, beta}, ids(list(&ticket.ListFilter{Text: "alpha"})), "title or comment text")
	assert.Equal(t, []int64{beta}, ids(list(&ticket.ListFilter{Text: "alpha parser", Statuses: []ticket.Status{"open"}})), "fields narrow together")

	from, before := day(4).Truncate(24*time.Hour), day(8).Truncate(24*time.Hour)
	assert.Equal(t, []int64{beta}, ids(list(&ticket.ListFilter{Created: ticket.TimeRange{From: &from, Before: &before}})))

	assert.Empty(t, list(&ticket.ListFilter{Statuses: []ticket.Status{"parked"}}).Items)
}

func TestLLMUsageOfADeletedTicketIsUnattributed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agentRow(t, "a")
	gone := f.plainTicket(t, "open")
	f.usage(t, f.ctx, usageSpec{agent: a, key: "k", requests: 4, est: micros(1), ticket: &gone, ended: now.Add(-hour)})
	_, err := f.database.Exec(f.ctx, `DELETE FROM tickets WHERE id = $1`, gone)
	require.NoError(t, err)

	got, err := reports.NewPostgresStore(f.database, 0).LLMUsage(f.ctx, usageFilter(7))
	require.NoError(t, err)
	assert.EqualValues(t, 4, sumsFor(t, got.Unattributed, reports.BillingSubscription).Requests)
	assert.Zero(t, got.Tickets)
}

func TestLLMUsageAgentWithoutARowKeepsItsUsage(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.usage(t, f.ctx, usageSpec{agent: uuid.New(), key: "k", requests: 4, est: micros(1), ended: now.Add(-hour)})

	got, err := reports.NewPostgresStore(f.database, 0).LLMUsage(f.ctx, usageFilter(7))
	require.NoError(t, err)
	require.Len(t, got.Agents, 1)
	assert.Empty(t, got.Agents[0].Name)
	assert.EqualValues(t, 4, got.Agents[0].Requests)
}

func TestLLMUsageTenantIsolation(t *testing.T) {
	t.Parallel()
	app, owner := dbtest.WithOwnerDB(t)
	a := &fixture{database: app, ctx: db.WithTenant(context.Background(), dbtest.NewTenant(t, app))}
	b := &fixture{database: app, ctx: db.WithTenant(context.Background(), dbtest.NewTenant(t, app))}
	store := reports.NewPostgresStore(app, 0)

	agentA, agentB := a.agentRow(t, "alice"), b.agentRow(t, "bob")
	ticketB := b.plainTicket(t, "done")
	a.usage(t, a.ctx, usageSpec{agent: agentA, key: "k", requests: 1, est: micros(1), ended: now.Add(-hour),
		tools: []toolSpec{{"bash", "Bash", 1, 0, 1}}})
	b.usage(t, b.ctx, usageSpec{agent: agentB, key: "k", requests: 1000, est: micros(1000), ticket: &ticketB, ended: now.Add(-hour),
		tools: []toolSpec{{"bash", "Bash", 1000, 0, 1000}}})

	var everyone int
	require.NoError(t, owner.Global().QueryRow(context.Background(), `SELECT count(*) FROM llm_usage`).Scan(&everyone))
	require.Equal(t, 2, everyone)

	got, err := store.LLMUsage(a.ctx, usageFilter(7))
	require.NoError(t, err)
	assert.EqualValues(t, 1, sumsFor(t, got.Totals, reports.BillingSubscription).Requests)
	require.Len(t, got.Agents, 1)
	assert.Equal(t, "alice", got.Agents[0].Name)
	assert.EqualValues(t, 1, got.Tools[0].Calls)
	assert.Zero(t, got.Tickets)
	assert.Empty(t, got.DoneTickets)

	page, err := store.LLMUsageTickets(a.ctx, usageFilter(7), usageQuery(10, nil))
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	assert.Zero(t, page.Total)

	gotB, err := store.LLMUsage(b.ctx, usageFilter(7))
	require.NoError(t, err)
	assert.EqualValues(t, 1000, sumsFor(t, gotB.Totals, reports.BillingSubscription).Requests)
	assert.EqualValues(t, 1, gotB.Tickets)
}

// guidance inserts a llm_guidance_usage row, the charge a declaration made.
func (f *fixture) guidance(t *testing.T, ctx context.Context, s usageSpec) {
	t.Helper()
	if s.model == "" {
		s.model = "m1"
	}
	if s.mode == "" {
		s.mode = reports.BillingSubscription
	}
	_, err := f.database.Exec(ctx, `
INSERT INTO llm_guidance_usage (agent_id, transcript_key, ticket_id, harness, provider, model, billing_mode, requests,
    input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens,
    reported_cost_microdollars, estimated_cost_microdollars, price_version, unpriced, ended_at)
VALUES ($1, $2, $3, 'claude', 'p', $4, $5, $6, $7, $8, $9, 0, 0, $10, $11, 'v', ($10::bigint IS NULL AND $11::bigint IS NULL), $12)`,
		s.agent, s.key, s.ticket, s.model, s.mode, s.requests, s.in, s.out, s.cacheR, s.reported, s.est, s.ended)
	require.NoError(t, err)
}

func TestLLMUsageTicketsShowWorkerAndGuidanceApart(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	worker, console := f.agentRow(t, "worker"), f.agentRow(t, "console")
	both, only, plain := f.plainTicket(t, "open"), f.plainTicket(t, "open"), f.plainTicket(t, "open")
	f.usage(t, f.ctx, usageSpec{agent: worker, key: "w1", in: 100, est: micros(10), ticket: &both, ended: now.Add(-hour)})
	f.usage(t, f.ctx, usageSpec{agent: worker, key: "w2", in: 50, est: micros(5), ticket: &plain, ended: now.Add(-hour)})
	f.guidance(t, f.ctx, usageSpec{agent: console, key: "c1", in: 9000, est: micros(900), ticket: &both, ended: now.Add(-hour)})
	f.guidance(t, f.ctx, usageSpec{agent: console, key: "c1", in: 70, mode: reports.BillingMetered, reported: micros(7), ticket: &only, ended: now.Add(-hour)})
	store := reports.NewPostgresStore(f.database, 0)

	page, err := store.LLMUsageTickets(f.ctx, usageFilter(7), usageQuery(10, nil))
	require.NoError(t, err)
	assert.EqualValues(t, 3, page.Total)
	// Ranked by worker tokens: the 9000 guidance tokens of `both` do not matter;
	// 100 > 50 puts it first, and the guidance-only ticket is last.
	assert.Equal(t, []int64{both, plain, only}, ids(page))

	b := page.Items[0]
	require.Len(t, b.Modes, 1)
	require.Len(t, b.Guidance, 1)
	assert.EqualValues(t, 100, b.Modes[0].InputTokens, "the worker's figure has no guidance in it")
	assert.EqualValues(t, 10, *b.Modes[0].CostMicros)
	assert.EqualValues(t, 9000, b.Guidance[0].InputTokens)
	assert.EqualValues(t, 900, *b.Guidance[0].CostMicros)

	assert.Empty(t, page.Items[1].Guidance, "a ticket without guidance has none")
	g := page.Items[2]
	assert.Empty(t, g.Modes, "guidance only: no worker figure")
	require.Len(t, g.Guidance, 1)
	assert.Equal(t, reports.BillingMetered, g.Guidance[0].BillingMode)
	assert.EqualValues(t, 7, *g.Guidance[0].CostMicros)
}

func TestLLMUsageTicketsGuidanceOnlyTicketsPageAndFilter(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agentRow(t, "c")
	open, done := f.plainTicket(t, "open"), f.plainTicket(t, "done")
	f.guidance(t, f.ctx, usageSpec{agent: a, key: "k1", in: 5, ticket: &open, ended: now.Add(-hour)})
	f.guidance(t, f.ctx, usageSpec{agent: a, key: "k2", in: 5, ticket: &done, ended: now.Add(-hour)})
	f.guidance(t, f.ctx, usageSpec{agent: a, key: "k3", in: 5, ticket: &done, ended: now.AddDate(0, 0, -30)})
	store := reports.NewPostgresStore(f.database, 0)

	first, err := store.LLMUsageTickets(f.ctx, usageFilter(7), usageQuery(1, nil))
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	require.NotNil(t, first.Next)
	q := usageQuery(1, nil)
	q.After = first.Next
	second, err := store.LLMUsageTickets(f.ctx, usageFilter(7), q)
	require.NoError(t, err)
	assert.ElementsMatch(t, []int64{open, done}, []int64{first.Items[0].TicketID, second.Items[0].TicketID})
	assert.Nil(t, second.Next)

	onlyDone, err := store.LLMUsageTickets(f.ctx, usageFilter(7), usageQuery(10, &ticket.ListFilter{Statuses: []ticket.Status{"done"}}))
	require.NoError(t, err)
	assert.Equal(t, []int64{done}, ids(onlyDone))
	assert.EqualValues(t, 5, onlyDone.Items[0].Guidance[0].InputTokens, "the row ended 30 days ago is another period's")
}

func TestLLMUsageOverheadDoesNotCountGuidanceTwice(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	console := f.agentRow(t, "console")
	tk := f.plainTicket(t, "open")
	// The console's transcript is an overhead row of 1000 tokens, 100 of which
	// were declared as guidance on the ticket: the other 900 are overhead.
	f.usage(t, f.ctx, usageSpec{agent: console, key: "c1", requests: 10, in: 1000, est: micros(500), ended: now.Add(-hour)})
	f.guidance(t, f.ctx, usageSpec{agent: console, key: "c1", requests: 3, in: 100, est: micros(50), ticket: &tk, ended: now.Add(-hour)})

	got, err := reports.NewPostgresStore(f.database, 0).LLMUsage(f.ctx, usageFilter(7))
	require.NoError(t, err)
	total := sumsFor(t, got.Totals, reports.BillingSubscription)
	guided := sumsFor(t, got.Guidance, reports.BillingSubscription)
	loose := sumsFor(t, got.Unattributed, reports.BillingSubscription)
	assert.EqualValues(t, 1000, total.InputTokens, "the totals are the usage reported, once")
	assert.EqualValues(t, 100, guided.InputTokens)
	assert.EqualValues(t, 3, guided.Requests)
	assert.EqualValues(t, 50, *guided.CostMicros)
	assert.EqualValues(t, 900, loose.InputTokens)
	assert.EqualValues(t, 7, loose.Requests)
	assert.EqualValues(t, 450, *loose.CostMicros)
	assert.EqualValues(t, 1, got.Tickets, "a ticket with guidance only has usage")
}

func TestLLMUsageGuidanceNeverTakesOverheadBelowZero(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agentRow(t, "console")
	tk := f.plainTicket(t, "open")
	// The overhead row has moved on to a later period than the guidance row
	// charged from it, so only the guidance is in this one.
	f.usage(t, f.ctx, usageSpec{agent: a, key: "c1", in: 10, est: micros(5), ended: now.Add(-hour)})
	f.guidance(t, f.ctx, usageSpec{agent: a, key: "c1", in: 100, est: micros(50), ticket: &tk, ended: now.Add(-hour)})

	got, err := reports.NewPostgresStore(f.database, 0).LLMUsage(f.ctx, usageFilter(7))
	require.NoError(t, err)
	loose := sumsFor(t, got.Unattributed, reports.BillingSubscription)
	assert.Zero(t, loose.InputTokens)
	assert.Zero(t, *loose.CostMicros)
}

func TestLLMUsageGuidanceOfAnotherTenantIsHidden(t *testing.T) {
	t.Parallel()
	app, _ := dbtest.WithOwnerDB(t)
	a := &fixture{database: app, ctx: db.WithTenant(context.Background(), dbtest.NewTenant(t, app))}
	b := &fixture{database: app, ctx: db.WithTenant(context.Background(), dbtest.NewTenant(t, app))}
	store := reports.NewPostgresStore(app, 0)
	agentB := b.agentRow(t, "bob")
	ticketB := b.plainTicket(t, "open")
	b.guidance(t, b.ctx, usageSpec{agent: agentB, key: "k", requests: 5, in: 1000, est: micros(1), ticket: &ticketB, ended: now.Add(-hour)})

	got, err := store.LLMUsage(a.ctx, usageFilter(7))
	require.NoError(t, err)
	assert.Empty(t, got.Guidance)
	assert.Zero(t, got.Tickets)
	page, err := store.LLMUsageTickets(a.ctx, usageFilter(7), usageQuery(10, nil))
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	assert.Zero(t, page.Total)

	gotB, err := store.LLMUsage(b.ctx, usageFilter(7))
	require.NoError(t, err)
	assert.EqualValues(t, 1000, sumsFor(t, gotB.Guidance, reports.BillingSubscription).InputTokens)
}

// A session reports its cumulative totals across midnight: each day gets what
// was added on it, and the agent and model figures are the sum of the days.
func TestLLMUsageMultiDaySessionSplitsAcrossDays(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agentRow(t, "a")
	svc := llmusage.NewPostgresStore(f.database, pricing.Default())
	store := reports.NewPostgresStore(f.database, 0)
	tr := &llmusage.Transcript{AgentID: a, Key: "t1", Harness: usagelib.HarnessPi, SessionID: "s", ScopeHint: "main"}
	day1 := now.AddDate(0, 0, -2).Add(-2 * hour)
	day2 := day1.Add(24 * hour)
	report := func(requests int64, ended time.Time) {
		t.Helper()
		r := usagelib.Record{
			SchemaVersion: usagelib.SchemaVersion, Provider: "p", Model: "claude-opus-5-5", Harness: usagelib.HarnessPi,
			BillingMode: usagelib.BillingSubscription, Requests: requests, InputTokens: requests * 100_000, OutputTokens: requests * 10_000,
			StartedAt: day1.Add(-hour), EndedAt: ended,
		}
		pricing.Default().Estimate(&r)
		_, _, err := svc.Upsert(f.ctx, tr, []usagelib.Record{r})
		require.NoError(t, err)
	}
	report(5, day1)
	report(8, day1.Add(hour))
	report(8, day1.Add(hour)) // a hook firing twice adds nothing
	report(20, day2)

	got, err := store.LLMUsage(f.ctx, usageFilter(7))
	require.NoError(t, err)
	require.Len(t, got.Days, 2)
	assert.True(t, got.Days[0].Day.Before(got.Days[1].Day))
	assert.EqualValues(t, 8, got.Days[0].Requests, "what was reported on the first day")
	assert.EqualValues(t, 12, got.Days[1].Requests, "only what the second day added")
	cost := func(requests int64) int64 {
		r := usagelib.Record{Model: "claude-opus-5-5", InputTokens: requests * 100_000, OutputTokens: requests * 10_000}
		pricing.Default().Estimate(&r)
		require.NotNil(t, r.EstimatedCostMicros)
		return *r.EstimatedCostMicros
	}
	require.NotNil(t, got.Days[1].CostMicros)
	assert.EqualValues(t, cost(20)-cost(8), *got.Days[1].CostMicros, "the cost it added, not the session's whole")

	var days int64
	for _, d := range got.Days {
		days += d.Requests
	}
	require.Len(t, got.Agents, 1)
	require.Len(t, got.Models, 1)
	assert.EqualValues(t, 20, days)
	assert.EqualValues(t, days, got.Agents[0].Requests, "window totals are the sum of the days")
	assert.EqualValues(t, days, got.Models[0].Requests)
	assert.EqualValues(t, 20, sumsFor(t, got.Totals, reports.BillingSubscription).Requests)

	// A window ending before the second day gets only what the first added,
	// though the stored session total is 20.
	early, err := store.LLMUsage(f.ctx, &reports.UsageFilter{From: now.AddDate(0, 0, -7), To: day2.Truncate(24 * time.Hour)})
	require.NoError(t, err)
	require.Len(t, early.Agents, 1)
	require.Len(t, early.Models, 1)
	assert.EqualValues(t, 8, early.Agents[0].Requests)
	assert.EqualValues(t, 8, early.Models[0].Requests)
}

// What a report writes to the rollup keeps the cost rule and the unpriced
// counts of the figures it feeds: reported over estimated, and a model the
// table does not know is unpriced.
func TestLLMUsageDailyKeepsCostAndUnpricedFromReports(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agentRow(t, "a")
	svc := llmusage.NewPostgresStore(f.database, pricing.Default())
	tr := &llmusage.Transcript{AgentID: a, Key: "t1", Harness: usagelib.HarnessPi, SessionID: "s", ScopeHint: "main"}
	ended := now.Add(-hour)
	report := func(model string, reported *int64, requests int64) {
		t.Helper()
		r := usagelib.Record{
			SchemaVersion: usagelib.SchemaVersion, Provider: "p", Model: model, Harness: usagelib.HarnessPi,
			BillingMode: usagelib.BillingMetered, Requests: requests, InputTokens: requests * 10, OutputTokens: requests,
			ReportedCostMicros: reported, StartedAt: ended.Add(-hour), EndedAt: ended,
		}
		pricing.Default().Estimate(&r)
		_, _, err := svc.Upsert(f.ctx, tr, []usagelib.Record{r})
		require.NoError(t, err)
	}
	report("mystery", nil, 3)
	report("mystery", nil, 5)
	report("claude-opus-5-5", micros(2_000), 4)

	got, err := reports.NewPostgresStore(f.database, 0).LLMUsage(f.ctx, usageFilter(7))
	require.NoError(t, err)
	// The last report replaces the transcript's models, so only the priced
	// one remains in llm_usage; the rollup keeps what each report added.
	require.Len(t, got.Models, 2)
	byModel := map[string]reports.UsageModel{}
	for _, m := range got.Models {
		byModel[m.Model] = m
	}
	mystery := byModel["mystery"]
	assert.EqualValues(t, 5, mystery.Requests)
	assert.EqualValues(t, 5, mystery.UnpricedRequests)
	assert.EqualValues(t, 2, mystery.UnpricedRecords)
	assert.Nil(t, mystery.CostMicros)
	opus := byModel["claude-opus-5-5"]
	assert.EqualValues(t, 4, opus.Requests)
	require.NotNil(t, opus.CostMicros)
	assert.EqualValues(t, 2_000, *opus.CostMicros, "reported over estimated")
	assert.Zero(t, opus.UnpricedRecords)
}

// Rows flagged as over-counted are left out of every figure by default and
// counted when asked, while the report still says they exist.
func TestLLMUsageLeavesOutUnreliableRowsUnlessAsked(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	good, bad := f.agentRow(t, "worker"), f.agentRow(t, "conductor")
	tk := f.plainTicket(t, "open")
	ended := now.Add(-2 * hour)
	f.usage(t, f.ctx, usageSpec{agent: good, key: "g", requests: 2, in: 20, est: micros(200), ticket: &tk, ended: ended})
	f.usage(t, f.ctx, usageSpec{agent: bad, key: "b", requests: 10, in: 1000, est: micros(5000), ticket: &tk, ended: ended})
	f.guidance(t, f.ctx, usageSpec{agent: bad, key: "b", ticket: &tk, requests: 4, in: 400, est: micros(100), ended: ended})
	// A ticket whose only usage is unreliable, one of them done.
	onlyBad, doneBad := f.plainTicket(t, "open"), f.plainTicket(t, "done")
	f.usage(t, f.ctx, usageSpec{agent: bad, key: "b2", requests: 1, in: 1, est: micros(1), ticket: &onlyBad, ended: ended})
	f.usage(t, f.ctx, usageSpec{agent: bad, key: "b3", requests: 1, in: 1, est: micros(9000), ticket: &doneBad, ended: ended})
	doneGood := f.plainTicket(t, "done")
	f.usage(t, f.ctx, usageSpec{agent: good, key: "g2", requests: 1, in: 1, est: micros(300), ticket: &doneGood, ended: ended})
	for _, table := range []string{"llm_usage", "llm_usage_daily", "llm_guidance_usage"} {
		_, err := f.database.Exec(f.ctx, `UPDATE `+table+` SET unreliable = true WHERE agent_id = $1`, bad)
		require.NoError(t, err)
	}
	store := reports.NewPostgresStore(f.database, 0)

	left, err := store.LLMUsage(f.ctx, usageFilter(7))
	require.NoError(t, err)
	totals := sumsFor(t, left.Totals, reports.BillingSubscription)
	assert.EqualValues(t, 3, totals.Requests)
	assert.EqualValues(t, 21, totals.InputTokens)
	assert.EqualValues(t, 2, totals.Transcripts)
	require.Len(t, left.Days, 1)
	assert.EqualValues(t, 3, left.Days[0].Requests)
	require.Len(t, left.Agents, 1)
	assert.Equal(t, "worker", left.Agents[0].Name)
	require.Len(t, left.Models, 1)
	assert.EqualValues(t, 3, left.Models[0].Requests)
	assert.Empty(t, left.Guidance)
	assert.EqualValues(t, 2, left.Tickets, "the tickets with any counted usage")
	require.Len(t, left.DoneTickets, 1)
	assert.EqualValues(t, 1, left.DoneTickets[0].Tickets)
	assert.EqualValues(t, 300, *left.DoneTickets[0].CostMicros)
	assert.EqualValues(t, 4, left.Unreliable, "the report says what it left out: usage rows and the guidance row")

	page, err := store.LLMUsageTickets(f.ctx, usageFilter(7), usageQuery(10, nil))
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.EqualValues(t, 2, sumsFor(t, page.Items[0].Modes, reports.BillingSubscription).Requests)
	assert.Empty(t, page.Items[0].Guidance)

	all := usageFilter(7)
	all.IncludeUnreliable = true
	got, err := store.LLMUsage(f.ctx, all)
	require.NoError(t, err)
	assert.EqualValues(t, 15, sumsFor(t, got.Totals, reports.BillingSubscription).Requests)
	assert.EqualValues(t, 15, got.Days[0].Requests)
	assert.Len(t, got.Agents, 2)
	assert.EqualValues(t, 400, sumsFor(t, got.Guidance, reports.BillingSubscription).InputTokens)
	assert.EqualValues(t, 4, got.Unreliable)
	assert.EqualValues(t, 4, got.Tickets)
	require.Len(t, got.DoneTickets, 1)
	assert.EqualValues(t, 2, got.DoneTickets[0].Tickets)

	q := usageQuery(10, nil)
	q.IncludeUnreliable = true
	page, err = store.LLMUsageTickets(f.ctx, all, q)
	require.NoError(t, err)
	assert.EqualValues(t, 12, sumsFor(t, page.Items[0].Modes, reports.BillingSubscription).Requests)
}
