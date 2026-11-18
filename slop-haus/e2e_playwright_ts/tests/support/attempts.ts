import * as db from "@/support/db";

// Usage the way an agent's report leaves it for one attempt of a ticket
// (the same row shape reports.spec.ts records).
export async function recordAttemptUsage(opts: {
  key: string;
  ticket: number;
  attempt: number;
  costMicros: number;
}): Promise<void> {
  await db.execute(
    `INSERT INTO llm_usage (tenant_id, agent_id, transcript_key, harness, session_id, scope_hint, scope, ticket_id, attempt_number, provider, model,
       billing_mode, requests, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens,
       estimated_cost_microdollars, price_version, unpriced, started_at, ended_at)
     SELECT (SELECT tenant_id FROM tickets WHERE id = $2), gen_random_uuid(), $1, 'pi', 's', 'main', 'ticket', $2, $3, 'anthropic', 'claude-opus',
            'subscription', 1, 100, 0, 0, 0, 0, $4, 'v', false, now() - interval '5 hours', now() - interval '4 hours'`,
    [opts.key, opts.ticket, opts.attempt, opts.costMicros],
  );
}
