// Tabs and open tickets have addresses: the browser's Back and Forward move
// through them, a reload stays put, and a ticket link can be sent to
// someone. A ticket's number is only unique within its tenant, so a link
// names the tenant too, and is never opened in a different one without
// asking.
import type { APIRequestContext, Page } from "@playwright/test";
import { boardWriteHeaders, expect, test } from "./support/fixtures";
import { newTicket } from "./support/api_helpers";
import * as mailhog from "./support/mailhog";
import { independentPage, newAccount, registerAndActivate, signInViaUI } from "./support/auth_flow";
import { headerTab, openHeaderTab } from "./support/widgets";

async function sessionTenant(api: APIRequestContext): Promise<string> {
  const resp = await api.get("/api/auth/me");
  expect(resp.status(), await resp.text()).toBe(200);
  return (await resp.json()).tenant_id;
}

const ticketHeading = (page: Page, id: number, title: string) =>
  page.getByRole("heading", { name: `#${id} ${title}` });
const board = (page: Page) => page.getByRole("heading", { name: "File a ticket" });
const mismatch = (page: Page, id: number) =>
  page.getByRole("heading", { name: `Ticket #${id} is in another tenant` });

test("Back and Forward move through tabs and an open ticket", async ({ page, api }) => {
  const title = `history-${Date.now()}`;
  const ticket = await newTicket(api, title);
  const tenant = await sessionTenant(api);

  await page.goto("/");
  await openHeaderTab(page, "Workers");
  await expect(page).toHaveURL(/\/workers$/);
  await expect(page.getByText("No workers yet.")).toBeVisible();

  await openHeaderTab(page, "Board");
  await page.getByText(title).click();
  await expect(ticketHeading(page, ticket.id, title)).toBeVisible();
  expect(new URL(page.url()).pathname + new URL(page.url()).search).toBe(
    `/tickets/${ticket.id}?from=board&tenant=${tenant}`,
  );

  await page.goBack();
  await expect(board(page)).toBeVisible();
  await expect(page).toHaveURL(/\/$/);

  await page.goBack();
  await expect(page.getByText("No workers yet.")).toBeVisible();
  await expect(page).toHaveURL(/\/workers$/);

  await page.goForward();
  await page.goForward();
  await expect(ticketHeading(page, ticket.id, title)).toBeVisible();

  // The in-ticket Back is a link to the tab it came from, not history back:
  // it goes to the board even though the entry before this one is too.
  await page.getByRole("link", { name: "← Back to board" }).click();
  await expect(board(page)).toBeVisible();
  await expect(page).toHaveURL(/\/$/);
});

test("an open ticket survives a reload, and its link opens it directly", async ({ page, api }) => {
  const title = `deep-link-${Date.now()}`;
  const ticket = await newTicket(api, title);

  await page.goto("/");
  const card = page.getByRole("link", { name: new RegExp(title) });
  // A real link, so middle-click and copy-link have an address to use.
  const href = await card.getAttribute("href");
  expect(href).toMatch(new RegExp(`^/tickets/${ticket.id}\\?from=board&tenant=`));
  await card.click();
  await expect(ticketHeading(page, ticket.id, title)).toBeVisible();

  await page.reload();
  await expect(ticketHeading(page, ticket.id, title)).toBeVisible();
  await expect(headerTab(page, "Board")).toHaveAttribute("aria-current", "page");

  await page.goto("/members");
  await expect(page.getByRole("heading", { name: "Invite someone" })).toBeVisible();
  await page.goto(href!);
  await expect(ticketHeading(page, ticket.id, title)).toBeVisible();
});

test("a ticket opened from the blocked view keeps its way back after a reload", async ({ page, api }) => {
  const stamp = Date.now();
  const blocker = await newTicket(api, `reload-blocker-${stamp}`);
  const blocked = await newTicket(api, `reload-blocked-${stamp}`);
  const resp = await api.post(`/api/tickets/${blocked.id}/blockers`, { data: { blocker_id: blocker.id } });
  expect(resp.status(), await resp.text()).toBe(200);

  await page.goto("/blocked");
  await page.getByText(`reload-blocked-${stamp}`).click();
  await expect(ticketHeading(page, blocked.id, `reload-blocked-${stamp}`)).toBeVisible();
  await expect(page).toHaveURL(/from=blocked/);

  await page.reload();
  await expect(ticketHeading(page, blocked.id, `reload-blocked-${stamp}`)).toBeVisible();
  await expect(headerTab(page, "Blocked")).toHaveAttribute("aria-current", "page");
  await page.getByRole("link", { name: "← Back to blocked" }).click();
  await expect(page).toHaveURL(/\/blocked$/);
});

test("a ticket link for a tenant the user is not in is refused, and opens nothing here", async ({ page, api }) => {
  const ticket = await newTicket(api, "This tenant's own");
  const tenant = await sessionTenant(api);
  const elsewhere = crypto.randomUUID();

  await page.goto(`/tickets/${ticket.id}?from=blocked&tenant=${elsewhere}`);

  await expect(mismatch(page, ticket.id)).toBeVisible();
  await expect(page.getByText(/tenant you are not a member of/)).toBeVisible();
  await expect(page.getByRole("button", { name: /^Switch to / })).toHaveCount(0);
  await expect(page.getByText("This tenant's own")).toHaveCount(0);
  expect(await sessionTenant(api)).toBe(tenant);
  await page.getByRole("link", { name: "← Back to blocked" }).click();
  await expect(page).toHaveURL(/\/blocked$/);
});

// Both tenants have a #1. The invitee belongs to both, so a link to either
// one's #1 has to open that tenant's ticket, asking before it switches --
// never the #1 of whichever tenant the session happens to be in.
test("a ticket link for another tenant's same-numbered ticket asks before switching to it", async ({
  page,
  api,
  browser,
}) => {
  const account = newAccount("ticket-link-tenant");
  const { context, page: invitee } = await independentPage(browser);
  await registerAndActivate(invitee, account);
  await signInViaUI(invitee, account.email, account.password);
  await expect(board(invitee)).toBeVisible();

  const tenantA = await sessionTenant(api);
  const tenantB = await sessionTenant(invitee.request);
  const aTicket = await newTicket(api, "Tenant A's first");
  const bTicket = await newTicket(invitee.request, "Tenant B's first", {
    headers: await boardWriteHeaders(invitee.request),
  });
  expect(aTicket.id).toBe(bTicket.id);
  const id = aTicket.id;

  // Join the dev user's tenant A, which moves the invitee's session there.
  const invite = await api.post("/api/invites", { data: { email: account.email } });
  expect(invite.status(), await invite.text()).toBe(201);
  const body = await mailhog.waitForEmail(account.email, "invited you to slop-haus");
  await invitee.goto(mailhog.linkPath(body));
  await invitee.getByRole("button", { name: "Accept" }).click();
  await expect(board(invitee)).toBeVisible();
  expect(await sessionTenant(invitee.request)).toBe(tenantA);

  // A link to tenant B's #1, opened while in tenant A.
  await invitee.goto(`/tickets/${id}?from=board&tenant=${tenantB}`);
  await expect(mismatch(invitee, id)).toBeVisible();
  await expect(invitee.getByText("Tenant A's first")).toHaveCount(0);
  expect(await sessionTenant(invitee.request)).toBe(tenantA);

  await invitee.getByRole("button", { name: `Switch to ${account.displayName}` }).click();
  await expect(ticketHeading(invitee, id, "Tenant B's first")).toBeVisible();
  expect(await sessionTenant(invitee.request)).toBe(tenantB);
  expect(new URL(invitee.url()).search).toBe(`?from=board&tenant=${tenantB}`);

  // And the dev user's own copy-link of tenant A's #1, opened now that the
  // invitee is in tenant B, asks the same way round.
  await page.goto("/");
  const aHref = await page.getByRole("link", { name: /Tenant A's first/ }).getAttribute("href");
  expect(aHref).toBe(`/tickets/${id}?from=board&tenant=${tenantA}`);
  await invitee.goto(aHref!);
  await expect(mismatch(invitee, id)).toBeVisible();
  await expect(invitee.getByText("Tenant B's first")).toHaveCount(0);
  await invitee.getByRole("button", { name: /^Switch to / }).click();
  await expect(ticketHeading(invitee, id, "Tenant A's first")).toBeVisible();

  await context.close();
});
