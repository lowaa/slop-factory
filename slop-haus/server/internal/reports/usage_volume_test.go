package reports_test

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reports"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// The LLM usage section is proved at volume the way the status-change
// reports are: a year of usage for several tenants and agents, hundreds of
// thousands of transcripts, every query EXPLAINed for the index it was
// designed around and held to a bound on the buffers it touches.

// Transcripts seeded per tenant, evenly over the year.
var usageVolumeTenants = []int{100000, 80000, 70000, 50000}

const usageAgentsPerTenant = 12

// seedUsageVolume gives the tenant a year of usage. Each transcript has one
// model row, a fifth also a second model; a tenth is metered (reported cost)
// and the rest subscription (estimated), with one in fifty unpriced; seven in
// ten are attributed to one of the tenant's tickets, which are mostly done.
// Every row has three tool rows.
func seedUsageVolume(t *testing.T, ctx context.Context, database *db.DB, transcripts int) {
	t.Helper()
	tickets := transcripts/6 + 1
	require.NoError(t, database.InTx(ctx, func(q db.Querier) error {
		if _, err := q.Exec(ctx, `INSERT INTO agents (id, name) SELECT gen_random_uuid(), 'agent-' || i FROM generate_series(1, $1::int) i`,
			usageAgentsPerTenant); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `
INSERT INTO tickets (id, title, status) SELECT n, 'ticket ' || n, CASE WHEN n % 10 < 8 THEN 'done' ELSE 'open' END
  FROM generate_series(1, $1::int) n`, tickets); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `
WITH ag AS (SELECT id, row_number() OVER (ORDER BY name) - 1 AS i FROM agents),
     tx AS (
    SELECT n, ag.id AS agent_id, k,
           $1::timestamptz - (n::float / $2::int * 364) * interval '1 day' - interval '1 hour' AS ended
      FROM generate_series(1, $2::int) n
      JOIN ag ON ag.i = n % $3::int
      CROSS JOIN LATERAL (VALUES (0), (1)) m(k)
     WHERE k = 0 OR n % 5 = 0)
INSERT INTO llm_usage (agent_id, transcript_key, harness, session_id, scope_hint, scope, ticket_id, provider, model,
    billing_mode, requests, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens,
    reported_cost_microdollars, estimated_cost_microdollars, price_version, unpriced, started_at, ended_at)
SELECT agent_id, 'tx-' || n, 'pi', 's', 'main',
       CASE WHEN n % 10 < 7 THEN 'ticket' ELSE 'overhead' END,
       CASE WHEN n % 10 < 7 THEN n / 6 + 1 END,
       'anthropic', CASE k WHEN 0 THEN 'claude-opus' ELSE 'claude-haiku' END,
       CASE WHEN n % 10 = 0 THEN 'metered' ELSE 'subscription' END,
       n % 40 + 1, 1000 + n % 500, 500 + n % 300, 20000 + n % 9000, n % 700, n % 100,
       CASE WHEN n % 10 = 0 THEN 900 + n % 5000 END,
       CASE WHEN n % 50 <> 1 THEN 1000 + n % 7000 END,
       'v', n % 50 = 1, ended - interval '20 minutes', ended
  FROM tx`, usageVolumeNow(), transcripts, usageAgentsPerTenant); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `
INSERT INTO llm_usage_daily (agent_id, day, provider, model, billing_mode, records, requests, input_tokens, output_tokens,
    cache_read_tokens, cache_write_tokens, reasoning_tokens, cost_microdollars)
SELECT agent_id, date_trunc('day', ended_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', provider, model, billing_mode,
       count(*), sum(requests), sum(input_tokens), sum(output_tokens), sum(cache_read_tokens), sum(cache_write_tokens),
       sum(reasoning_tokens), sum(COALESCE(reported_cost_microdollars, estimated_cost_microdollars))
  FROM llm_usage GROUP BY 1, 2, 3, 4, 5`); err != nil {
			return err
		}
		_, err := q.Exec(ctx, `
INSERT INTO llm_tool_usage (agent_id, transcript_key, provider, model, harness, billing_mode, category, tool, calls, errors, duration_ms, result_bytes, ended_at)
SELECT u.agent_id, u.transcript_key, u.provider, u.model, u.harness, u.billing_mode, c.category, c.tool,
       10 + u.requests, u.requests % 3, 100 * u.requests, 1000, u.ended_at
  FROM llm_usage u
  CROSS JOIN (VALUES ('bash', 'Bash'), ('file_read', 'Read'), ('search', 'Grep')) c(category, tool)`)
		return err
	}))
}

// usageVolumeNow is fixed per run, so every tenant is seeded against the
// same clock the queries are run at.
var usageVolumeClock = time.Now().UTC()

func usageVolumeNow() time.Time { return usageVolumeClock }

// usagePlanProblems is what is wrong with the plan: a sequential scan of
// either usage table, or an index the query is meant to use that it does not.
func usagePlanProblems(p *plan, wantIndexes ...string) []string {
	var problems []string
	used := map[string]bool{}
	p.Plan.walk(func(n *planNode) {
		if n.Index != "" {
			used[n.Index] = true
		}
		if n.NodeType == "Seq Scan" && (n.Relation == "llm_usage" || n.Relation == "llm_tool_usage" || n.Relation == "llm_usage_daily") {
			problems = append(problems, "sequential scan of "+n.Relation)
		}
	})
	for _, idx := range wantIndexes {
		if !used[idx] {
			problems = append(problems, "does not use "+idx)
		}
	}
	return problems
}

func TestLLMUsageAtVolume(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("seeds a million rows")
	}
	app, owner := dbtest.WithOwnerDB(t)
	var tenants []context.Context
	for _, n := range usageVolumeTenants {
		ctx := db.WithTenant(context.Background(), dbtest.NewTenant(t, app))
		seedUsageVolume(t, ctx, app, n)
		tenants = append(tenants, ctx)
	}
	_, err := owner.Global().Exec(context.Background(), `ANALYZE agents, tickets, llm_usage, llm_usage_daily, llm_tool_usage`)
	require.NoError(t, err)

	var transcripts, rows, toolRows int
	require.NoError(t, owner.Global().QueryRow(context.Background(),
		`SELECT count(DISTINCT (tenant_id, agent_id, transcript_key)), count(*), (SELECT count(*) FROM llm_tool_usage) FROM llm_usage`).
		Scan(&transcripts, &rows, &toolRows))
	require.GreaterOrEqual(t, transcripts, 300_000, "the volume the reports are proved at")
	t.Logf("%d transcripts, %d usage rows and %d tool rows across %d tenants", transcripts, rows, toolRows, len(tenants))

	busiest := tenants[0]
	now := usageVolumeNow()
	period := func(days int) []any { return []any{now.AddDate(0, 0, -days), now} }
	withUnreliable := func(args []any) []any { return append(append([]any{}, args...), false) }

	type checked struct {
		name string
		args []any
		// sql replaces the named report query, for one built from a filter.
		sql     string
		indexes []string
	}
	queryOf := func(c checked) string {
		if c.sql != "" {
			return c.sql
		}
		return reports.Queries[c.name]
	}
	cases := func(days int) []checked {
		p := period(days)
		f := &reports.UsageFilter{From: p[0].(time.Time), To: now}
		firstSQL, firstArgs := reports.UsageTicketsSQL(f, &reports.UsageTicketQuery{Filter: &ticket.ListFilter{}, Limit: reports.MaxUsageTickets})
		// A filtered page after a cursor: the heaviest shape the list takes.
		filteredSQL, filteredArgs := reports.UsageTicketsSQL(f, &reports.UsageTicketQuery{
			Filter: &ticket.ListFilter{Statuses: []ticket.Status{ticket.StatusDone}, Priorities: []model.Priority{"high"}, Text: "usage"},
			Limit:  reports.MaxUsageTickets,
			After:  &reports.UsageTicketCursor{Tokens: 1_000, TicketID: 5},
		})
		withLimit := append(withUnreliable(p), reports.MaxUsageGroups)
		return []checked{
			{name: "usage_totals", args: withUnreliable(p), indexes: []string{"llm_usage_by_ended"}},
			{name: "usage_days", args: withUnreliable(p), indexes: []string{"llm_usage_daily_pkey"}},
			{name: "usage_agents", args: withLimit, indexes: []string{"llm_usage_daily_pkey"}},
			{name: "usage_models", args: withLimit, indexes: []string{"llm_usage_daily_pkey"}},
			{name: "usage_unreliable", args: p, indexes: []string{"llm_usage_unreliable_by_ended"}},
			{name: "usage_tools", args: p, indexes: []string{"llm_tool_usage_by_ended"}},
			{name: "usage_ticket_count", args: withUnreliable(p), indexes: []string{"llm_usage_by_ended"}},
			{name: "usage_done", args: withUnreliable(p), indexes: []string{"llm_usage_by_ended"}},
			{name: "usage_tickets", sql: firstSQL, args: firstArgs, indexes: []string{"llm_usage_by_ended"}},
			{name: "usage_tickets_filtered_after", sql: filteredSQL, args: filteredArgs, indexes: []string{"llm_usage_by_ended"}},
		}
	}

	for _, days := range []int{7, 30, 90} {
		for _, c := range cases(days) {
			t.Run(fmt.Sprintf("%s/%dd", c.name, days), func(t *testing.T) {
				p, text := explain(t, busiest, app, queryOf(c), c.args...)
				t.Logf("%s over %d days: %.1fms, %d blocks\n%s", c.name, days, p.ExecutionTime, p.blocks(), text)
				assert.Empty(t, usagePlanProblems(p, c.indexes...))
				assert.Less(t, p.blocks(), int64(perQueryBlockBudget))
			})
		}
	}

	t.Run("whole report", func(t *testing.T) {
		store := reports.NewPostgresStore(app, 0)
		f := &reports.UsageFilter{From: now.AddDate(0, 0, -90), To: now}
		start := time.Now()
		got, err := store.LLMUsage(busiest, f)
		require.NoError(t, err)
		elapsed := time.Since(start)
		t.Logf("a 90-day LLM usage report for the busiest tenant: %s", elapsed)
		// Wall-clock, so opt-in: a busy CI agent fails it for no fault of the report.
		if _, ok := os.LookupEnv("SLOP_HAUS_PERF_BUDGETS"); ok {
			assert.Less(t, elapsed, 2*time.Second)
		}
		assert.NotEmpty(t, got.Totals)
		assert.LessOrEqual(t, len(got.Agents), reports.MaxUsageGroups)
	})

	// The check above must be able to fail: without the index it exists to
	// find, the same plans are reported as a problem.
	t.Run("a dropped index is caught", func(t *testing.T) {
		for _, idx := range []string{"llm_usage_by_ended", "llm_usage_unreliable_by_ended", "llm_tool_usage_by_ended"} {
			_, err := owner.Global().Exec(context.Background(), `DROP INDEX `+idx)
			require.NoError(t, err)
		}
		for _, c := range cases(30) {
			if slices.Contains(c.indexes, "llm_usage_daily_pkey") {
				continue // its primary key cannot be dropped
			}
			p, _ := explain(t, busiest, app, queryOf(c), c.args...)
			assert.NotEmpty(t, usagePlanProblems(p, c.indexes...), c.name)
		}
	})
}
