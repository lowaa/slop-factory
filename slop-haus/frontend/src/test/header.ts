import { screen, within } from "@testing-library/react";
import type { UserEvent } from "@testing-library/user-event";

// Helpers for reaching what the desktop header keeps behind a menu: the More
// tabs (Integrations and Settings) and the account links. Each opens its menu
// the way a person does, by its button, so a test that used to click a link
// in the row still exercises the real path to it.

// The app's header is found by its brand heading: "banner" and "navigation"
// alone also match a ticket's own <header> and the footer's legal links.
async function appHeader(): Promise<HTMLElement> {
  const brand = await screen.findByRole("heading", { name: "slop-haus", level: 1 });
  const header = brand.closest("header");
  if (!header) throw new Error("appHeader: the brand heading is not in a header");
  return header;
}

export async function headerNav(): Promise<HTMLElement> {
  return within(await appHeader()).getByRole("navigation");
}

// The More button is the one button in the header's nav. Its name is "More",
// or the open tab's label when that tab is one of its own.
export async function openMoreMenu(user: UserEvent): Promise<void> {
  const nav = await headerNav();
  await user.click(within(nav).getByRole("button"));
}

// The account menu's button, named for the signed-in person, is the header's
// one button outside the nav: the processing toggle is a switch and the
// tenant switcher a combobox.
export async function openAccountMenu(user: UserEvent): Promise<void> {
  const header = await appHeader();
  const nav = within(header).getByRole("navigation");
  const account = within(header)
    .getAllByRole("button")
    .find((b) => !nav.contains(b));
  if (!account) throw new Error("openAccountMenu: no account menu button in the header");
  await user.click(account);
}

export async function clickMoreTab(user: UserEvent, name: string | RegExp): Promise<void> {
  await openMoreMenu(user);
  await user.click(screen.getByRole("link", { name }));
}

export async function clickAccountLink(user: UserEvent, name: string | RegExp): Promise<void> {
  await openAccountMenu(user);
  await user.click(screen.getByRole(name === "Sign out" ? "button" : "link", { name }));
}
