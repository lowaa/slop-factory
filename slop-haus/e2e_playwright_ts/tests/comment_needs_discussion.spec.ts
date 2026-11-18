// A person answers an agent's question and, in the same click, asks for
// the ticket to be planned rather than built. One request, so the asking
// agent -- often already waiting to reclaim it -- can never pick the ticket
// up between the answer and the flag.
import { expect, test } from "@/support/fixtures";
import { McpSession } from "@/support/mcp";
import { credentialAgentViaCodeFlow } from "@/support/oauth";

test("commenting with needs discussion on a ticket in discussion puts it back in the queue, flagged", async ({
  page,
  api,
}) => {
  const stamp = Date.now();
  const title = `comment-flag-${stamp}`;
  const created = await api.post("/api/tickets", {
    data: { title, paths: [{ path: `e2e/flagged-${stamp}/` }] },
  });
  expect(created.status(), await created.text()).toBe(201);
  const ticket = await created.json();

  // Directed, so no other spec's ticket is taken.
  const { accessToken } = await credentialAgentViaCodeFlow(api, `asker-${stamp}`);
  const auth = { Authorization: `Bearer ${accessToken}` };
  const claim = await api.post("/api/claims", { headers: auth, data: { ticket_id: ticket.id } });
  expect(claim.status(), await claim.text()).toBe(200);
  const mcp = await McpSession.open(api, accessToken);
  const asked = await mcp.callTool("ticket_discuss", {
    question: "Flat or nested?",
    handoff: "Nothing built yet.",
  });
  expect(asked.isError, asked.text).toBe(false);

  await page.goto("/");
  await page.getByText(title).click();
  const detail = page.locator(".ticket-detail");
  await expect(detail.getByText("Waiting on you.")).toBeVisible();
  await expect(detail.getByRole("checkbox", { name: "Needs discussion" })).not.toBeChecked();

  // The atomicity itself is proven against Postgres in the server's tests,
  // where the claim can be held inside the window; what the page owes is
  // that it takes the one route there is: a single comment request carrying
  // the flag, never a comment followed by a PATCH of the flag.
  const writes: string[] = [];
  page.on("request", (req) => {
    if (req.method() !== "GET" && req.url().includes(`/api/tickets/${ticket.id}`)) {
      writes.push(`${req.method()} ${new URL(req.url()).pathname} ${req.postData() ?? ""}`);
    }
  });
  await detail.getByLabel("Add a comment").fill("Neither yet -- plan it and come back.");
  await detail.getByRole("button", { name: "Comment & needs discussion" }).click();

  await expect(detail.getByText("Neither yet -- plan it and come back.")).toBeVisible();
  await expect(detail.getByRole("combobox", { name: "Status" })).toHaveText("open");
  await expect(detail.getByRole("checkbox", { name: "Needs discussion" })).toBeChecked();
  await expect(detail.getByRole("button", { name: "Comment & clear needs discussion" })).toBeVisible();
  expect(writes).toHaveLength(1);
  expect(writes[0]).toMatch(new RegExp(`^POST /api/tickets/${ticket.id}/comments `));
  expect(JSON.parse(writes[0].slice(writes[0].indexOf("{")))).toMatchObject({ needs_discussion: true });

  // The asker reclaims it and is told to plan, not build.
  const reclaim = await api.post("/api/claims", { headers: auth, data: { ticket_id: ticket.id } });
  expect(reclaim.status(), await reclaim.text()).toBe(200);
  const reclaimed = (await reclaim.json()).ticket;
  expect(reclaimed?.id).toBe(ticket.id);
  expect(reclaimed.needs_discussion).toBe(true);
});
