import { expect, type Locator, type Page } from "@playwright/test";

// Drivers for @slop-factory/ui's Dropdown/FilterableDropdown widgets.
//
// These replaced the native <select>s the UI used to have, and
// locator.selectOption only works on a real select element -- so a picker
// is now driven the way a person drives it: open the control, click the
// option. One place to change if the widgets' markup moves.

export async function chooseFromDropdown(page: Page, label: string, optionLabel: string | RegExp): Promise<void> {
  const combobox = page.getByRole("combobox", { name: label });
  await combobox.click();
  // Scoped to the listbox the control opened, so a page that happens to
  // contain the same text elsewhere cannot satisfy this by accident.
  const options = page.getByRole("listbox").getByRole("option");
  // An option's accessible name can carry more than its label (the tenant
  // switcher adds its owner), so one tenant's name may be a substring of
  // another entry's name. A string that is exactly an option's label means
  // that option; anything else is matched by name as before.
  await expect(options.first()).toBeVisible();
  const exact = typeof optionLabel === "string" ? options.filter({ has: page.getByText(optionLabel, { exact: true }) }) : null;
  const target = exact !== null && (await exact.count()) > 0 ? exact : options.and(page.getByRole("option", { name: optionLabel }));
  await target.click();
  await expect(page.getByRole("listbox")).toBeHidden();
}

// Filtering first is the point of the filterable variant: typing has to
// narrow the list, or the click below would find the option anyway and
// the test would pass with filtering broken.
export async function filterAndChoose(
  page: Page,
  label: string,
  query: string,
  optionLabel: string | RegExp,
): Promise<void> {
  const combobox = page.getByRole("combobox", { name: label });
  await combobox.click();
  await combobox.fill(query);
  const listbox = page.getByRole("listbox");
  await expect(listbox.getByRole("option")).toHaveCount(1);
  await listbox.getByRole("option", { name: optionLabel }).click();
}

// The app header (not a ticket page's own <header>, nor the footer's nav).
const appHeader = (page: Page): Locator => page.locator("header.app__header");

// The tabs that live behind the desktop header's More button. Every other tab
// is a link in the bar; on a phone they are all in the Menu panel, which the
// caller opens.
const MORE_TABS = ["Git", "Intake", "Reports", "Compare", "Eval sets", "Members", "Tenant settings"];

// A header dropdown (More, the account menu) is opened by its button and
// closes on navigation; open it only if it is not already showing.
async function openMenu(button: Locator): Promise<void> {
  if ((await button.getAttribute("aria-expanded")) !== "true") {
    await button.click();
  }
  await expect(button).toHaveAttribute("aria-expanded", "true");
}

// The More button: the button inside the header's nav. Its name is "More", or
// the open tab's label when the page is one of the More tabs, so it is found by
// where it is rather than by name.
export function moreButton(page: Page): Locator {
  return appHeader(page).locator("nav.app__nav .header-menu__button");
}

// headerTab is one of the header's tabs, a link since tabs have URLs. Scoped to the header's nav: a ticket card is a link too, and one titled "Board ..." would otherwise match.
// A More tab's link is only visible while the More menu is open: click through
// openHeaderTab, and check which tab is current with expectHeaderTabActive.
export function headerTab(page: Page, name: string): Locator {
  return appHeader(page).locator("nav.app__nav").getByRole("link", { name, exact: true });
}

// openHeaderTab goes to a tab the way a person does: for a More tab, open the
// More menu first (unless it already is), then click the link.
export async function openHeaderTab(page: Page, name: string): Promise<void> {
  if (MORE_TABS.includes(name)) {
    await openMenu(moreButton(page));
  }
  await headerTab(page, name).click();
}

// expectHeaderTabActive asserts the tab reads as the current one. A bar tab
// carries aria-current itself. A More tab shows on the More button, which
// takes the tab's name; opening the menu then shows the link as current.
export async function expectHeaderTabActive(page: Page, name: string): Promise<void> {
  if (!MORE_TABS.includes(name)) {
    await expect(headerTab(page, name)).toHaveAttribute("aria-current", "page");
    return;
  }
  const more = moreButton(page);
  await expect(more).toHaveAccessibleName(name);
  await expect(more).toHaveClass(/header-menu__button--active/);
  await openMenu(more);
  await expect(headerTab(page, name)).toHaveAttribute("aria-current", "page");
}

// The account menu's button: in the header outside the nav, named for the
// person (their full name, though a narrower desktop shows only initials).
// Pass the display name where the test knows it, which also asserts it.
export function accountMenuButton(page: Page, displayName?: string): Locator {
  if (displayName !== undefined) {
    return appHeader(page).getByRole("button", { name: displayName, exact: true });
  }
  return appHeader(page).locator(".header-menu--end > .header-menu__button");
}

export async function openAccountMenu(page: Page, displayName?: string): Promise<void> {
  await openMenu(accountMenuButton(page, displayName));
}

// Each of these opens the account menu, then clicks the item inside it.
export async function openAccountPage(page: Page, displayName?: string): Promise<void> {
  await openAccountMenu(page, displayName);
  await appHeader(page).getByRole("link", { name: "Account", exact: true }).click();
}

export async function openNewTenant(page: Page, displayName?: string): Promise<void> {
  await openAccountMenu(page, displayName);
  await appHeader(page).getByRole("link", { name: "New tenant", exact: true }).click();
}

export async function signOutViaHeader(page: Page, displayName?: string): Promise<void> {
  await openAccountMenu(page, displayName);
  await appHeader(page).getByRole("button", { name: "Sign out", exact: true }).click();
}

// The board's "File a ticket" form starts collapsed; this opens it the way
// a person does, and waits until its fields are actually showing.
export async function openNewTicketForm(page: Page): Promise<void> {
  const toggle = page.getByRole("button", { name: "File a ticket" });
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  await expect(page.getByLabel("Title")).toBeVisible();
}
