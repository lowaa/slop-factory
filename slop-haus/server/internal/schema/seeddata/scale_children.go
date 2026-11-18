package seeddata

import (
	"context"
	"fmt"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// seedScaleChildren writes what hangs off the tickets SeedScale just made.
// Everything is placed by ticket id, so the mix is the same at any size.
func seedScaleChildren(ctx context.Context, q db.Querier, sum *ScaleSummary) error {
	run := func(label, sql string, args ...any) (int, error) {
		tag, err := q.Exec(ctx, sql, args...)
		if err != nil {
			return 0, fmt.Errorf("seed scale: %s: %w", label, err)
		}
		return int(tag.RowsAffected()), nil
	}
	var err error

	// Up to four comments on a ticket, by id; the rare word is in one
	// ticket's comments in a hundred and the broad one in a few of every
	// ticket's.
	if sum.Comments, err = run("comments", `
		INSERT INTO ticket_comments (ticket_id, author_agent_id, body, created_at)
		SELECT t.id, a.id,
		       'Comment ' || c || ' on ' || t.id || ' about the `+scaleCommonWord+`'
		         || CASE WHEN t.id % 100 = 13 AND c = 1 THEN ' and a `+scaleSearchWord+`' ELSE '' END,
		       t.created_at + make_interval(secs => c)
		  FROM tickets t
		  CROSS JOIN LATERAL generate_series(1, t.id % 5) c
		  CROSS JOIN LATERAL (SELECT id FROM agents ORDER BY id OFFSET (t.id % $1) LIMIT 1) a`, scaleAgents); err != nil {
		return err
	}

	// A handoff on one ticket in ten.
	if sum.Handoffs, err = run("handoffs", `
		INSERT INTO agent_contexts (ticket_id, attempt_number, agent_id, body, created_at)
		SELECT t.id, 1, a.id, 'Handoff on ' || t.id || ': stopped at the `+scaleCommonWord+`.', t.created_at
		  FROM tickets t
		  CROSS JOIN LATERAL (SELECT id FROM agents ORDER BY id OFFSET (t.id % $1) LIMIT 1) a
		 WHERE t.id % 10 = 0`, scaleAgents); err != nil {
		return err
	}

	// Every blocked ticket waits on the nearest earlier open one, and every
	// seventh also on the blocked one before it, so some blocked tickets
	// hold others up and the blocked list has a rank to page by. Every
	// tenth other ticket has a satisfied link to the done ticket before it,
	// so the graph has edges that are not blocking.
	if sum.Blockers, err = run("blockers", `
		INSERT INTO ticket_deps (ticket_id, depends_on_ticket_id)
		SELECT t.id, d.id
		  FROM tickets t
		  CROSS JOIN LATERAL (SELECT id FROM tickets p WHERE p.status = 'open' AND p.id < t.id ORDER BY p.id DESC LIMIT 1) d
		 WHERE t.status = 'blocked'
		UNION
		SELECT t.id, d.id
		  FROM tickets t
		  CROSS JOIN LATERAL (SELECT id FROM tickets p WHERE p.status = 'blocked' AND p.id < t.id ORDER BY p.id DESC LIMIT 1) d
		 WHERE t.status = 'blocked' AND t.id % 7 = 0
		UNION
		SELECT t.id, d.id
		  FROM tickets t
		  JOIN tickets d ON d.id = t.id - 1
		 WHERE t.id % 10 = 5 AND t.status <> 'blocked' AND d.status = 'done'`); err != nil {
		return err
	}

	// One pending proposal for each ticket a console could act on: answer a
	// discussion, ready a preparing ticket, decide a review.
	if sum.Proposals, err = run("proposals", `
		INSERT INTO pending_actions (id, ticket_id, attempt_number, agent_id, kind, payload, base_status, base_revision,
		                             base_pr_url, base_review_started_at, created_at, expires_at)
		SELECT gen_random_uuid(), t.id, CASE WHEN t.status = 'preparing' THEN NULL ELSE pa.number END, a.id,
		       CASE t.status WHEN 'discussion' THEN 'discussion_answer' WHEN 'preparing' THEN 'ready' ELSE 'review_decision' END,
		       CASE t.status WHEN 'discussion' THEN '{"body":"Go ahead."}'::jsonb
		                     WHEN 'preparing' THEN '{}'::jsonb
		                     ELSE '{"decision":"approve"}'::jsonb END,
		       t.status, t.revision, pa.pr_url, pa.review_started_at, now(), now() + interval '30 days'
		  FROM tickets t `+ticket.AttemptJoin+`
		  CROSS JOIN LATERAL (SELECT id FROM agents WHERE kind = 'console' ORDER BY id OFFSET (t.id % $1) LIMIT 1) a
		 WHERE t.status IN ('discussion', 'preparing', 'review')`, scaleConsoles); err != nil {
		return err
	}

	// LLM usage on every second ticket that was worked on.
	if sum.UsageRows, err = run("usage", `
		INSERT INTO llm_usage (agent_id, transcript_key, harness, session_id, scope_hint, scope, ticket_id, attempt_number,
		                       provider, model, billing_mode, requests, input_tokens, output_tokens,
		                       cache_read_tokens, cache_write_tokens, reasoning_tokens,
		                       estimated_cost_microdollars, price_version, unpriced, started_at, ended_at)
		SELECT a.id, 'scale-' || t.id, 'claude_code', 'session-' || t.id, '', 'ticket', t.id, 1,
		       'anthropic', (ARRAY['model-a','model-b','model-c'])[1 + t.id % 3], 'metered',
		       1 + t.id % 40, 1000 * (1 + t.id % 97), 100 * (1 + t.id % 13), 5000, 500, 0,
		       1000 * (1 + t.id % 50), 'scale', false, t.created_at, t.created_at + interval '10 minutes'
		  FROM tickets t
		  CROSS JOIN LATERAL (SELECT id FROM agents WHERE kind = 'worker' ORDER BY id OFFSET (t.id % $1) LIMIT 1) a
		 WHERE t.id % 2 = 0 AND t.status NOT IN ('preparing', 'parked')`, scaleAgents); err != nil {
		return err
	}

	if _, err = run("usage by day", `
		INSERT INTO llm_usage_daily (agent_id, day, provider, model, billing_mode, records, requests, input_tokens,
		                             output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, cost_microdollars)
		SELECT agent_id, date_trunc('day', ended_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', provider, model, billing_mode,
		       count(*), sum(requests), sum(input_tokens), sum(output_tokens), sum(cache_read_tokens),
		       sum(cache_write_tokens), sum(reasoning_tokens), sum(estimated_cost_microdollars)
		  FROM llm_usage GROUP BY 1, 2, 3, 4, 5`); err != nil {
		return err
	}

	rows, err := q.Query(ctx, `SELECT status, count(*) FROM tickets GROUP BY status`)
	if err != nil {
		return fmt.Errorf("seed scale: count: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return fmt.Errorf("seed scale: count: %w", err)
		}
		sum.ByStatus[status] = n
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	if err := q.QueryRow(ctx, `
		SELECT count(*) FROM tickets t `+ticket.AttemptJoin+`
		 WHERE t.status = 'done' AND NOT t.requires_approval AND pa.approved_at IS NULL AND t.post_reviewed_at IS NULL`).Scan(&sum.AwaitingReview); err != nil {
		return fmt.Errorf("seed scale: count: %w", err)
	}
	return nil
}
