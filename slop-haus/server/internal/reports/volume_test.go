package reports_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reports"
)

// The reports run on every page view over a table that only grows, so they
// are proved at volume: a year of history for several tenants, hundreds of
// thousands of status changes in all. The plans must use the indexes the
// queries were designed around -- never a sequential scan of the history --
// and each report must fit a budget with plenty of headroom, so a plan that
// quietly stops using an index fails here, long before it is slow in
// production.

// Tickets seeded per tenant; each makes about six changes, over a year.
var volumeTenants = []int{30000, 25000, 15000, 15000}

// seedVolume gives the tenant tickets created evenly over the year before
// now (minus a fortnight, so no ticket's changes run past now), each walking
// open, in_progress, discussion, in_progress, review, done at gaps that
// differ by ticket. A few in every hundred stop on the way, so the board has
// tickets in every state. The creation rows the trigger writes are replaced
// by the walk.
func seedVolume(t *testing.T, ctx context.Context, database *db.DB, tickets int) {
	t.Helper()
	require.NoError(t, database.InTx(ctx, func(q db.Querier) error {
		if _, err := q.Exec(ctx, `
INSERT INTO tickets (id, title, priority, status, created_at)
SELECT n, 'ticket ' || n,
       (ARRAY['low', 'normal', 'high', 'urgent'])[1 + n % 4],
       CASE WHEN n % 100 < 92 THEN 'done'
            WHEN n % 100 = 92 THEN 'open'
            WHEN n % 100 < 96 THEN 'in_progress'
            WHEN n % 100 < 98 THEN 'discussion'
            WHEN n % 100 = 98 THEN 'in_progress'
            ELSE 'review' END,
       $1::timestamptz - interval '14 days' - (n::float / $2 * 351) * interval '1 day'
  FROM generate_series(1, $2) n`, time.Now(), tickets); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `DELETE FROM ticket_status_changes`); err != nil {
			return err
		}
		_, err := q.Exec(ctx, `
WITH walk(step, to_status, gap_hours) AS (
    VALUES (0, 'open', 0), (1, 'in_progress', 2), (2, 'discussion', 1),
           (3, 'in_progress', 6), (4, 'review', 3), (5, 'done', 2)
), last_step AS (
    SELECT id, created_at, priority,
           CASE WHEN id % 100 < 92 THEN 5 WHEN id % 100 = 92 THEN 0 WHEN id % 100 < 96 THEN 1
                WHEN id % 100 < 98 THEN 2 WHEN id % 100 = 98 THEN 3 ELSE 4 END AS last
      FROM tickets
), timed AS (
    SELECT l.id, l.priority, w.step, w.to_status,
           l.created_at + interval '1 hour' * sum(w.gap_hours * (1 + l.id % 7)) OVER (PARTITION BY l.id ORDER BY w.step) AS at
      FROM last_step l JOIN walk w ON w.step <= l.last
)
INSERT INTO ticket_status_changes (ticket_id, from_status, to_status, priority, changed_at, actor_kind)
SELECT id, lag(to_status) OVER (PARTITION BY id ORDER BY step), to_status, priority, at, 'system'
  FROM timed ORDER BY id, step`)
		return err
	}))
}

type planNode struct {
	NodeType string     `json:"Node Type"`
	Relation string     `json:"Relation Name"`
	Index    string     `json:"Index Name"`
	Plans    []planNode `json:"Plans"`
	// Buffers the node and its children touched, in cache or read in.
	SharedHit  int64 `json:"Shared Hit Blocks"`
	SharedRead int64 `json:"Shared Read Blocks"`
}

type plan struct {
	Plan          planNode `json:"Plan"`
	ExecutionTime float64  `json:"Execution Time"`
}

// blocks is the 8kB buffers the whole query touched. Unlike its execution
// time it does not depend on how busy the machine is, and a query that stops
// using its index, or reads more than it should, touches many times more.
func (p *plan) blocks() int64 { return p.Plan.SharedHit + p.Plan.SharedRead }

func (n *planNode) walk(fn func(*planNode)) {
	fn(n)
	for i := range n.Plans {
		n.Plans[i].walk(fn)
	}
}

// explain runs the query under EXPLAIN (ANALYZE, BUFFERS) in the tenant, as
// the app role, and returns the plan, the text a person reads it from, and
// how long it took to execute.
func explain(t *testing.T, ctx context.Context, database *db.DB, query string, args ...any) (*plan, string) {
	t.Helper()
	var raw, text string
	require.NoError(t, database.InReadTx(ctx, func(q db.Querier) error {
		if err := q.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+query, args...).Scan(&raw); err != nil {
			return err
		}
		rows, err := q.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			text += line + "\n"
		}
		return rows.Err()
	}))
	var plans []plan
	require.NoError(t, json.Unmarshal([]byte(raw), &plans))
	return &plans[0], text
}

// planProblems is what is wrong with the plan: any sequential scan of the
// history or the tickets, or an index the query is meant to use that it does
// not.
func planProblems(p *plan, wantIndexes ...string) []string {
	var problems []string
	used := map[string]bool{}
	p.Plan.walk(func(n *planNode) {
		if n.Index != "" {
			used[n.Index] = true
		}
		if n.NodeType == "Seq Scan" && (n.Relation == "ticket_status_changes" || n.Relation == "tickets") {
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

// perQueryBlockBudget is the buffers each report query may touch, with
// about three times the most any of them touched when this was written (156k). It stands
// in for a time budget, which fails on a busy CI runner for reasons that are
// not the query's. A lost index costs far more than the headroom.
const perQueryBlockBudget = 500_000

func TestReportsAtVolume(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("seeds hundreds of thousands of rows")
	}
	app, owner := dbtest.WithOwnerDB(t)
	var tenants []context.Context
	for _, n := range volumeTenants {
		ctx := db.WithTenant(context.Background(), dbtest.NewTenant(t, app))
		seedVolume(t, ctx, app, n)
		tenants = append(tenants, ctx)
	}
	// What autovacuum does in production, which has no time to in a test.
	_, err := owner.Global().Exec(context.Background(), `ANALYZE tickets, ticket_status_changes`)
	require.NoError(t, err)

	var total int
	require.NoError(t, owner.Global().QueryRow(context.Background(), `SELECT count(*) FROM ticket_status_changes`).Scan(&total))
	require.Greater(t, total, 300_000, "the volume the reports are proved at")
	t.Logf("%d status changes across %d tenants", total, len(tenants))

	// The busiest tenant, over the longest period: the worst case.
	busiest := tenants[0]
	now := time.Now().UTC()
	window := func(days int) []any {
		return []any{now.AddDate(0, 0, -days), now, ""}
	}

	type checked struct {
		name    string
		query   string
		args    []any
		indexes []string
	}
	cases := func(days int) []checked {
		return []checked{
			{"history_from", reports.Queries["history_from"], nil, []string{"ticket_status_changes_by_time"}},
			{"states", reports.Queries["states"], window(days),
				[]string{"ticket_status_changes_by_time", "ticket_status_changes_by_ticket", "tickets_live"}},
			{"lead", reports.Queries["lead"], append(window(days), false),
				[]string{"ticket_status_changes_by_status_time", "ticket_status_changes_by_ticket"}},
			{"throughput", reports.Queries["throughput"], window(days), []string{"ticket_status_changes_by_status_time"}},
			{"now", reports.Queries["now"], []any{""}, []string{"tickets_live"}},
			{"waiting", reports.Queries["waiting"], []any{"discussion", "", 50, 0}, []string{"tickets_live", "ticket_status_changes_by_ticket"}},
			{"waiting_total", reports.Queries["waiting_total"], []any{"discussion", ""}, []string{"tickets_live"}},
		}
	}

	for _, days := range []int{7, 30, 90} {
		for _, c := range cases(days) {
			t.Run(fmt.Sprintf("%s/%dd", c.name, days), func(t *testing.T) {
				p, text := explain(t, busiest, app, c.query, c.args...)
				t.Logf("%s over %d days: %.1fms, %d blocks\n%s", c.name, days, p.ExecutionTime, p.blocks(), text)
				assert.Empty(t, planProblems(p, c.indexes...))
				assert.Less(t, p.blocks(), int64(perQueryBlockBudget))
			})
		}
	}

	t.Run("whole report", func(t *testing.T) {
		store := reports.NewPostgresStore(app, 0)
		start := time.Now()
		got, err := store.Summary(busiest, &reports.Filter{From: now.AddDate(0, 0, -90), To: now})
		require.NoError(t, err)
		elapsed := time.Since(start)
		t.Logf("a 90-day report for the busiest tenant: %s", elapsed)
		// Wall-clock, so opt-in: a busy CI agent fails it for no fault of the report.
		if _, ok := os.LookupEnv("SLOP_HAUS_PERF_BUDGETS"); ok {
			assert.Less(t, elapsed, 2*time.Second)
		}
		assert.NotEmpty(t, got.States)
	})

	// The check above must be able to fail: without the index it exists to
	// find, the same plan is reported as a problem.
	t.Run("a dropped index is caught", func(t *testing.T) {
		c := cases(30)[2] // lead time: leans on the by-status index
		_, err := owner.Global().Exec(context.Background(), `DROP INDEX ticket_status_changes_by_status_time`)
		require.NoError(t, err)
		p, _ := explain(t, busiest, app, c.query, c.args...)
		assert.NotEmpty(t, planProblems(p, c.indexes...))
	})
}
