// A tab left open on tenant A while another tab of the same browser switches
// the shared session cookies to tenant B. The stale tab must not act on B:
// the server refuses what it sends, and the tab says why.
//
// The account needs two tenants, which it gets the way a person does: the dev
// user (tenant A) invites it, and it accepts while signed in. Its own tenant
// is the second.
import type { Browser, Page, Route } from "@playwright/test";
import { expect, test } from "./support/fixtures";
import * as mailhog from "./support/mailhog";
import { fillLoginForm, independentPage, newAccount, registerAndActivate } from "./support/auth_flow";
import { chooseFromDropdown, openHeaderTab, openNewTicketForm } from "./support/widgets";

async function twoTabsOnTheInvitersTenant(page: Page, browser: Browser) {
  const account = newAccount("stale-tab");
  const { context, page: tab1 } = await independentPage(browser);
  await registerAndActivate(tab1, account);

  await page.goto("/");
  await openHeaderTab(page, "Members");
  await page.getByLabel("Email").fill(account.email);
  await page.getByRole("button", { name: "Send invite" }).click();
  await expect(page.getByText(account.email)).toBeVisible();
  const body = await mailhog.waitForEmail(account.email, "invited you to slop-haus");

  await tab1.goto("/login");
  await fillLoginForm(tab1, account.email, account.password);
  await expect(tab1.getByRole("heading", { name: "File a ticket" })).toBeVisible();
  await tab1.goto(mailhog.linkPath(body));
  await expect(tab1.getByRole("heading", { name: "You're invited" })).toBeVisible();
  await tab1.getByRole("button", { name: "Accept" }).click();
  await expect(tab1.getByRole("heading", { name: "File a ticket" })).toBeVisible();
  await expect(tab1.getByRole("combobox", { name: "Tenant" })).toBeVisible();

  const tab2 = await context.newPage();
  await tab2.goto("/");
  await expect(tab2.getByRole("combobox", { name: "Tenant" })).toBeVisible();
  return { account, context, tab1, tab2 };
}

// A switch ends in a full page load (the switcher stays disabled, still showing
// the old tenant, until the browser has navigated), so the wait is on that load,
// not on a 5s assertion that a slow CI navigation can outrun.
async function switchTenantInTab(tab: Page, tenantName: string) {
  const loaded = tab.waitForEvent("load");
  await chooseFromDropdown(tab, "Tenant", tenantName);
  await loaded;
  await expect(tab.getByRole("combobox", { name: "Tenant" })).toHaveText(tenantName);
}

async function fileTicket(page: Page, title: string) {
  await openNewTicketForm(page);
  await page.getByLabel("Title").fill(title);
  await page.getByRole("button", { name: "Open", exact: true }).click();
}

test("another tab's tenant switch is shown at once, and nothing the stale tab does lands anywhere", async ({
  page,
  browser,
  api,
}) => {
  const { account, context, tab1, tab2 } = await twoTabsOnTheInvitersTenant(page, browser);

  await switchTenantInTab(tab1, account.displayName);

  await expect(tab2.getByRole("alertdialog")).toContainText("Your session is now on");
  await expect(tab2.getByRole("button", { name: `Reload into ${account.displayName}` })).toBeVisible();
  await expect(tab2.getByRole("button", { name: /^Switch back to/ })).toBeVisible();
  await expect(tab2.getByRole("button", { name: "File a ticket" })).toHaveCount(0);

  const tickets = await api.get("/api/tickets");
  expect((await tickets.json()).tickets).toEqual([]);
  await context.close();
});

// Without the broadcast (a browser without BroadcastChannel, or a tab opened
// before the switch was announced) the server is the guarantee: the write is
// refused, nothing is filed in either tenant, and the tab shows the notice.
test("a write from a stale tab that was never told is refused by the server", async ({ page, browser, api }) => {
  const { account, context, tab1, tab2 } = await twoTabsOnTheInvitersTenant(page, browser);
  await tab2.close();
  const quiet = await context.newPage();
  await quiet.addInitScript(() => {
    Object.defineProperty(window, "BroadcastChannel", { value: undefined });
  });
  await quiet.goto("/");
  await expect(quiet.getByRole("combobox", { name: "Tenant" })).toBeVisible();

  // Installed before the switch, so no poll can land after it. The tab's own
  // polling and stream would otherwise hit the same refusal first, replace the
  // tab with the notice, and leave no form to write from: the write below must
  // be the only request that tells it. Aborted, not answered, so nothing but
  // the server's refusal of the write can announce the change.
  // Once the server has refused the write, everything goes through: the flag
  // is set before the refusal reaches the page, so the notice's own listing
  // can never be intercepted while it is still off.
  let refusalSeen = false;
  const onlyTheWrite = async (route: Route) => {
    if (refusalSeen) return route.continue();
    if (route.request().method() === "POST" && route.request().url().endsWith("/api/tickets")) {
      const response = await route.fetch();
      refusalSeen = true;
      return route.fulfill({ response });
    }
    return route.abort();
  };
  await quiet.route("**/api/**", onlyTheWrite);

  await switchTenantInTab(tab1, account.displayName);

  const refused = quiet.waitForResponse((r) => r.request().method() === "POST" && r.url().endsWith("/api/tickets"));
  await fileTicket(quiet, "Filed from a stale tab");
  expect((await refused).status()).toBe(409);
  // The dialog reads "Loading…" until that listing answers, and on a loaded CI
  // runner the answer can take longer than the default 5s assertion.
  // A failed listing renders the same sentence with "another tenant", so the
  // loaded names are what show it got through.
  await expect(quiet.getByRole("alertdialog")).toContainText(`Your session is now on ${account.displayName}`, {
    timeout: 20_000,
  });
  const tickets = await api.get("/api/tickets");
  expect((await tickets.json()).tickets).toEqual([]);
  await context.close();
});
