// The "Preparing" ticket status end to end. tickets.spec.ts already
// covers filing straight to Open; this is the other half: a ticket a human
// is still shaping sits out of the claim queue until they open it,
// and opening itself defers to the same blocked-dependency rule the
// answered-discussion path uses.
import { expect, test } from "./support/fixtures";
import { API_BASE_URL, newSessionID } from "./support/env";
import { credentialAgentViaCodeFlow } from "./support/oauth";
import { openNewTicketForm } from "./support/widgets";

test("a ticket filed as Preparing sits out of the queue until a human opens it", async ({ page, api }) => {
  const title = `prep-flow-${Date.now()}`;

  await page.goto("/");
  await openNewTicketForm(page);
  await page.getByLabel("Title").fill(title);
  await page.getByRole("button", { name: "Preparing", exact: true }).click();

  const preparingColumn = page
    .locator(".board__column")
    .filter({ has: page.getByRole("heading", { name: "Preparing" }) });
  await expect(preparingColumn.getByText(title)).toBeVisible();

  // Not offered to an agent while preparing -- the same claim path
  // blocking.spec.ts's "a blocked ticket is not offered to an agent" uses,
  // just against a different reason for being unclaimable.
  const { accessToken } = await credentialAgentViaCodeFlow(api, `prep-agent-${Date.now()}`);
  const auth = { Authorization: `Bearer ${accessToken}` };
  const claimWhilePreparing = await api.post("/api/claims", { headers: auth, data: {} });
  expect(claimWhilePreparing.status(), await claimWhilePreparing.text()).toBe(200);
  expect((await claimWhilePreparing.json()).ticket, "preparing must not be claimable").toBeNull();

  await page.getByText(title).click();
  await expect(page.getByRole("heading", { name: new RegExp(title) })).toBeVisible();
  await expect(page.getByText("Not in the queue.")).toBeVisible();

  await page.getByRole("button", { name: "Open" }).click();
  await expect(page.getByRole("combobox", { name: "Status" })).toHaveText("open");
  await expect(page.getByText("Not in the queue.")).not.toBeVisible();

  await page.getByRole("link", { name: "← Back to board" }).click();
  const openColumn = page.locator(".board__column").filter({ has: page.getByRole("heading", { name: "Open" }) });
  await expect(openColumn.getByText(title)).toBeVisible();
  await expect(preparingColumn.getByText(title)).not.toBeVisible();

  // Opening put it on the queue for real -- the same agent's claim now picks
  // it up.
  const claimAfterReady = await api.post("/api/claims", { headers: auth, data: {} });
  expect(claimAfterReady.status(), await claimAfterReady.text()).toBe(200);
  expect((await claimAfterReady.json()).ticket).toMatchObject({ title, status: "in_progress" });
});

// Opening defers to the same "unfinished dependencies land on blocked, not
// open" rule the answered-discussion path already uses (decision 2 in the
// plan doc) -- filed and blocked through the API, same as blocking.spec.ts
// sets up its own chains, since the point here is the opening's own outcome, not
// re-proving how blockers are added.
test("Opening lands a preparing ticket on blocked when its dependency isn't finished", async ({ page, api }) => {
  const stamp = Date.now();
  const blockerResp = await api.post("/api/tickets", { data: { title: `prep-blocker-${stamp}` } });
  expect(blockerResp.status(), await blockerResp.text()).toBe(201);
  const blocker = await blockerResp.json();

  const readyTitle = `prep-blocked-${stamp}`;
  const ticketResp = await api.post("/api/tickets", { data: { title: readyTitle, status: "preparing" } });
  expect(ticketResp.status(), await ticketResp.text()).toBe(201);
  const ticket = await ticketResp.json();
  expect(ticket.status).toBe("preparing");

  const blockResp = await api.post(`/api/tickets/${ticket.id}/blockers`, {
    data: { blocker_id: blocker.id },
  });
  expect(blockResp.status(), await blockResp.text()).toBe(200);

  await page.goto("/");
  await page.getByText(readyTitle).click();
  await expect(page.getByRole("heading", { name: new RegExp(readyTitle) })).toBeVisible();

  await page.getByRole("button", { name: "Open" }).click();

  await expect(page.getByRole("combobox", { name: "Status" })).toHaveText("blocked");
  await expect(page.getByText("Not in the queue.")).not.toBeVisible();
});

// The agent's side of a human taking its ticket away, over the real wire:
// the next heartbeat carries the reason once, after which the agent simply
// holds nothing -- and the Stop hook treats it that way, rather than
// sending it back to finish the withdrawn ticket.
test("moving a held ticket to Preparing revokes the agent's lease and tells it once", async ({ api }) => {
  const sessionID = newSessionID();
  const { accessToken } = await credentialAgentViaCodeFlow(api, `revoked-${Date.now()}`);
  const auth = { Authorization: `Bearer ${accessToken}` };
  const created = await api.post("/api/tickets", { data: { title: `revoked-${Date.now()}`, status: "open" } });
  const ticket = await created.json();

  await api.post("/hooks/session-start", { headers: auth, data: { session_id: sessionID, cwd: "/srv/ws/1" } });
  const claim = await api.post("/api/claims", { headers: auth, data: { ticket_id: ticket.id } });
  expect((await claim.json()).ticket.id).toBe(ticket.id);

  const moved = await api.patch(`/api/tickets/${ticket.id}`, { data: { status: "preparing" } });
  expect(moved.status(), await moved.text()).toBe(200);

  const told = await api.post("/api/claims/heartbeat", { headers: auth });
  expect(told.status()).toBe(409);
  expect(await told.text()).toContain(`#${ticket.id} was moved to Preparing by a human -- stop working on it`);

  const after = await api.post("/api/claims/heartbeat", { headers: auth });
  expect(after.status()).toBe(409);
  expect(await after.text()).toContain("no active lease");

  const stop = await api.post("/hooks/stop", { headers: auth, data: { session_id: sessionID, cwd: "/srv/ws/1" } });
  expect(stop.status(), await stop.text()).toBe(200);
  const body = await stop.json();
  // Holding nothing now: let stop, never sent back to the ticket a human
  // just took away.
  expect(body.hookSpecificOutput ?? null).toBeNull();
});

// Ready is a person's call: an agent's credential does not reach it.
test("an agent cannot open a ticket", async ({ api, playwright }) => {
  const { accessToken } = await credentialAgentViaCodeFlow(api, `not-ready-${Date.now()}`);
  const created = await api.post("/api/tickets", { data: { title: `draft-${Date.now()}`, status: "preparing" } });
  const ticket = await created.json();

  const anon = await playwright.request.newContext({ baseURL: API_BASE_URL });
  const resp = await anon.post(`/api/tickets/${ticket.id}/ready`, {
    headers: { Authorization: `Bearer ${accessToken}` },
  });
  expect(resp.status()).toBe(401);
  await anon.dispose();

  const got = await api.get(`/api/tickets/${ticket.id}`);
  expect((await got.json()).status).toBe("preparing");
});
