import { expect, type APIRequestContext } from "@playwright/test";

import { execute } from "@/support/db";

// seedPendingProposals leaves `count` proposals pending, one per new ticket.
//
// The rows are written straight to pending_actions: over the wire a proposal
// takes a console agent's MCP session per proposal, which is a lot of set-up
// for a test that is about how the page lays a long list out. The unique index
// allows one pending proposal per ticket and kind, hence a ticket each. This
// connection is the owner's, with no tenant set, so the rows name the agent's
// tenant rather than relying on the column's current_tenant() default.
export async function seedPendingProposals(api: APIRequestContext, count: number): Promise<void> {
  const stamp = Date.now();
  const agentResp = await api.post("/api/agents", { data: { name: `proposer-${stamp}` } });
  expect(agentResp.status(), await agentResp.text()).toBe(201);
  const agent = await agentResp.json();

  const ids = await Promise.all(
    Array.from({ length: count }, async (_, i) => {
      const resp = await api.post("/api/tickets", { data: { title: `proposal ${i} ${stamp}` } });
      expect(resp.status(), await resp.text()).toBe(201);
      return (await resp.json()).id as number;
    }),
  );

  // base_status and base_revision copy the ticket as it is now, so none of the
  // proposals is stale.
  await execute(
    `INSERT INTO pending_actions (tenant_id, id, ticket_id, agent_id, kind, payload, base_status, base_revision, expires_at)
     SELECT a.tenant_id, gen_random_uuid(), t.id, a.id, 'status', '{"to": "done"}'::jsonb, t.status, t.revision, now() + interval '1 day'
     FROM agents a JOIN tickets t ON t.tenant_id = a.tenant_id
     WHERE a.id = $1 AND t.id = ANY($2::bigint[])`,
    [agent.id, ids],
  );
}
