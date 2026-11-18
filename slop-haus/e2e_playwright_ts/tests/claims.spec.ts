import { expect, test } from "./support/fixtures";
import { newTicket, newAgent } from "./support/api_helpers";
import { API_BASE_URL } from "./support/env";

// The claim path through real HTTP, against a real Postgres. The unit
// suite covers the algorithm in depth; what this adds is that the wiring
// holds -- headers, status codes, and the shapes the eventual MCP tools
// and hook endpoints will be built on.

test("an agent claims a ticket and heartbeats it", async ({ api }) => {
  const agent = await newAgent(api, `builder-${Date.now()}`);
  const ticket = await newTicket(api, "Claimable work", { paths: ["server/internal/lease/"] });

  const claim = await api.post("/api/claims", { headers: agent.auth, data: {} });
  expect(claim.status(), await claim.text()).toBe(200);
  const claimed = await claim.json();
  expect(claimed.ticket.id).toBe(ticket.id);
  expect(claimed.ticket.status).toBe("in_progress");
  expect(claimed.blocked).toBeNull();

  const beat = await api.post("/api/claims/heartbeat", { headers: agent.auth });
  expect(beat.status(), await beat.text()).toBe(204);

  // The close-out is the MCP ticket_finish tool's; the REST route that
  // skipped its rules is gone.
  for (const path of ["done", "review", "scope", "scope/narrow"]) {
    const gone = await api.post(`/api/claims/${path}`, { headers: agent.auth, data: {} });
    expect([404, 405], `${path}: ${await gone.text()}`).toContain(gone.status());
  }
  const held = await api.get(`/api/tickets/${ticket.id}`);
  expect((await held.json()).status).toBe("in_progress");
});

// The point of the whole design: a second agent must not be handed work
// whose paths collide with what is already held, and must be told why
// rather than seeing an empty queue.
test("a conflicting scope is refused and names its holder", async ({ api }) => {
  const holder = await newAgent(api, `holder-${Date.now()}`);
  const other = await newAgent(api, `other-${Date.now()}`);

  const held = await newTicket(api, "Holds the directory", { paths: ["server/internal/core/"] });
  const contended = await newTicket(api, "Wants a file inside it", {
    paths: ["server/internal/core/lease.go"],
  });

  const first = await api.post("/api/claims", {
    headers: holder.auth,
    data: { ticket_id: held.id },
  });
  expect((await first.json()).ticket.id).toBe(held.id);

  const second = await api.post("/api/claims", {
    headers: other.auth,
    data: { ticket_id: contended.id },
  });
  expect(second.status(), await second.text()).toBe(200);
  const blocked = await second.json();
  expect(blocked.ticket).toBeNull();
  expect(blocked.blocked).toMatchObject({
    ticket_id: contended.id,
    conflicting_ticket_id: held.id,
    conflicting_path: "server/internal/core/",
    holder_agent_id: holder.id,
  });
});

test("a disjoint scope is granted while another is held", async ({ api }) => {
  const holder = await newAgent(api, `holder-${Date.now()}`);
  const other = await newAgent(api, `other-${Date.now()}`);

  await newTicket(api, "Server work", { paths: ["server/"] });
  const free = await newTicket(api, "Frontend work", { paths: ["frontend/"] });

  await api.post("/api/claims", { headers: holder.auth, data: {} });

  const second = await api.post("/api/claims", {
    headers: other.auth,
    data: { ticket_id: free.id },
  });
  expect((await second.json()).ticket.id).toBe(free.id);
});

test("the integration lock is exclusive", async ({ api }) => {
  const first = await newAgent(api, `merger-${Date.now()}`);
  const second = await newAgent(api, `waiter-${Date.now()}`);

  await newTicket(api, "Merge one", { paths: ["a/"] });
  await newTicket(api, "Merge two", { paths: ["b/"] });
  await api.post("/api/claims", { headers: first.auth, data: {} });
  await api.post("/api/claims", { headers: second.auth, data: {} });

  expect((await api.post("/api/integration_lock", { headers: first.auth })).status()).toBe(204);
  expect((await api.post("/api/integration_lock", { headers: second.auth })).status()).toBe(409);

  expect((await api.delete("/api/integration_lock", { headers: first.auth })).status()).toBe(204);
  expect((await api.post("/api/integration_lock", { headers: second.auth })).status()).toBe(204);
});

// CreateAgent's own response, and every listing, carries no credential at
// all any more (server/internal/agent/handler.go's AgentBody doc comment)
// -- an agent's only tokens live behind the OAuth grant support/oauth.ts's
// device flow mints, never in this JSON.
test("creating and listing an agent carries no credential", async ({ api }) => {
  const name = `tokenless-${Date.now()}`;
  const resp = await api.post("/api/agents", { data: { name } });
  expect(resp.status(), await resp.text()).toBe(201);
  const created = await resp.json();
  expect(created.token, "creation must not return a token any more").toBeUndefined();

  const listed = await api.get("/api/agents");
  const { agents } = await listed.json();
  const mine = agents.find((a: { id: string }) => a.id === created.id);
  expect(mine).toBeTruthy();
  expect(mine.token, "a listing must never carry a token").toBeUndefined();
});

test("heartbeating without a lease is a conflict, not a crash", async ({ api }) => {
  const agent = await newAgent(api, `idle-${Date.now()}`);

  const beat = await api.post("/api/claims/heartbeat", { headers: agent.auth });
  expect(beat.status(), await beat.text()).toBe(409);
});

test("a claim with no credential is rejected", async ({ api }) => {
  const resp = await api.post("/api/claims", { data: {} });
  expect(resp.status()).toBe(401);
});

// The whole point of the token: knowing an agent's id is not enough to act
// as it. Before auth existed, this request succeeded.
test("knowing an agent's id does not let you act as it", async ({ api }) => {
  const victim = await newAgent(api, `victim-${Date.now()}`);
  await newTicket(api, "Impersonation target", { paths: ["server/"] });

  const resp = await api.post("/api/claims", {
    headers: { "X-Agent-Id": victim.id },
    data: {},
  });
  expect(resp.status(), await resp.text()).toBe(401);
});

// Registering an agent mints an agent token. Leaving that route open would
// make every other token check decorative: anyone able to reach the API
// could issue themselves a valid identity and then claim, comment and
// complete as it. It is authn.RequireUser() now, not an admin token, so
// the check is against a genuinely unauthenticated context rather than
// `api` (which is always signed in -- see support/fixtures.ts).
test("minting an agent requires a signed-in user", async ({ playwright }) => {
  const anon = await playwright.request.newContext({ baseURL: API_BASE_URL });
  const resp = await anon.post("/api/agents", { data: { name: `unauthorized-${Date.now()}` } });
  expect(resp.status(), await resp.text()).toBe(401);
  await anon.dispose();
});

// Disabling an agent force-releases its lease, so an open route there is a
// denial of service against the fleet.
test("disabling an agent requires a signed-in user", async ({ api, playwright }) => {
  const agent = await newAgent(api, `target-${Date.now()}`);

  const anon = await playwright.request.newContext({ baseURL: API_BASE_URL });
  const resp = await anon.patch(`/api/agents/${agent.id}`, { data: { disabled: true } });
  expect(resp.status(), await resp.text()).toBe(401);
  await anon.dispose();

  const allowed = await api.patch(`/api/agents/${agent.id}`, { data: { disabled: true } });
  expect(allowed.status(), await allowed.text()).toBe(200);
});

// An agent's own token is not a user session -- otherwise any agent could
// mint more identities or disable its peers.
test("an agent token cannot mint another agent", async ({ api }) => {
  const agent = await newAgent(api, `climber-${Date.now()}`);

  const resp = await api.post("/api/agents", {
    headers: agent.auth,
    data: { name: `escalated-${Date.now()}` },
  });
  expect(resp.status(), await resp.text()).toBe(401);
});
