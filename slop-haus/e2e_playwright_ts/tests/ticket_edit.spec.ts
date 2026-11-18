// A ticket's title and description are edited from two doors -- a person on
// the ticket detail view, and the agent holding the ticket through its MCP
// ticket_edit tool -- into one history, which both the board and the next
// agent to read the ticket can see.
import { expect, test } from "@/support/fixtures";
import { McpSession } from "@/support/mcp";
import { credentialAgentViaCodeFlow } from "@/support/oauth";

test("a person and the agent holding a ticket both edit it, and the history shows each", async ({ page, api }) => {
  const stamp = Date.now();
  const created = await api.post("/api/tickets", {
    data: { title: `Delete button ${stamp}`, body: "Add a delete button.", paths: [{ path: `e2e/edit-${stamp}/` }] },
  });
  expect(created.status(), await created.text()).toBe(201);
  const ticket = await created.json();

  // The agent claims it -- directed, so no other spec's ticket is taken --
  // and renames it through MCP, as it learns what the work really is.
  const { accessToken } = await credentialAgentViaCodeFlow(api, `editor-${stamp}`);
  const claim = await api.post("/api/claims", {
    headers: { Authorization: `Bearer ${accessToken}` },
    data: { ticket_id: ticket.id },
  });
  expect(claim.status(), await claim.text()).toBe(200);
  const mcp = await McpSession.open(api, accessToken);
  const edited = await mcp.callTool("ticket_edit", { revision: 0, title: `Reinstall script ${stamp}` });
  expect(edited.isError, edited.text).toBe(false);
  expect(edited.text).toContain(`Edited #${ticket.id} (revision 1)`);

  // Then the person opens it on the board and rewrites the description.
  await page.goto("/");
  await page.getByText(`Reinstall script ${stamp}`).click();
  await expect(page.getByRole("heading", { name: `#${ticket.id} Reinstall script ${stamp}` })).toBeVisible();

  const detail = page.locator(".ticket-detail");
  await detail.getByRole("button", { name: "Edit title and description" }).click();
  await detail.getByLabel("Description").fill("A per-agent reinstall script.\nAnd a hide-disabled filter.");
  await detail.getByRole("button", { name: "Save" }).click();
  await expect(detail.locator(".ticket-detail__body")).toHaveText(
    "A per-agent reinstall script.\nAnd a hide-disabled filter.",
  );

  // Both edits are in the history, newest first, each with what it changed.
  const history = page.getByRole("region", { name: "History" });
  const entries = history.getByRole("list", { name: "Edits" }).getByRole("listitem");
  await expect(entries).toHaveCount(2);
  await expect(entries.nth(0)).toContainText("Revision 2");
  await expect(entries.nth(0)).toContainText("- Add a delete button.");
  await expect(entries.nth(0)).toContainText("+ And a hide-disabled filter.");
  await expect(entries.nth(1)).toContainText("Revision 1");
  await expect(entries.nth(1)).toContainText(`editor-${stamp}`);
  await expect(entries.nth(1)).toContainText(`Title: Delete button ${stamp} → Reinstall script ${stamp}`);

  // The same section lists the ticket's status changes: its creation, then
  // the agent's claim.
  const moves = history.getByRole("heading", { name: "Status changes" }).locator("xpath=..").getByRole("listitem");
  await expect(moves).toHaveCount(2);
  await expect(moves.nth(0)).toContainText("open → in progress");
  await expect(moves.nth(0)).toContainText("claimed");
  await expect(moves.nth(1)).toContainText("Created as open");

  // And the agent reading the ticket back sees both edits too.
  const read = await mcp.callTool("ticket_get", { ticket_id: ticket.id });
  expect(read.isError, read.text).toBe(false);
  expect(read.text).toContain(`r1 by editor-${stamp}`);
  expect(read.text).toContain("r2 by human");
  expect(read.text).toContain("+ And a hide-disabled filter.");
});

// The agent's door opens onto its own held ticket only: the tool has no way
// to name another, and without a lease it has nothing to edit.
test("an agent holding nothing cannot edit a ticket", async ({ api }) => {
  const stamp = Date.now();
  const created = await api.post("/api/tickets", { data: { title: `Not yours ${stamp}` } });
  const ticket = await created.json();
  const { accessToken } = await credentialAgentViaCodeFlow(api, `idle-${stamp}`);
  const mcp = await McpSession.open(api, accessToken);

  const refused = await mcp.callTool("ticket_edit", { revision: 0, title: "Hijacked" });
  expect(refused.isError).toBe(true);

  const after = await (await api.get(`/api/tickets/${ticket.id}`)).json();
  expect(after.title).toBe(`Not yours ${stamp}`);
  expect(after.revision).toBe(0);
});
