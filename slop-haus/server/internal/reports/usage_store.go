package reports

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// A row's cost is what the harness reported, else the server's estimate; a
// row with neither is unpriced.
const (
	usageCost     = `COALESCE(reported_cost_microdollars, estimated_cost_microdollars)`
	usageUnpriced = `reported_cost_microdollars IS NULL AND estimated_cost_microdollars IS NULL`

	// usageSums is the column list every aggregate selects, in the order
	// scanSums reads them.
	usageSums = `count(*) AS records, sum(requests)::bigint AS requests,
       sum(input_tokens)::bigint AS input_tokens, sum(output_tokens)::bigint AS output_tokens,
       sum(cache_read_tokens)::bigint AS cache_read_tokens, sum(cache_write_tokens)::bigint AS cache_write_tokens,
       sum(reasoning_tokens)::bigint AS reasoning_tokens, sum(` + usageCost + `)::bigint AS cost,
       count(*) FILTER (WHERE ` + usageUnpriced + `) AS unpriced_records,
       COALESCE(sum(requests) FILTER (WHERE ` + usageUnpriced + `), 0)::bigint AS unpriced_requests`

	// usageSumsOf reads usageSums back from a CTE named by alias.
	usageSumsOf = `%[1]s.records, %[1]s.requests, %[1]s.input_tokens, %[1]s.output_tokens, %[1]s.cache_read_tokens,
       %[1]s.cache_write_tokens, %[1]s.reasoning_tokens, %[1]s.cost, %[1]s.unpriced_records, %[1]s.unpriced_requests`

	// $1 and $2 are the period [from, to), as ended_at: an index range scan
	// on (tenant_id, ended_at) in every query below.
	usageWindow = `ended_at >= $1 AND ended_at < $2`

	// $3 is whether to count rows flagged unreliable (the unreliable column), which
	// are left out by default. llm_tool_usage has no flag: call counts were
	// never inflated.
	usageRows = usageWindow + ` AND ($3 OR NOT unreliable)`

	// dailySums is usageSums over llm_usage_daily, whose rows are already
	// per-report deltas summed by day. A day is in the period when it starts
	// before the period ends and ends after it starts, so the period's first
	// partial day counts whole rather than being lost.
	dailySums = `sum(records)::bigint AS records, sum(requests)::bigint AS requests,
       sum(input_tokens)::bigint AS input_tokens, sum(output_tokens)::bigint AS output_tokens,
       sum(cache_read_tokens)::bigint AS cache_read_tokens, sum(cache_write_tokens)::bigint AS cache_write_tokens,
       sum(reasoning_tokens)::bigint AS reasoning_tokens, sum(cost_microdollars)::bigint AS cost,
       sum(unpriced_records)::bigint AS unpriced_records, sum(unpriced_requests)::bigint AS unpriced_requests`
	dailyWindow = `day > $1::timestamptz - interval '1 day' AND day < $2 AND ($3 OR NOT unreliable)`
)

var (
	queryUsageTotals = `
SELECT billing_mode, ticket_id IS NOT NULL, count(DISTINCT (agent_id, transcript_key)), ` + usageSums + `
  FROM llm_usage WHERE ` + usageRows + `
 GROUP BY 1, 2 ORDER BY 1, 2`

	queryGuidanceTotals = `
SELECT billing_mode, count(DISTINCT (agent_id, transcript_key)), ` + usageSums + `
  FROM llm_guidance_usage WHERE ` + usageRows + `
 GROUP BY 1 ORDER BY 1`

	queryUsageUnreliable = `
SELECT (SELECT count(*) FROM llm_usage WHERE ` + usageWindow + ` AND unreliable)
     + (SELECT count(*) FROM llm_guidance_usage WHERE ` + usageWindow + ` AND unreliable)`

	queryUsageDays = `
SELECT day, billing_mode, ` + dailySums + `
  FROM llm_usage_daily WHERE ` + dailyWindow + `
 GROUP BY 1, 2 ORDER BY 1, 2`

	// The agent's name is looked up after grouping, once per group.
	queryUsageAgents = `
WITH g AS (
    SELECT agent_id, billing_mode, ` + dailySums + `
      FROM llm_usage_daily WHERE ` + dailyWindow + `
     GROUP BY 1, 2
     ORDER BY sum(input_tokens + output_tokens + cache_read_tokens + cache_write_tokens) DESC, 1, 2
     LIMIT $4)
SELECT g.agent_id, COALESCE(a.name, ''), g.billing_mode, ` + fmt.Sprintf(usageSumsOf, "g") + `
  FROM g LEFT JOIN agents a ON a.id = g.agent_id
 ORDER BY g.input_tokens + g.output_tokens + g.cache_read_tokens + g.cache_write_tokens DESC, g.agent_id, g.billing_mode`

	queryUsageModels = `
SELECT provider, model, billing_mode, ` + dailySums + `
  FROM llm_usage_daily WHERE ` + dailyWindow + `
 GROUP BY 1, 2, 3
 ORDER BY sum(input_tokens + output_tokens + cache_read_tokens + cache_write_tokens) DESC, 1, 2, 3
 LIMIT $4`

	// A tool row carries its record's ended_at (llm_tool_usage.ended_at), so the
	// period is an index range on the tool table itself, not a join through
	// llm_usage, which let the planner scan every tool row there is.
	queryUsageTools = `
SELECT category, sum(calls)::bigint, sum(errors)::bigint, sum(duration_ms)::bigint
  FROM llm_tool_usage WHERE ` + usageWindow + `
 GROUP BY 1 ORDER BY 2 DESC, 1`

	queryUsageTicketCount = `
SELECT count(DISTINCT ticket_id) FROM (
    SELECT ticket_id FROM llm_usage WHERE ` + usageRows + ` AND ticket_id IS NOT NULL
    UNION ALL
    SELECT ticket_id FROM llm_guidance_usage WHERE ` + usageRows + `) u`

	// Each ticket's usage per billing mode, then the median over done
	// tickets. Priced and unpriced rows of one ticket sum to a cost that
	// understates it, so the ticket is counted as unpriced.
	queryUsageDone = `
WITH per AS (
    SELECT ticket_id, billing_mode, sum(` + usageCost + `)::bigint AS cost,
           count(*) FILTER (WHERE ` + usageUnpriced + `) AS unpriced
      FROM llm_usage WHERE ` + usageRows + ` AND ticket_id IS NOT NULL
     GROUP BY 1, 2)
SELECT p.billing_mode, count(*), sum(p.cost)::bigint,
       percentile_disc(0.5) WITHIN GROUP (ORDER BY p.cost), count(*) FILTER (WHERE p.unpriced > 0)
  FROM per p
  JOIN tickets t ON t.id = p.ticket_id
 WHERE t.status = ` + ticket.SQLStatusDone + `
 GROUP BY 1 ORDER BY 1`

	// Tickets are ranked by tokens across billing modes (a cost cannot be
	// ranked across two), then each page's tickets are expanded to a row per
	// mode. %[1]s is the ticket filter, a subquery rather than a join so the
	// usage columns keep their meaning; $1 and $2 are the period, the filter
	// numbers its arguments from $3, and %[2]s is the keyset condition on a
	// page after the first.
	queryUsageTicketsFiltered = `
WITH per AS (
    SELECT ticket_id, billing_mode, ` + usageSums + `
      FROM llm_usage
     WHERE ` + usageRows + ` AND ticket_id IN (SELECT t.id FROM tickets t WHERE %[1]s)
     GROUP BY 1, 2),
gper AS (
    SELECT ticket_id, billing_mode, ` + usageSums + `
      FROM llm_guidance_usage
     WHERE ` + usageRows + ` AND ticket_id IN (SELECT t.id FROM tickets t WHERE %[1]s)
     GROUP BY 1, 2),
tok AS (
    SELECT ticket_id, sum(tokens) AS tokens FROM (
        SELECT ticket_id, input_tokens + output_tokens + cache_read_tokens + cache_write_tokens AS tokens FROM per
        UNION ALL
        SELECT ticket_id, 0 FROM gper) u
     GROUP BY 1),
ranked AS (
    SELECT ticket_id, tokens FROM tok
    %[2]s
     ORDER BY 2 DESC, 1 LIMIT %[3]s),
both_kinds AS (
    SELECT 'worker' AS kind, * FROM per
    UNION ALL
    SELECT 'guidance', * FROM gper)
SELECT p.ticket_id, t.title, t.status, r.tokens, p.kind, p.billing_mode, ` + fmt.Sprintf(usageSumsOf, "p") + `
  FROM ranked r
  JOIN both_kinds p USING (ticket_id)
  JOIN tickets t ON t.id = p.ticket_id
 ORDER BY r.tokens DESC, p.ticket_id, p.kind DESC, p.billing_mode`

	queryUsageTicketsTotal = `
SELECT count(DISTINCT ticket_id) FROM (
    SELECT ticket_id FROM llm_usage WHERE ` + usageRows + `
    UNION ALL
    SELECT ticket_id FROM llm_guidance_usage WHERE ` + usageRows + `) u
 WHERE ticket_id IN (SELECT t.id FROM tickets t WHERE %s)`
)

func (s *PostgresStore) LLMUsage(ctx context.Context, f *UsageFilter) (*UsageReport, error) {
	out := &UsageReport{From: f.From, To: f.To}
	err := s.readTx(ctx, func(q db.Querier) error {
		var err error
		if out.Totals, out.Unattributed, err = queryUsageTotalSums(ctx, q, f); err != nil {
			return err
		}
		if out.Guidance, err = queryGuidanceSums(ctx, q, f); err != nil {
			return err
		}
		out.Unattributed = withoutCharged(out.Unattributed, out.Guidance)
		if out.Days, err = queryUsageDaySums(ctx, q, f); err != nil {
			return err
		}
		if out.Agents, err = queryUsageAgentSums(ctx, q, f); err != nil {
			return err
		}
		if out.Models, err = queryUsageModelSums(ctx, q, f); err != nil {
			return err
		}
		if out.Tools, err = queryUsageToolSums(ctx, q, f); err != nil {
			return err
		}
		if out.DoneTickets, err = queryUsageDoneSums(ctx, q, f); err != nil {
			return err
		}
		if err := q.QueryRow(ctx, queryUsageTicketCount, f.From, f.To, f.IncludeUnreliable).Scan(&out.Tickets); err != nil {
			return fmt.Errorf("reports: llm usage tickets: %w", err)
		}
		return q.QueryRow(ctx, queryUsageUnreliable, f.From, f.To).Scan(&out.Unreliable)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// usageTicketsSQL is the page query of the per-ticket list and its
// arguments, and the count of every ticket the filter matches with its own.
func usageTicketsSQL(f *UsageFilter, q *UsageTicketQuery) (page string, pageArgs []any, total string, totalArgs []any) {
	where, args := q.Filter.Predicate(3)
	totalArgs = append([]any{f.From, f.To, f.IncludeUnreliable}, args...)
	// One more than the page, to know whether another follows.
	pageArgs = append(slices.Clone(totalArgs), q.Limit+1)
	limit := fmt.Sprintf("$%d", len(pageArgs))
	after := ""
	if q.After != nil {
		pageArgs = append(pageArgs, q.After.Tokens, q.After.TicketID)
		n := len(pageArgs)
		after = fmt.Sprintf(`WHERE tokens < $%[1]d OR (tokens = $%[1]d AND ticket_id > $%[2]d)`, n-1, n)
	}
	return fmt.Sprintf(queryUsageTicketsFiltered, where, after, limit), pageArgs, fmt.Sprintf(queryUsageTicketsTotal, where), totalArgs
}

func (s *PostgresStore) LLMUsageTickets(ctx context.Context, f *UsageFilter, q *UsageTicketQuery) (*UsageTicketPage, error) {
	page := &UsageTicketPage{Items: []UsageTicket{}}
	pageSQL, pageArgs, totalSQL, totalArgs := usageTicketsSQL(f, q)
	err := s.readTx(ctx, func(tx db.Querier) error {
		if err := tx.QueryRow(ctx, totalSQL, totalArgs...).Scan(&page.Total); err != nil {
			return fmt.Errorf("reports: llm usage tickets: %w", err)
		}
		rows, err := tx.Query(ctx, pageSQL, pageArgs...)
		if err != nil {
			return fmt.Errorf("reports: llm usage tickets: %w", err)
		}
		defer rows.Close()
		var tokens []int64
		for rows.Next() {
			var (
				id, ranked   int64
				title, state string
				kind         string
				sums         UsageSums
			)
			if err := scanUsage(rows, &sums, &id, &title, &state, &ranked, &kind); err != nil {
				return fmt.Errorf("reports: llm usage tickets: %w", err)
			}
			// Rows of one ticket are adjacent.
			if n := len(page.Items); n == 0 || page.Items[n-1].TicketID != id {
				page.Items = append(page.Items, UsageTicket{TicketID: id, Title: title, Status: state})
				tokens = append(tokens, ranked)
			}
			last := &page.Items[len(page.Items)-1]
			if kind == "guidance" {
				last.Guidance = append(last.Guidance, sums)
			} else {
				last.Modes = append(last.Modes, sums)
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(page.Items) > q.Limit {
			page.Items = page.Items[:q.Limit]
			end := q.Limit - 1
			page.Next = &UsageTicketCursor{Tokens: tokens[end], TicketID: page.Items[end].TicketID}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return page, nil
}

// UsageSumsSelect is the aggregate column list over llm_usage-shaped rows
// (llm_usage and llm_guidance_usage share the columns), for other packages'
// per-ticket reads to share the cost rule. Read it back with ScanUsageSums.
const UsageSumsSelect = usageSums

// ScanUsageSums reads a row of a query's own leading columns, then its
// billing mode, then UsageSumsSelect.
func ScanUsageSums(rows pgx.Rows, u *UsageSums, leading ...any) error {
	return scanUsage(rows, u, leading...)
}

// scanUsage reads a row of a query's own leading columns, then its billing
// mode, then the usageSums columns.
func scanUsage(rows pgx.Rows, u *UsageSums, leading ...any) error {
	dest := append(leading, &u.BillingMode, &u.Records, &u.Requests, &u.InputTokens, &u.OutputTokens,
		&u.CacheReadTokens, &u.CacheWriteTokens, &u.ReasoningTokens, &u.CostMicros,
		&u.UnpricedRecords, &u.UnpricedRequests)
	return rows.Scan(dest...)
}

func queryUsageTotalSums(ctx context.Context, q db.Querier, f *UsageFilter) (totals, unattributed []UsageSums, err error) {
	rows, err := q.Query(ctx, queryUsageTotals, f.From, f.To, f.IncludeUnreliable)
	if err != nil {
		return nil, nil, fmt.Errorf("reports: llm usage totals: %w", err)
	}
	defer rows.Close()
	byMode := map[string]int{}
	for rows.Next() {
		var (
			u        UsageSums
			ticketed bool
		)
		dest := []any{&u.BillingMode, &ticketed, &u.Transcripts, &u.Records, &u.Requests, &u.InputTokens, &u.OutputTokens,
			&u.CacheReadTokens, &u.CacheWriteTokens, &u.ReasoningTokens, &u.CostMicros,
			&u.UnpricedRecords, &u.UnpricedRequests}
		if err := rows.Scan(dest...); err != nil {
			return nil, nil, fmt.Errorf("reports: llm usage totals: %w", err)
		}
		if !ticketed {
			unattributed = append(unattributed, u)
		}
		// A transcript has one attribution, so its transcripts are in
		// exactly one of the ticketed and unticketed groups of a mode.
		i, ok := byMode[u.BillingMode]
		if !ok {
			byMode[u.BillingMode] = len(totals)
			totals = append(totals, UsageSums{BillingMode: u.BillingMode})
			i = len(totals) - 1
		}
		totals[i].add(&u)
	}
	return totals, unattributed, rows.Err()
}

func queryGuidanceSums(ctx context.Context, q db.Querier, f *UsageFilter) ([]UsageSums, error) {
	rows, err := q.Query(ctx, queryGuidanceTotals, f.From, f.To, f.IncludeUnreliable)
	if err != nil {
		return nil, fmt.Errorf("reports: llm guidance usage: %w", err)
	}
	defer rows.Close()
	var out []UsageSums
	for rows.Next() {
		var u UsageSums
		dest := []any{&u.BillingMode, &u.Transcripts, &u.Records, &u.Requests, &u.InputTokens, &u.OutputTokens,
			&u.CacheReadTokens, &u.CacheWriteTokens, &u.ReasoningTokens, &u.CostMicros,
			&u.UnpricedRecords, &u.UnpricedRequests}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("reports: llm guidance usage: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// withoutCharged is the overhead with what was charged to tickets as
// guidance taken out, per billing mode.
func withoutCharged(overhead, guidance []UsageSums) []UsageSums {
	out := slices.Clone(overhead)
	for i := range out {
		for j := range guidance {
			if guidance[j].BillingMode == out[i].BillingMode {
				out[i].subtractCharged(&guidance[j])
			}
		}
	}
	return out
}

func queryUsageDaySums(ctx context.Context, q db.Querier, f *UsageFilter) ([]UsageDay, error) {
	rows, err := q.Query(ctx, queryUsageDays, f.From, f.To, f.IncludeUnreliable)
	if err != nil {
		return nil, fmt.Errorf("reports: llm usage days: %w", err)
	}
	defer rows.Close()
	var out []UsageDay
	for rows.Next() {
		var d UsageDay
		if err := scanUsage(rows, &d.UsageSums, &d.Day); err != nil {
			return nil, fmt.Errorf("reports: llm usage days: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func queryUsageAgentSums(ctx context.Context, q db.Querier, f *UsageFilter) ([]UsageAgent, error) {
	rows, err := q.Query(ctx, queryUsageAgents, f.From, f.To, f.IncludeUnreliable, MaxUsageGroups)
	if err != nil {
		return nil, fmt.Errorf("reports: llm usage agents: %w", err)
	}
	defer rows.Close()
	var out []UsageAgent
	for rows.Next() {
		var a UsageAgent
		if err := scanUsage(rows, &a.UsageSums, &a.AgentID, &a.Name); err != nil {
			return nil, fmt.Errorf("reports: llm usage agents: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func queryUsageModelSums(ctx context.Context, q db.Querier, f *UsageFilter) ([]UsageModel, error) {
	rows, err := q.Query(ctx, queryUsageModels, f.From, f.To, f.IncludeUnreliable, MaxUsageGroups)
	if err != nil {
		return nil, fmt.Errorf("reports: llm usage models: %w", err)
	}
	defer rows.Close()
	var out []UsageModel
	for rows.Next() {
		var m UsageModel
		if err := scanUsage(rows, &m.UsageSums, &m.Provider, &m.Model); err != nil {
			return nil, fmt.Errorf("reports: llm usage models: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func queryUsageToolSums(ctx context.Context, q db.Querier, f *UsageFilter) ([]UsageTool, error) {
	rows, err := q.Query(ctx, queryUsageTools, f.From, f.To)
	if err != nil {
		return nil, fmt.Errorf("reports: llm tool usage: %w", err)
	}
	defer rows.Close()
	var out []UsageTool
	for rows.Next() {
		var t UsageTool
		if err := rows.Scan(&t.Category, &t.Calls, &t.Errors, &t.DurationMs); err != nil {
			return nil, fmt.Errorf("reports: llm tool usage: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func queryUsageDoneSums(ctx context.Context, q db.Querier, f *UsageFilter) ([]UsageDoneTickets, error) {
	rows, err := q.Query(ctx, queryUsageDone, f.From, f.To, f.IncludeUnreliable)
	if err != nil {
		return nil, fmt.Errorf("reports: llm usage done tickets: %w", err)
	}
	defer rows.Close()
	var out []UsageDoneTickets
	for rows.Next() {
		var d UsageDoneTickets
		if err := rows.Scan(&d.BillingMode, &d.Tickets, &d.CostMicros, &d.CostP50Micros, &d.UnpricedTickets); err != nil {
			return nil, fmt.Errorf("reports: llm usage done tickets: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
