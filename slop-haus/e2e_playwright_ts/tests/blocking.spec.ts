import type { APIRequestContext } from "@playwright/test";

import { expect, test } from "./support/fixtures";
import { newTicket } from "./support/api_helpers";
import { filterAndChoose, openHeaderTab } from "./support/widgets";
import { credentialAgentViaCodeFlow } from "./support/oauth";

// Blocking is the same edge the claim path already uses to decide what is
// claimable, so these check that the graph view and the scheduler agree.
// Two answers to "is this blocked" that can disagree is worse than one.

async function block(api: APIRequestContext, blocked: number, blocker: number) {
  const resp = await api.post(`/api/tickets/${blocked}/blockers`, {
    data: { blocker_id: blocker },
  });
  expect(resp.status(), await resp.text()).toBe(200);
  return resp.json();
}

async function candidates(api: APIRequestContext, ticketID: number) {
  const resp = await api.get(`/api/tickets/${ticketID}/blocker_candidates`);
  expect(resp.status(), await resp.text()).toBe(200);
  return (await resp.json()).tickets as { id: number; title: string }[];
}

function node(graph: any, id: number) {
  const found = graph.nodes.find((n: { id: number }) => n.id === id);
  expect(found, `#${id} should be in the graph`).toBeTruthy();
  return found;
}

test("a ticket can be blocked by several others at once", async ({ api }) => {
  const target = await newTicket(api, `needs three ${Date.now()}`);
  const blockers = [];
  for (let i = 0; i < 3; i++) {
    blockers.push((await newTicket(api, `blocker ${i} ${Date.now()}`)).id);
  }
  for (const b of blockers) await block(api, target.id, b);

  const graph = await (await api.get("/api/tickets/graph")).json();
  const n = node(graph, target.id);
  expect(n.blocked_by.sort()).toEqual(blockers.sort());
  expect(n.blocked).toBe(true);
});

// The layering is what lets the browser draw the graph without doing its
// own topological sort.
test("the graph arrives with layers already worked out", async ({ api }) => {
  const first = await newTicket(api, `first ${Date.now()}`);
  const second = await newTicket(api, `second ${Date.now()}`);
  const third = await newTicket(api, `third ${Date.now()}`);
  await block(api, second.id, first.id);
  await block(api, third.id, second.id);

  const graph = await (await api.get("/api/tickets/graph")).json();
  expect(node(graph, first.id).layer).toBeLessThan(node(graph, second.id).layer);
  expect(node(graph, second.id).layer).toBeLessThan(node(graph, third.id).layer);

  // Transitive: the far end waits on the whole chain, not just its
  // immediate blocker.
  expect(node(graph, third.id).transitive_blockers.sort()).toEqual([first.id, second.id].sort());
  expect(node(graph, first.id).unblocks).toBe(2);
});

// The board and the scheduler must not disagree about what "blocked"
// means: a ticket the graph shows as blocked must not be handed out.
test("a blocked ticket is not offered to an agent", async ({ api }) => {
  // An agent has no credential of its own until something is authorized
  // to act as it -- see support/oauth.ts.
  const { accessToken } = await credentialAgentViaCodeFlow(api, `blocked-check-${Date.now()}`);
  const auth = { Authorization: `Bearer ${accessToken}` };

  const blocker = await newTicket(api, `must go first ${Date.now()}`);
  const dependent = await newTicket(api, `waits ${Date.now()}`);
  await block(api, dependent.id, blocker.id);

  const graph = await (await api.get("/api/tickets/graph")).json();
  expect(node(graph, dependent.id).blocked).toBe(true);

  const claim = await api.post("/api/claims", {
    headers: auth,
    data: { ticket_id: dependent.id },
  });
  expect(claim.status(), await claim.text()).toBe(200);
  expect((await claim.json()).ticket, "the scheduler must agree it is blocked").toBeNull();
});

// Finishing a blocker has to free what it was holding up -- in the graph
// and in the queue alike.
test("finishing a blocker unblocks what it held up", async ({ api }) => {
  const blocker = await newTicket(api, `will finish ${Date.now()}`);
  const dependent = await newTicket(api, `then free ${Date.now()}`);
  await block(api, dependent.id, blocker.id);

  await api.patch(`/api/tickets/${blocker.id}`, { data: { status: "done" } });

  const graph = await (await api.get("/api/tickets/graph")).json();
  const n = node(graph, dependent.id);
  expect(n.blocked).toBe(false);
  expect(n.transitive_blockers).toEqual([]);
  expect(n.layer).toBe(0);
});

// Nothing un-blocks a ticket by hand, so a cycle would strand it forever
// with no route back through the API.
test("a blocking cycle is refused", async ({ api }) => {
  const a = await newTicket(api, `a ${Date.now()}`);
  const b = await newTicket(api, `b ${Date.now()}`);
  await block(api, b.id, a.id);

  const resp = await api.post(`/api/tickets/${a.id}/blockers`, {
    data: { blocker_id: b.id },
  });
  expect(resp.status(), await resp.text()).toBe(422);
});

test("a blocker can be removed again", async ({ api }) => {
  const blocked = await newTicket(api, `blocked ${Date.now()}`);
  const blocker = await newTicket(api, `blocker ${Date.now()}`);
  await block(api, blocked.id, blocker.id);

  const resp = await api.delete(`/api/tickets/${blocked.id}/blockers/${blocker.id}`);
  expect(resp.status(), await resp.text()).toBe(200);
  expect((await resp.json()).blocked_by).toEqual([]);

  const graph = await (await api.get("/api/tickets/graph")).json();
  expect(node(graph, blocked.id).blocked).toBe(false);
});

test("the blockers view narrows to one ticket's upstream", async ({ api }) => {
  const work = await newTicket(api, `work ${Date.now()}`);
  const blocker = await newTicket(api, `blocker ${Date.now()}`);
  const unrelated = await newTicket(api, `unrelated ${Date.now()}`);
  await block(api, work.id, blocker.id);

  const graph = await (await api.get(`/api/tickets/${work.id}/blockers`)).json();
  const ids = graph.nodes.map((n: { id: number }) => n.id);
  expect(ids).toContain(blocker.id);
  expect(ids).not.toContain(unrelated.id);
});

// The view through a browser. The list holds the blocked ticket, and opening
// it draws its own graph. The columns come from the server's `layers` array
// -- if the UI ever starts sorting these itself, that is a second definition
// of the ordering free to drift from the scheduler's.
test("the blocked view draws a ticket's chain in dependency order when it is opened", async ({ page, api }) => {
  const stamp = Date.now();
  const first = await newTicket(api, `ui-first-${stamp}`);
  const second = await newTicket(api, `ui-second-${stamp}`);
  await block(api, second.id, first.id);

  await page.goto("/");
  await openHeaderTab(page, "Blocked");

  // Only the blocked end is listed; the blocker comes with its graph.
  await expect(page.getByText(`ui-second-${stamp}`)).toBeVisible();
  await expect(page.getByText(`ui-first-${stamp}`)).toHaveCount(0);
  await page.getByRole("button", { name: `Show graph for #${second.id}` }).click();
  await expect(page.getByText(`ui-first-${stamp}`)).toBeVisible();

  // ...and the blocker is drawn before the ticket it holds up.
  const firstBox = await page.getByText(`ui-first-${stamp}`).boundingBox();
  const secondBox = await page.locator(".blocking-graph__layers").getByText(`ui-second-${stamp}`).boundingBox();
  expect(firstBox, "the first ticket should be rendered").toBeTruthy();
  expect(secondBox, "the second ticket should be rendered").toBeTruthy();
  expect(firstBox!.x, "a blocker belongs left of what it blocks").toBeLessThan(secondBox!.x);
});

// The candidate list is what the picker in the UI is built from, so it has
// to agree with AddDependency exactly: offering a ticket the server then
// refuses teaches a human the cycle rule by failing at them.
test("the candidate list offers only blockers the api will accept", async ({ api }) => {
  const stamp = Date.now();
  const work = await newTicket(api, `cand-work-${stamp}`);
  const downstream = await newTicket(api, `cand-downstream-${stamp}`);
  const existing = await newTicket(api, `cand-existing-${stamp}`);
  await block(api, work.id, existing.id);
  await block(api, downstream.id, work.id);

  const offered = await candidates(api, work.id);
  const ids = offered.map((c) => c.id);
  expect(ids).not.toContain(work.id);
  expect(ids).not.toContain(existing.id);
  expect(ids).not.toContain(downstream.id);

  // Everything it does offer is accepted, which is the claim the picker
  // rests on.
  for (const candidate of offered.slice(0, 5)) {
    const resp = await api.post(`/api/tickets/${work.id}/blockers`, {
      data: { blocker_id: candidate.id },
    });
    expect(resp.status(), `#${candidate.id} was offered: ${await resp.text()}`).toBe(200);
  }
});

test("a finished ticket is not offered as a blocker", async ({ api }) => {
  const stamp = Date.now();
  const work = await newTicket(api, `cand-open-${stamp}`);
  const finished = await newTicket(api, `cand-done-${stamp}`);

  const patched = await api.patch(`/api/tickets/${finished.id}`, { data: { status: "done" } });
  expect(patched.status(), await patched.text()).toBe(200);

  const ids = (await candidates(api, work.id)).map((c) => c.id);
  expect(ids).not.toContain(finished.id);
});

test("a blocker is chosen from a list in the ticket view, never typed", async ({ page, api }) => {
  const stamp = Date.now();
  const work = await newTicket(api, `pick-work-${stamp}`);
  const blocker = await newTicket(api, `pick-blocker-${stamp}`);
  // A third ticket, so the picker still has something to offer after the
  // first is used up -- otherwise the control is legitimately replaced by
  // its empty state and there is nothing left to inspect.
  await newTicket(api, `pick-other-${stamp}`);

  await page.goto("/");
  await page.getByText(`pick-work-${stamp}`).click();
  await expect(page.getByText("Nothing is blocking this ticket.")).toBeVisible();

  // Found by typing part of its title, which is the point of a filterable
  // picker on a board with more tickets than fit in a panel.
  const chosen = new RegExp(`#${blocker.id} pick-blocker-${stamp}`);
  await filterAndChoose(page, "Add blocker", `pick-blocker-${stamp}`, chosen);
  // Add submits whatever the form holds, and an unchosen picker is refused
  // with "Choose a ticket." -- so wait for the choice to show in the box
  // rather than clicking in the same instant the option is picked.
  await expect(page.getByRole("combobox", { name: "Add blocker" })).toHaveValue(chosen);
  await page.getByRole("button", { name: "Add", exact: true }).click();

  await expect(page.getByText("Nothing is blocking this ticket.")).not.toBeVisible();
  // Named, not just numbered, so a human can tell what they just added.
  await expect(page.getByText(`#${blocker.id} pick-blocker-${stamp}`, { exact: true })).toBeVisible();

  // The ticket itself is never on offer: the server drops it, so the UI
  // cannot present a self-block for a human to try.
  await page.getByRole("combobox", { name: "Add blocker" }).click();
  await expect(page.getByRole("listbox").getByRole("option", { name: new RegExp(`pick-work-${stamp}`) })).toHaveCount(0);

  const graph = await (await api.get("/api/tickets/graph")).json();
  expect(node(graph, work.id).blocked).toBe(true);
});

// The view is already the full width (layout.spec.ts), so starting an
// edit should not move anything sideways; the editor used to be the only
// thing that broke out of a narrower reading column.
test("the ticket body is edited in place, at the width it is read at", async ({ page, api }) => {
  const stamp = Date.now();
  await newTicket(api, `edit-body-${stamp}`);

  await page.goto("/");
  await page.getByText(`edit-body-${stamp}`).click();

  const detail = page.locator(".ticket-detail");
  const readingWidth = (await detail.boundingBox())!.width;

  await page.getByRole("button", { name: "Edit title and description" }).click();
  await detail.getByLabel("Description").fill("a much longer description, written in the wide editor");
  const editingWidth = (await detail.boundingBox())!.width;
  expect(editingWidth, "editing should not change the view's width").toBe(readingWidth);

  await page.getByRole("button", { name: "Save" }).click();
  // The body itself, not the history's diff line announcing it.
  await expect(detail.locator(".ticket-detail__body")).toHaveText("a much longer description, written in the wide editor");

  // It survives a reload, so it was really saved rather than only shown.
  // The view lives in React state, so the reload lands back on the board
  // and the ticket has to be reopened.
  await page.reload();
  await page.getByText(`edit-body-${stamp}`).click();
  await expect(detail.locator(".ticket-detail__body")).toHaveText("a much longer description, written in the wide editor");
});

// The graph answers "what is holding this up". The answer to "so go and
// fix it" is the ticket, and before this the only route there was back to
// the board to find the same card a second time.
test("clicking a ticket in the blocked view opens that ticket", async ({ page, api }) => {
  const stamp = Date.now();
  const first = await newTicket(api, `nav-first-${stamp}`);
  const second = await newTicket(api, `nav-second-${stamp}`);
  await block(api, second.id, first.id);

  await page.goto("/");
  await openHeaderTab(page, "Blocked");

  // The blocked end of the chain, not the one at the front -- a click
  // that opened whatever was leftmost would pass on the other card.
  await page.getByText(`nav-second-${stamp}`).click();

  await expect(page.getByRole("heading", { name: `#${second.id} nav-second-${stamp}` })).toBeVisible();
  // The blocker as the blockers list names it; the comment the block left
  // mentions it too, so the bare number alone matches twice.
  await expect(page.getByText(`#${first.id} nav-first-${stamp}`)).toBeVisible();

  // ...and the editor really is the editor, not a read-only panel.
  await expect(page.getByRole("combobox", { name: "Status" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Edit title and description" })).toBeVisible();
});

// Back has to return where it came from. Sending someone to the board
// after they opened a ticket from the graph loses their place in it, and
// the graph is the view you are in precisely because you are tracing a
// chain through it.
test("back from a ticket opened in the blocked view returns to the graph", async ({ page, api }) => {
  const stamp = Date.now();
  const first = await newTicket(api, `back-first-${stamp}`);
  const second = await newTicket(api, `back-second-${stamp}`);
  await block(api, second.id, first.id);

  await page.goto("/");
  await openHeaderTab(page, "Blocked");
  await page.getByText(`back-second-${stamp}`).click();

  await expect(page.getByRole("link", { name: "← Back to blocked" })).toBeVisible();
  await page.getByRole("link", { name: "← Back to blocked" }).click();

  await expect(page.getByText(`back-second-${stamp}`)).toBeVisible();
});

// The board route is unchanged by any of that.
test("back from a ticket opened on the board still returns to the board", async ({ page, api }) => {
  const stamp = Date.now();
  await newTicket(api, `board-back-${stamp}`);

  await page.goto("/");
  await page.getByText(`board-back-${stamp}`).click();
  await page.getByRole("link", { name: "← Back to board" }).click();

  await expect(page.getByRole("heading", { name: "File a ticket" })).toBeVisible();
});
