// Roles gate which tickets an agent may claim: a ticket filed
// with a role requirement sits in the queue until an agent holding that
// role claims it, and an agent without it is refused -- by name, not just
// an empty queue. This drives the whole path through the UI (create the
// role, give it to one agent, file the ticket through the board's own
// form) and then checks the claim path itself, the same way
// agent_roles.spec.ts and claims.spec.ts each cover their own half.
import { expect, test } from "./support/fixtures";
import { credentialAgentViaCodeFlow } from "./support/oauth";
import { openHeaderTab, openNewTicketForm } from "./support/widgets";

test("a ticket requiring a role can only be claimed by an agent that holds it", async ({ page, api }) => {
  const roleName = `deploy-${Date.now()}`;
  const holderName = `holder-${Date.now()}`;
  const outsiderName = `outsider-${Date.now()}`;
  const ticketTitle = `Needs ${roleName}`;

  const holder = await credentialAgentViaCodeFlow(api, holderName);
  const outsider = await credentialAgentViaCodeFlow(api, outsiderName);

  await page.goto("/");
  await openHeaderTab(page, "Workers");

  // Create the role and give it to the holder, the same way
  // agent_roles.spec.ts does -- through the UI, not the API, since that's
  // the flow this ticket is actually about.
  const rolesSection = page.locator(".agent-roles");
  await rolesSection.getByLabel("Role name").fill(roleName);
  await rolesSection.getByRole("button", { name: "Add role" }).click();
  await expect(rolesSection.getByText(roleName, { exact: true })).toBeVisible();

  const holderRow = page.locator(".agents-panel__row", { hasText: holderName });
  await holderRow.getByRole("button", { name: "Edit roles" }).click();
  const holderEditor = holderRow.getByRole("group", { name: `Roles for ${holderName}` });
  await holderEditor.getByRole("checkbox", { name: roleName }).check();
  await holderEditor.getByRole("button", { name: "Save" }).click();
  await expect(holderRow.locator(".agents-panel__role-chip", { hasText: roleName })).toBeVisible();

  // File the ticket requiring that role through the board's own new-ticket
  // form, so the role_requirement_picker gets exercised for real, not just
  // the raw API field it eventually sends.
  await openHeaderTab(page, "Board");
  await openNewTicketForm(page);
  await page.getByLabel("Title").fill(ticketTitle);
  const picker = page.getByRole("group", { name: "Roles needed to claim this" });
  await picker.getByRole("checkbox", { name: roleName }).check();
  await page.getByRole("button", { name: "Open", exact: true }).click();

  const openColumn = page.locator(".board__column").filter({ has: page.getByRole("heading", { name: "Open" }) });
  await expect(openColumn.getByText(ticketTitle)).toBeVisible();

  await page.getByText(ticketTitle).click();
  await expect(page.getByRole("heading", { name: new RegExp(ticketTitle) })).toBeVisible();
  await expect(page.getByText(`role: ${roleName}`)).toBeVisible();

  // The detail view's own heading is "#<id> <title>" -- the ticket id this
  // test claims by below, rather than a bare POST /api/claims that would
  // hand either agent whatever else is sitting in the shared queue.
  // Scoped past the app header's own <h1>slop-haus</h1>, which
  // getByRole("heading", { level: 1 }) alone also matches.
  const heading = await page.locator(".ticket-detail__header h1").textContent();
  const ticketId = Number(/^#(\d+)/.exec(heading ?? "")?.[1]);
  expect(ticketId).toBeGreaterThan(0);

  // The outsider is refused this specific ticket, and told why.
  const refused = await api.post("/api/claims", {
    headers: { Authorization: `Bearer ${outsider.accessToken}` },
    data: { ticket_id: ticketId },
  });
  expect(refused.status(), await refused.text()).toBe(403);
  expect((await refused.json()).detail).toContain(roleName);

  // The holder is granted it.
  const granted = await api.post("/api/claims", {
    headers: { Authorization: `Bearer ${holder.accessToken}` },
    data: { ticket_id: ticketId },
  });
  expect(granted.status(), await granted.text()).toBe(200);
  expect((await granted.json()).ticket.id).toBe(ticketId);
});
