// The ticket filter on a phone: one "Filters" row instead of every field laid
// out above the list. Layout needs a real browser (vitest applies no CSS), so
// this is where "the first ticket is on the first screen" can be shown.
import { execute } from "@/support/db";
import { expect, test } from "@/support/fixtures";
import { seedPendingProposals } from "@/support/proposals";
import { chooseFromDropdown } from "./support/widgets";

test.use({ viewport: { width: 390, height: 844 } });

test("the approvals list starts on the first screen, and the filter opens inline and narrows it", async ({ page, api }) => {
  await seedPendingProposals(api, 2);
  // seedPendingProposals names its tickets "proposal <i> <ms>", whose ms is its
  // own clock; match on the prefix and take the two it just made.
  await execute(
    `UPDATE tickets SET priority = 'urgent'
     WHERE id = (SELECT id FROM tickets WHERE title LIKE 'proposal 0 %' ORDER BY id DESC LIMIT 1)`,
  );

  await page.goto("/approvals");
  const toggle = page.getByRole("button", { name: /^Filters/ });
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await expect(page.getByRole("searchbox", { name: "Title or text" })).toBeHidden();

  const first = page.getByRole("group", { name: /^Proposal on #/ }).first();
  await expect(first).toBeVisible();
  const viewport = page.viewportSize()!;
  const box = (await first.boundingBox())!;
  expect(box.y, "the first ticket's top is on screen").toBeLessThan(viewport.height);
  expect(box.y + Math.min(box.height, 100), "and its head is not pushed off the bottom").toBeLessThan(viewport.height);

  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  await expect(page.getByRole("searchbox", { name: "Title or text" })).toBeVisible();

  const cards = page.getByRole("group", { name: /^Proposal on #/ });
  const before = await cards.count();
  expect(before).toBeGreaterThanOrEqual(2);
  await chooseFromDropdown(page, "Priority", "urgent");

  await expect(toggle).toHaveText(/Filters · 1/);
  await expect(cards).toHaveCount(1);
});
