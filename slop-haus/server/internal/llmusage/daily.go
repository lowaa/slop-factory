package llmusage

import (
	"context"
	"fmt"

	usagelib "github.com/lowaa/slop-factory/go-llm-usage"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// addDaily adds what a report added to the transcript's totals to
// llm_usage_daily, on the day each record ended. It runs in the report's
// transaction, so a retried or rolled back report never counts twice.
//
// A report from a reporter with a known over-count (Transcript.Unreliable)
// is added to the day's flagged row, so a day holds the flagged and the
// reliable usage apart.
//
// The delta is the one go-llm-usage counts in its metrics: the previous
// totals are priced with today's table, and totals that shrank add nothing.
func (s *PostgresStore) addDaily(ctx context.Context, q db.Querier, t *Transcript, previous, records []usagelib.Record) error {
	before := make(map[usagelib.RecordKey]*usagelib.Record, len(previous))
	for i := range previous {
		p := previous[i].Clone()
		s.pricer.Estimate(p)
		before[p.Key()] = p
	}
	for i := range records {
		r := &records[i]
		d, err := usagelib.Delta(before[r.Key()], r)
		if err != nil {
			return fmt.Errorf("llm usage delta: %w", err)
		}
		if d.Shrank() || d.IsZero() {
			continue
		}
		unpriced := d.ReportedCostMicros == nil && d.EstimatedCostMicros == nil
		var cost *int64
		if !unpriced {
			c := int64(0)
			if d.ReportedCostMicros != nil {
				c = *d.ReportedCostMicros
			} else {
				c = *d.EstimatedCostMicros
			}
			cost = &c
		}
		unpricedRequests := int64(0)
		unpricedRecords := int64(0)
		if unpriced {
			unpricedRequests, unpricedRecords = d.Requests, 1
		}
		if _, err := q.Exec(ctx, `
			INSERT INTO llm_usage_daily (agent_id, day, provider, model, billing_mode, unreliable, records, unpriced_records,
				requests, unpriced_requests, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
				reasoning_tokens, cost_microdollars)
			VALUES ($1, date_trunc('day', $2::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', $3, $4, $5, $15,
				1, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			ON CONFLICT (tenant_id, day, agent_id, provider, model, billing_mode, unreliable) DO UPDATE SET
				records = llm_usage_daily.records + 1,
				unpriced_records = llm_usage_daily.unpriced_records + EXCLUDED.unpriced_records,
				requests = llm_usage_daily.requests + EXCLUDED.requests,
				unpriced_requests = llm_usage_daily.unpriced_requests + EXCLUDED.unpriced_requests,
				input_tokens = llm_usage_daily.input_tokens + EXCLUDED.input_tokens,
				output_tokens = llm_usage_daily.output_tokens + EXCLUDED.output_tokens,
				cache_read_tokens = llm_usage_daily.cache_read_tokens + EXCLUDED.cache_read_tokens,
				cache_write_tokens = llm_usage_daily.cache_write_tokens + EXCLUDED.cache_write_tokens,
				reasoning_tokens = llm_usage_daily.reasoning_tokens + EXCLUDED.reasoning_tokens,
				cost_microdollars = CASE WHEN llm_usage_daily.cost_microdollars IS NULL AND EXCLUDED.cost_microdollars IS NULL
					THEN NULL ELSE COALESCE(llm_usage_daily.cost_microdollars, 0) + COALESCE(EXCLUDED.cost_microdollars, 0) END`,
			t.AgentID, r.EndedAt, r.Provider, r.Model, string(r.BillingMode), unpricedRecords,
			d.Requests, unpricedRequests, d.InputTokens, d.OutputTokens, d.CacheReadTokens, d.CacheWriteTokens,
			d.ReasoningTokens, cost, t.Unreliable); err != nil {
			return fmt.Errorf("store llm daily usage: %w", err)
		}
	}
	return nil
}
