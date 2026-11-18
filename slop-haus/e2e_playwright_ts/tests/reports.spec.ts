// The Reports tab end to end: tickets moved through statuses through the
// API show up as throughput and as counts of where tickets are now. Only
// figures that do not depend on how long anything took are asserted -- a
// duration here would be seconds, and a test cannot wait for a real one.
import type { APIRequestContext } from "@playwright/test";
import * as db from "@/support/db";
import { expect, test } from "./support/fixtures";
import { newTicket } from "./support/api_helpers";
import { chooseFromDropdown, openHeaderTab } from "./support/widgets";

async function finish(api: APIRequestContext, id: number) {
  const resp = await api.patch(`/api/tickets/${id}`, { data: { status: "done" } });
  expect(resp.status(), await resp.text()).toBe(200);
}

// Two done, one open, one preparing.
async function seedBoard(api: APIRequestContext) {
  const stamp = Date.now();
  const first = await newTicket(api, `report-done-1-${stamp}`, { status: "open" });
  const second = await newTicket(api, `report-done-2-${stamp}`, { status: "open" });
  await finish(api, first.id);
  await finish(api, second.id);
  const waiting = await newTicket(api, `report-open-${stamp}`, { status: "open" });
  await newTicket(api, `report-preparing-${stamp}`, { status: "preparing" });
  return { waiting, stamp };
}

test("the Reports tab counts what was done and where the rest are now", async ({ page, api }) => {
  const { waiting, stamp } = await seedBoard(api);

  await page.goto("/");
  await openHeaderTab(page, "Reports");
  await expect(page).toHaveURL(/\/reports$/);

  await expect(page.getByRole("heading", { name: "Throughput" })).toBeVisible();
  await expect(page.getByText(/done in this period/)).toHaveText("2 tickets done in this period");

  // Discussion is called out among the states even when nothing waited in it.
  await expect(page.getByRole("heading", { name: "Time in each state" })).toBeVisible();
  await expect(page.getByText("waiting on a person")).toBeVisible();

  await expect(page.getByRole("button", { name: /^Open\s*1$/ })).toBeVisible();
  await expect(page.getByRole("button", { name: /^Preparing\s*1$/ })).toBeVisible();

  // Opening a status lists its tickets, each a link into the ticket.
  await page.getByRole("button", { name: /^Open\s*1$/ }).click();
  await expect(page.getByRole("link", { name: new RegExp(`report-open-${stamp}`) })).toBeVisible();
  await page.getByRole("link", { name: new RegExp(`report-open-${stamp}`) }).click();
  await expect(page.getByRole("heading", { name: new RegExp(`report-open-${stamp}`) })).toBeVisible();
  await expect(page).toHaveURL(new RegExp(`/tickets/${waiting.id}\\?.*from=reports`));
  await page.getByRole("link", { name: "← Back to reports" }).click();
  await expect(page.getByRole("heading", { name: "Throughput" })).toBeVisible();
});

test("the period and priority are in the address", async ({ page, api }) => {
  await seedBoard(api);

  await page.goto("/reports");
  await page.getByRole("button", { name: "7 days" }).click();
  await expect(page).toHaveURL(/[?&]days=7/);
  await chooseFromDropdown(page, "Priority", "urgent");
  await expect(page).toHaveURL(/[?&]priority=urgent/);

  // No ticket is urgent, so nothing was done in it -- and a filtered
  // report is still a report, not an error.
  await expect(page.getByText(/Nothing was finished/)).toBeVisible();

  await page.reload();
  await expect(page.getByRole("button", { name: "7 days" })).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByRole("combobox", { name: "Priority" })).toHaveText(/urgent/);
});

test("a board whose history starts today says so, and one with older history does not", async ({ page, api }) => {
  const { stamp } = await seedBoard(api);

  await page.goto("/reports");
  const today = new Date().toISOString().slice(0, 10);
  await expect(page.getByTestId("history-note")).toContainText(`No history before ${today}`);

  // One ticket's changes were recorded before the 90-day period began; the
  // other's still fall inside it.
  await db.execute(
    `UPDATE ticket_status_changes c SET changed_at = changed_at - interval '100 days'
       FROM tickets t WHERE t.tenant_id = c.tenant_id AND t.id = c.ticket_id AND t.title = $1`,
    [`report-done-1-${stamp}`],
  );
  await page.goto("/reports?days=90");
  await expect(page.getByText(/done in this period/)).toHaveText("1 ticket done in this period");
  await expect(page.getByTestId("history-note")).toHaveCount(0);
});

// Usage the way an agent's report leaves it: one row per transcript and
// model, ended a few hours ago, in the tenant of the ticket named.
async function seedUsage(rows: {
  key: string;
  ticket: number | null;
  mode: "metered" | "subscription";
  requests: number;
  inputTokens: number;
  cost: number | null;
  tool?: { category: string; calls: number; errors: number };
}[]) {
  for (const r of rows) {
    await db.execute(
      `INSERT INTO llm_usage (tenant_id, agent_id, transcript_key, harness, session_id, scope_hint, scope, ticket_id, attempt_number, provider, model,
         billing_mode, requests, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens,
         estimated_cost_microdollars, price_version, unpriced, started_at, ended_at)
       SELECT (SELECT tenant_id FROM tickets LIMIT 1), gen_random_uuid(), $1, 'pi', 's', 'main',
              CASE WHEN $2::bigint IS NULL THEN 'overhead' ELSE 'ticket' END, $2, CASE WHEN $2::bigint IS NOT NULL THEN 1 END, 'anthropic', 'claude-opus', $3, $4, $5, 0, 0, 0, 0,
              $6, 'v', $6::bigint IS NULL, now() - interval '5 hours', now() - interval '4 hours'`,
      [r.key, r.ticket, r.mode, r.requests, r.inputTokens, r.cost],
    );
    await db.execute(
      `INSERT INTO llm_usage_daily (tenant_id, day, agent_id, provider, model, billing_mode, records, unpriced_records, requests,
         unpriced_requests, input_tokens, cost_microdollars)
       SELECT tenant_id, date_trunc('day', ended_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', agent_id, provider, model, billing_mode,
              1, CASE WHEN unpriced THEN 1 ELSE 0 END, requests, CASE WHEN unpriced THEN requests ELSE 0 END, input_tokens,
              estimated_cost_microdollars
         FROM llm_usage WHERE transcript_key = $1`,
      [r.key],
    );
    if (r.tool) {
      await db.execute(
        `INSERT INTO llm_tool_usage (tenant_id, agent_id, transcript_key, provider, model, harness, billing_mode, category, tool, calls, errors, duration_ms, result_bytes, ended_at)
         SELECT tenant_id, agent_id, transcript_key, provider, model, harness, billing_mode, $2, 'Tool', $3, $4, 1500, 0, ended_at
           FROM llm_usage WHERE transcript_key = $1`,
        [r.key, r.tool.category, r.tool.calls, r.tool.errors],
      );
    }
  }
}

test("the LLM usage section shows cost per mode, per done ticket, and usage not tied to a ticket", async ({ page, api }) => {
  const stamp = Date.now();
  const doneTicket = await newTicket(api, `usage-done-${stamp}`, { status: "open" });
  await finish(api, doneTicket.id);
  await seedUsage([
    { key: `u1-${stamp}`, ticket: doneTicket.id, mode: "subscription", requests: 10, inputTokens: 2000, cost: 3_000_000, tool: { category: "bash", calls: 40, errors: 2 } },
    { key: `u2-${stamp}`, ticket: null, mode: "metered", requests: 4, inputTokens: 500, cost: 1_000_000 },
    { key: `u3-${stamp}`, ticket: null, mode: "subscription", requests: 3, inputTokens: 300, cost: null },
  ]);

  await page.goto("/reports");

  const section = page.getByRole("region", { name: "LLM usage" });
  await expect(section.getByRole("heading", { name: "LLM usage" })).toBeVisible();
  // Each billing mode is its own figure; the subscription one is an estimate.
  await expect(section.getByText("cost is estimated")).toBeVisible();
  await expect(section.getByText("cost is spent")).toBeVisible();
  await expect(section.getByText("$3.00 estimated").first()).toBeVisible();
  await expect(section.getByText("$1.00 spent").first()).toBeVisible();
  await expect(section.getByText("$4.00")).toHaveCount(0);
  // The unpriced transcript adds no cost and says so.
  await expect(section.getByText(/1 record, 3 requests, with no price/)).toBeVisible();
  // Usage with no ticket is its own line, per mode.
  await expect(section.getByTestId("unattributed")).toHaveCount(2);
  // The done ticket's cost sits with its lead time, as a link into the ticket.
  await expect(section.getByTestId("done-tickets")).toContainText("Subscription: 1 done ticket with usage");
  await section.getByRole("link", { name: new RegExp(`usage-done-${stamp}`) }).click();
  await expect(page).toHaveURL(new RegExp(`/tickets/${doneTicket.id}\\?.*from=reports`));
  await page.getByRole("link", { name: "← Back to reports" }).click();

  const tools = section.getByRole("row", { name: /bash/ });
  await expect(tools).toContainText("40");
  await expect(tools).toContainText("5.0%");
});

test("the LLM usage section follows the period and says when there is none", async ({ page, api }) => {
  await seedBoard(api);

  await page.goto("/reports");
  await expect(page.getByText("No LLM usage was reported in this period.")).toBeVisible();

  await seedUsage([{ key: `old-${Date.now()}`, ticket: null, mode: "subscription", requests: 1, inputTokens: 1, cost: 1 }]);
  await db.execute(`UPDATE llm_usage SET started_at = now() - interval '21 days', ended_at = now() - interval '20 days'`);
  await db.execute(`UPDATE llm_tool_usage SET ended_at = now() - interval '20 days'`);
  await db.execute(`UPDATE llm_usage_daily SET day = date_trunc('day', now() AT TIME ZONE 'UTC' - interval '20 days') AT TIME ZONE 'UTC'`);

  await page.getByRole("button", { name: "7 days" }).click();
  await expect(page.getByText("No LLM usage was reported in this period.")).toBeVisible();
  await page.getByRole("button", { name: "30 days" }).click();
  await expect(page.getByText("cost is estimated")).toBeVisible();
});
