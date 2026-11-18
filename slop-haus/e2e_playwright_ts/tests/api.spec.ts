// Drives slop-haus/server's ticket REST API directly (no UI, no browser),
// the same "hit the backend over real HTTP" role.
// tickets.spec.ts covers the same create-then-view path through the UI.
import { expect, test } from "@/support/fixtures";

test("create then get a ticket round-trips through the API", async ({ api }) => {
  const createResp = await api.post("/api/tickets", {
    data: { title: "Wire up the board", body: "Render columns by status", priority: "high" },
  });
  expect(createResp.status(), await createResp.text()).toBe(201);
  const created = await createResp.json();
  expect(created).toMatchObject({
    title: "Wire up the board",
    body: "Render columns by status",
    priority: "high",
    status: "open",
    depends_on: [],
    created_by_agent_id: null,
  });

  const getResp = await api.get(`/api/tickets/${created.id}`);
  expect(getResp.ok(), await getResp.text()).toBeTruthy();
  // The detail read alone carries the ticket's attempts.
  const { attempts, ...detail } = await getResp.json();
  expect(detail).toEqual(created);
  expect(attempts).toHaveLength(1);
  expect(attempts[0]).toMatchObject({ number: 1, status: "queued", promoted: true, holder: null, variant: null });
});

// The collection endpoints wrap their array in an object. A bare top-level
// array has nowhere to grow a cursor or a count without breaking every
// existing client -- so the envelope is asserted here rather than left implicit.
test("list tickets returns a {tickets: [...]} envelope", async ({ api }) => {
  await api.post("/api/tickets", { data: { title: "Included in the list" } });

  const resp = await api.get("/api/tickets");
  expect(resp.ok(), await resp.text()).toBeTruthy();
  const body = await resp.json();
  expect(Array.isArray(body.tickets)).toBe(true);
  expect(body.tickets.some((t: { title: string }) => t.title === "Included in the list")).toBe(true);
});

test("add then list a ticket's comments", async ({ api }) => {
  const createResp = await api.post("/api/tickets", { data: { title: "Has a comment" } });
  const ticket = await createResp.json();

  const commentResp = await api.post(`/api/tickets/${ticket.id}/comments`, { data: { body: "Started on this" } });
  expect(commentResp.status(), await commentResp.text()).toBe(201);
  expect(await commentResp.json()).toMatchObject({ body: "Started on this", author_agent_id: null });

  const listResp = await api.get(`/api/tickets/${ticket.id}/comments`);
  expect(listResp.ok()).toBeTruthy();
  const { comments } = await listResp.json();
  expect(Array.isArray(comments)).toBe(true);
  expect(comments).toHaveLength(1);
  expect(comments[0]).toMatchObject({ body: "Started on this" });
});

// A path scope is filed with just a path. `source` is server-assigned, and
// requiring it on input meant a plain `{"path": "..."}` -- the obvious
// thing for a human with curl, or an agent, to send -- was rejected 422.
test("files a path scope without naming its source", async ({ api }) => {
  const resp = await api.post("/api/tickets", {
    data: { title: "Scoped", paths: [{ path: "server/internal/lease/" }] },
  });
  expect(resp.status(), await resp.text()).toBe(201);

  const ticket = await resp.json();
  expect(ticket.paths).toEqual([{ path: "server/internal/lease/", source: "filed" }]);
});

test("get an unknown ticket returns 404", async ({ api }) => {
  const resp = await api.get("/api/tickets/999999");
  expect(resp.status()).toBe(404);
});

// An unrecognised enum is client input, so it must be rejected before it
// reaches the schema's CHECK constraint -- a constraint violation surfaces
// as an opaque driver error that the handler can't tell apart from a
// genuine internal fault, and the client would get a 500 for bad input.
test("create a ticket with an unknown priority returns 422, not 500", async ({ api }) => {
  const resp = await api.post("/api/tickets", { data: { title: "Bad priority", priority: "bogus" } });
  expect(resp.status(), await resp.text()).toBe(422);
});

test("patching a ticket to an unknown status returns 422, not 500", async ({ api }) => {
  const createResp = await api.post("/api/tickets", { data: { title: "Good ticket" } });
  const ticket = await createResp.json();

  const resp = await api.patch(`/api/tickets/${ticket.id}`, { data: { status: "bogus" } });
  expect(resp.status(), await resp.text()).toBe(422);
});

test("create a ticket with an empty title returns 422", async ({ api }) => {
  const resp = await api.post("/api/tickets", { data: { title: "" } });
  expect(resp.status(), await resp.text()).toBe(422);
});
