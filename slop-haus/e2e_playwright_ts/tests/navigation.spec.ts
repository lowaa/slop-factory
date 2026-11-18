// Moving around the signed-in app: the header's tabs, the account page and
// the browser's own Back/Forward between them. These pin what a person sees
// and, where a page already has one, its URL -- not how the app gets there,
// so a change of routing underneath has to keep them passing unchanged.
//
// The redirects into and out of /login are in auth.spec.ts, the invite and
// OAuth round trips through it in invites.spec.ts and oauth.spec.ts, and
// "Back to blocked"/"Back to board" from an open ticket in blocking.spec.ts.
import type { Locator, Page } from "@playwright/test";
import { expect, test } from "./support/fixtures";
import { newTicket } from "./support/api_helpers";
import { expectHeaderTabActive, headerTab, openAccountPage, openHeaderTab } from "./support/widgets";

// What each tab shows with nothing in it yet -- cleanDatabase leaves every
// panel empty, so these are all there is to see.
const TABS: { name: string; path: string; shows: (page: Page) => Locator }[] = [
  { name: "Board", path: "/", shows: (page) => page.getByRole("heading", { name: "File a ticket" }) },
  { name: "Remote Control", path: "/remote-control", shows: (page) => page.getByRole("button", { name: "add a remote control", exact: true }) },
  { name: "Blocked", path: "/blocked", shows: (page) => page.getByText("Nothing is blocked.") },
  { name: "Reports", path: "/reports", shows: (page) => page.getByText(/no history yet/) },
  { name: "Workspace", path: "/workspace", shows: (page) => page.getByRole("heading", { name: "Workspace", exact: true }) },
  { name: "Workers", path: "/workers", shows: (page) => page.getByText("No workers yet.") },
  { name: "Git", path: "/git", shows: (page) => page.getByRole("heading", { name: "GitHub webhooks" }) },
  { name: "Intake", path: "/intake", shows: (page) => page.getByRole("heading", { name: "Intake sources" }) },
  { name: "Members", path: "/members", shows: (page) => page.getByRole("heading", { name: "Invite someone" }) },
];

const pathOf = (page: Page) => new URL(page.url()).pathname;

const accountPage = (page: Page) => page.getByRole("heading", { name: "Account", exact: true });

test("each header tab shows its own panel, and only that one, at its own address", async ({ page }) => {
  await page.goto("/");
  await expect(TABS[0].shows(page)).toBeVisible();

  // Walked in reverse so every tab, Board included, is reached by a click
  // from some other tab rather than only by the initial load.
  for (const tab of [...TABS].reverse()) {
    await openHeaderTab(page, tab.name);
    await expect(tab.shows(page), tab.name).toBeVisible();
    expect(pathOf(page), tab.name).toBe(tab.path);
    await expectHeaderTabActive(page, tab.name);
    for (const other of TABS.filter((t) => t !== tab)) {
      await expect(other.shows(page), `${other.name} while on ${tab.name}`).toHaveCount(0);
    }
  }
});

// Agents do not coordinate through a chat room: the tab, and the API behind
// it, are gone.
test("there is no Chat tab, and nothing answers at its old API", async ({ page, api }) => {
  await page.goto("/");
  await expect(TABS[0].shows(page)).toBeVisible();
  await expect(headerTab(page, "Chat")).toHaveCount(0);

  const listed = await api.get("/api/chat");
  expect(listed.status()).toBe(404);
});

test("each tab's address opens that tab on a fresh load", async ({ page }) => {
  for (const tab of TABS) {
    await page.goto(tab.path);
    await expect(tab.shows(page), tab.path).toBeVisible();
    await expectHeaderTabActive(page, tab.name);
  }
});

test("a tab clicked from an open ticket closes the ticket and shows the tab", async ({ page, api }) => {
  const title = `tab-from-ticket-${Date.now()}`;
  const ticket = await newTicket(api, title);

  await page.goto("/");
  await page.getByText(title).click();
  const heading = page.getByRole("heading", { name: `#${ticket.id} ${title}` });
  await expect(heading).toBeVisible();

  await openHeaderTab(page, "Workers");
  await expect(page.getByText("No workers yet.")).toBeVisible();
  await expect(heading).toHaveCount(0);

  // Back on the board the ticket is a card again, not still open.
  await openHeaderTab(page, "Board");
  await expect(page.getByRole("heading", { name: "File a ticket" })).toBeVisible();
  await expect(heading).toHaveCount(0);
});

test("the Account link in the account menu opens /account, and the Board tab comes back to /", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "File a ticket" })).toBeVisible();

  await openAccountPage(page);
  await expect(accountPage(page)).toBeVisible();
  await expect(page).toHaveURL(/\/account$/);
  await expect(page.getByRole("heading", { name: "File a ticket" })).toHaveCount(0);

  // A reload stays where it was: /account is a real address, not state
  // that only lives in the open tab.
  await page.reload();
  await expect(accountPage(page)).toBeVisible();
  await expect(page).toHaveURL(/\/account$/);

  await openHeaderTab(page, "Board");
  await expect(page.getByRole("heading", { name: "File a ticket" })).toBeVisible();
  await expect(page).toHaveURL(/\/$/);
  await expect(accountPage(page)).toHaveCount(0);
});

// Any tab leaves the account page, not just Board -- and lands on that
// tab's own address, now that tabs have one.
test("any tab clicked from /account leaves the account page for that tab", async ({ page }) => {
  await page.goto("/account");
  await expect(accountPage(page)).toBeVisible();

  await openHeaderTab(page, "Workers");
  await expect(page.getByText("No workers yet.")).toBeVisible();
  await expect(accountPage(page)).toHaveCount(0);
  await expect(page).toHaveURL(/\/workers$/);
});

test("the browser's Back and Forward move between the board and /account", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "File a ticket" })).toBeVisible();
  await openAccountPage(page);
  await expect(accountPage(page)).toBeVisible();

  await page.goBack();
  await expect(page.getByRole("heading", { name: "File a ticket" })).toBeVisible();
  await expect(page).toHaveURL(/\/$/);
  await expect(accountPage(page)).toHaveCount(0);

  await page.goForward();
  await expect(accountPage(page)).toBeVisible();
  await expect(page).toHaveURL(/\/account$/);
});

test("the old /agents address lands on Workers", async ({ page }) => {
  await page.goto("/agents");
  await expect(page.getByText("No workers yet.")).toBeVisible();
  expect(pathOf(page)).toBe("/workers");
  await expectHeaderTabActive(page, "Workers");
});
