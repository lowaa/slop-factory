// The board's select mode end to end: many tickets moved, readied or
// reprioritised in one action, all or nothing, on a wide window and a phone.
import { expect, test } from "@/support/fixtures";
import type { APIRequestContext, Locator, Page } from "@playwright/test";
import { chooseFromDropdown } from "@/support/widgets";
import { fileTicket } from "./support/api_helpers";

const WIDE = { width: 1600, height: 900 };
const NARROW = { width: 390, height: 844 };

// The bar is sticky to the bottom of the screen, so from there a menu that
// only opens downwards lands off-screen and its later options can't be
// reached. A long Open column keeps the bar stuck to the edge.
async function expectBottomBarMenusReachable(page: Page, api: APIRequestContext, viewport: { height: number }) {
  const stamp = Date.now();
  const picked = `bulk-flip-${stamp}`;
  const id = await fileTicket(api, picked, "open");
  for (let n = 0; n < 24; n++) await fileTicket(api, `bulk-flip-filler-${n}-${stamp}`, "open");

  await page.goto("/");
  await page.getByRole("button", { name: "Select", exact: true }).click();
  await selectCard(page, picked).click();
  await expect(actionBar(page).getByRole("status")).toHaveText("1 selected");
  const bar = actionBar(page);
  await expect(bar).toBeInViewport();

  const insideViewportAboveTrigger = async (label: string) => {
    const combobox = page.getByRole("combobox", { name: label });
    await combobox.click();
    const listbox = page.getByRole("listbox");
    await expect(listbox).toBeVisible();
    const menu = (await listbox.boundingBox())!;
    const trigger = (await combobox.boundingBox())!;
    expect(menu.y).toBeGreaterThanOrEqual(0);
    expect(menu.y + menu.height).toBeLessThanOrEqual(viewport.height);
    expect(menu.y + menu.height).toBeLessThanOrEqual(trigger.y);
    return listbox;
  };

  const moves = await insideViewportAboveTrigger("Move to");
  await moves.getByRole("option").last().click({ trial: true });
  await page.keyboard.press("Escape");
  await expect(moves).toBeHidden();

  const priorities = await insideViewportAboveTrigger("Priority");
  await priorities.getByRole("option").last().click();
  await expect(bar).toBeHidden();
  expect((await ticketOf(api, id)).priority).toBe("urgent");
}

async function ticketOf(api: APIRequestContext, id: number): Promise<{ status: string; priority: string }> {
  const resp = await api.get(`/api/tickets/${id}`);
  expect(resp.status(), await resp.text()).toBe(200);
  return resp.json();
}

function column(page: Page, name: string): Locator {
  return page.locator(".board__column").filter({ has: page.getByRole("heading", { name, exact: true }) });
}

// A card in select mode: a toggle, named by its content like the link it
// replaces.
function selectCard(page: Page, title: string): Locator {
  return page.getByRole("button", { name: new RegExp(title) });
}

function actionBar(page: Page): Locator {
  return page.getByRole("region", { name: "Bulk actions" });
}

test.describe("wide window", () => {
  test.use({ viewport: WIDE });

  test("the bar's menus open upwards and reach their last option", async ({ page, api }) => {
    await expectBottomBarMenusReachable(page, api, WIDE);
  });

  test("parks several Preparing tickets in one action", async ({ page, api }) => {
    const stamp = Date.now();
    const titles = [1, 2, 3].map((n) => `bulk-park-${n}-${stamp}`);
    const ids = await Promise.all(titles.map((t) => fileTicket(api, t, "preparing")));
    const bystander = await fileTicket(api, `bulk-bystander-${stamp}`, "open");

    await page.goto("/");
    await expect(column(page, "Preparing").getByText(titles[0])).toBeVisible();
    await page.getByRole("button", { name: "Select", exact: true }).click();
    await page.getByRole("button", { name: "Select all in Preparing" }).click();
    await expect(actionBar(page).getByRole("status")).toHaveText("3 selected");

    await chooseFromDropdown(page, "Move to", "Parked");

    // Off the board (Parked is filtered out), and out of select mode.
    for (const title of titles) await expect(page.getByText(title)).toBeHidden();
    await expect(actionBar(page)).toBeHidden();
    await expect(page.getByRole("button", { name: "Select", exact: true })).toHaveAttribute("aria-pressed", "false");
    for (const id of ids) expect((await ticketOf(api, id)).status).toBe("parked");
    expect((await ticketOf(api, bystander)).status).toBe("open");
  });

  // The board offers only moves every selected ticket allows, so the
  // server refuses a batch only when the board is stale: here a ticket is
  // readied behind its back, and Open for the two selected is then wrong
  // for one of them.
  test("a mixed selection the server refuses leaves every ticket where it was", async ({ page, api }) => {
    const stamp = Date.now();
    const stillPreparing = `bulk-refused-a-${stamp}`;
    const readiedElsewhere = `bulk-refused-b-${stamp}`;
    const a = await fileTicket(api, stillPreparing, "preparing");
    const b = await fileTicket(api, readiedElsewhere, "preparing");

    // The board is live: left connected, the change below would reach it as
    // an event at an arbitrary moment, re-rendering the bar under the open
    // menu (or making the board current, so Open is no longer offered).
    // Without the stream the board stays stale until the refusal refreshes it.
    await page.route("**/api/tickets/stream*", (route) => route.abort());
    await page.goto("/");
    await page.getByRole("button", { name: "Select", exact: true }).click();
    await selectCard(page, stillPreparing).click();
    await selectCard(page, readiedElsewhere).click();
    await expect(actionBar(page).getByRole("status")).toHaveText("2 selected");

    const readied = await api.post(`/api/tickets/${b}/ready`);
    expect(readied.status(), await readied.text()).toBe(200);

    await chooseFromDropdown(page, "Move to", "Open");

    const alert = actionBar(page).getByRole("alert");
    await expect(alert).toContainText("nothing was changed");
    await expect(alert.getByRole("listitem")).toHaveCount(1);
    await expect(alert.getByRole("listitem")).toContainText(`#${b} open`);
    expect((await ticketOf(api, a)).status).toBe("preparing");
    expect((await ticketOf(api, b)).status).toBe("open");

    // The board refreshed, keeping the selection.
    await expect(column(page, "Open").getByText(readiedElsewhere)).toBeVisible();
    await expect(actionBar(page).getByRole("status")).toHaveText("2 selected");

    await alert.getByRole("button", { name: "Deselect the 1 that can't make this change" }).click();
    await expect(actionBar(page).getByRole("status")).toHaveText("1 selected");
    await chooseFromDropdown(page, "Move to", "Open");

    await expect(column(page, "Open").getByText(stillPreparing)).toBeVisible();
    expect((await ticketOf(api, a)).status).toBe("open");
  });
});

test.describe("narrow window", () => {
  test.use({ viewport: NARROW });

  test("the bar's menus open upwards and reach their last option", async ({ page, api }) => {
    await expectBottomBarMenusReachable(page, api, NARROW);
  });

  test("the selection carries across columns, and the action bar stays in reach", async ({ page, api }) => {
    const stamp = Date.now();
    const preparing = `bulk-phone-prep-${stamp}`;
    const open = `bulk-phone-open-${stamp}`;
    const ids = [await fileTicket(api, preparing, "preparing"), await fileTicket(api, open, "open")];
    // Enough cards that the Open column runs past the bottom of the phone.
    for (let n = 0; n < 12; n++) await fileTicket(api, `bulk-phone-filler-${n}-${stamp}`, "open");

    const chip = (name: string) =>
      page.getByRole("group", { name: "Board column" }).getByRole("button", { name: new RegExp(`^${name}`) });

    await page.goto("/");
    await page.getByRole("button", { name: "Select", exact: true }).click();
    await chip("Preparing").click();
    await selectCard(page, preparing).click();
    await chip("Open").click();
    await selectCard(page, open).click();
    await expect(actionBar(page).getByRole("status")).toHaveText("2 selected");

    // Sticky at the bottom of the screen -- above the page footer, which
    // takes the last strip of it -- even scrolled back to the top of a
    // column that runs well past it.
    const bar = actionBar(page);
    await page.getByRole("button", { name: "Select", exact: true }).scrollIntoViewIfNeeded();
    await expect(bar).toBeInViewport();
    const box = (await bar.boundingBox())!;
    const footer = (await page.getByRole("contentinfo").boundingBox())!;
    expect(footer.y + footer.height).toBeLessThanOrEqual(NARROW.height);
    expect(footer.height).toBeLessThan(60);
    expect(box.y + box.height).toBeLessThanOrEqual(footer.y);
    expect(box.y + box.height).toBeGreaterThan(footer.y - 40);

    await selectCard(page, `bulk-phone-filler-0-${stamp}`).click();
    await expect(bar.getByRole("status")).toHaveText("3 selected");
    await selectCard(page, `bulk-phone-filler-0-${stamp}`).click();

    await chooseFromDropdown(page, "Priority", "urgent");

    await expect(bar).toBeHidden();
    for (const id of ids) expect((await ticketOf(api, id)).priority).toBe("urgent");
  });
});
