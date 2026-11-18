import type { APIRequestContext, Page } from "@playwright/test";

import { expect, test } from "./support/fixtures";
import { newAgent, newTicket, type TestAgent } from "./support/api_helpers";
import { newSessionID } from "./support/env";

// The fleet's on/off switch, end to end: the top bar writes the setting,
// and the two places that read it -- the claim path and the Stop hook --
// change behaviour because of it.
//
// The unit suites cover each half against a fake. What only this one shows
// is that the browser's write and the agent loop's read are talking about
// the same row: a toggle wired to a setting nothing consults looks
// completely correct on screen.

function toggle(page: Page) {
  return page.getByRole("switch", { name: "Ticket processing" });
}

async function stopHook(api: APIRequestContext, agent: TestAgent, sessionID: string) {
  const resp = await api.post("/hooks/stop", {
    headers: agent.auth,
    data: { session_id: sessionID, cwd: "/srv/ws/1" },
  });
  expect(resp.status(), await resp.text()).toBe(200);
  return resp.json();
}

test("the fleet is processing by default, with nothing stored", async ({ page, api }) => {
  await page.goto("/");

  await expect(toggle(page)).toHaveAttribute("aria-checked", "true");
  await expect(toggle(page)).toContainText("Processing");

  // Nothing is seeded: the default lives in Go and a missing row means it,
  // so a fresh database and a reset one behave identically.
  const resp = await api.get("/api/settings/ticket_processing_enabled");
  expect(resp.status(), await resp.text()).toBe(200);
  const setting = await resp.json();
  expect(setting.bool_val).toBe(true);
  expect(setting.is_default).toBe(true);
});

// The whole point of the ticket: agents stop draining the queue.
test("pausing from the top bar refuses claims and lets an idle agent stop", async ({ page, api }) => {
  const agent = await newAgent(api, `pausable-${Date.now()}`);
  const ticket = await newTicket(api, "Should stay in the queue", { paths: ["server/internal/paused/"] });
  const sessionID = newSessionID();
  await api.post("/hooks/session-start", {
    headers: agent.auth,
    data: { session_id: sessionID, cwd: "/srv/ws/1" },
  });

  // The Stop hook never sent idle agents back for work, and no longer reads the switch.
  const before = await stopHook(api, agent, sessionID);
  expect(before.hookSpecificOutput ?? null, "an idle agent is allowed to stop").toBeNull();

  // The toggle lives in the header on every screen (see app.tsx), not
  // behind the Agents tab -- `page` is already signed in (see
  // support/fixtures.ts), so there's nothing left to authenticate here.
  await page.goto("/");
  await toggle(page).click();
  await expect(toggle(page)).toHaveAttribute("aria-checked", "false");
  await expect(toggle(page)).toContainText("Paused");

  // The claim path refuses outright -- 409, a valid request against a
  // server state that forbids it, not a 500 an agent would retry.
  const claim = await api.post("/api/claims", { headers: agent.auth, data: {} });
  expect(claim.status(), await claim.text()).toBe(409);
  expect(await claim.text()).toContain("processing is disabled");

  // And the Stop hook lets the agent stop rather than sending it back.
  const after = await stopHook(api, agent, sessionID);
  expect(after.hookSpecificOutput ?? null, "an idle agent must be allowed to stop").toBeNull();

  // The ticket is untouched, waiting for the switch to come back on.
  const still = await api.get(`/api/tickets/${ticket.id}`);
  expect((await still.json()).status).toBe("open");
});

// Pausing means "start nothing new", not "drop what you are holding". An
// agent that stops mid-ticket takes its working context with it.
test("a paused fleet still tells an agent holding a ticket to finish it", async ({ page, api }) => {
  const agent = await newAgent(api, `holder-${Date.now()}`);
  const ticket = await newTicket(api, "Already in hand", { paths: ["server/internal/held/"] });
  const sessionID = newSessionID();
  await api.post("/hooks/session-start", {
    headers: agent.auth,
    data: { session_id: sessionID, cwd: "/srv/ws/1" },
  });
  const claim = await api.post("/api/claims", { headers: agent.auth, data: { ticket_id: ticket.id } });
  expect(claim.status(), await claim.text()).toBe(200);

  await page.goto("/");
  await toggle(page).click();
  await expect(toggle(page)).toHaveAttribute("aria-checked", "false");

  const decision = await stopHook(api, agent, sessionID);
  expect(decision.hookSpecificOutput.decision).toBe("block");
  expect(decision.hookSpecificOutput.reason).toContain(`#${ticket.id}`);
  expect(decision.hookSpecificOutput.reason).toContain("ticket_finish");
});

test("resuming hands work out again, and the state survives a reload", async ({ page, api }) => {
  const agent = await newAgent(api, `resumable-${Date.now()}`);
  const ticket = await newTicket(api, "Waiting for the switch", { paths: ["server/internal/resumed/"] });

  await page.goto("/");
  await toggle(page).click();
  await expect(toggle(page)).toHaveAttribute("aria-checked", "false");

  // The switch is server state, not a browser preference.
  await page.reload();
  await expect(toggle(page)).toHaveAttribute("aria-checked", "false");

  await toggle(page).click();
  await expect(toggle(page)).toHaveAttribute("aria-checked", "true");

  const claim = await api.post("/api/claims", { headers: agent.auth, data: {} });
  expect(claim.status(), await claim.text()).toBe(200);
  expect((await claim.json()).ticket.id).toBe(ticket.id);
});

// Reading and writing the switch are both authn.RequireUser() now (see
// settings/routes.go's doc comment: "There is no separate admin token --
// that concept belongs to the single-tenant server this replaced") -- so
// the old two-tier "readable without a token, not writable without one"
// boundary collapsed into a single one: signed in or not reachable at all.
test("the switch is not reachable at all without signing in", async ({ anonPage }) => {
  await anonPage.goto("/");
  await expect(anonPage.getByRole("heading", { name: "Sign in" })).toBeVisible();
});
