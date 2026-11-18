// Browser-driven test for slop-haus/frontend, proving the real UI works
// against the real backend end-to-end -- the UI counterpart to
// api.spec.ts's direct-to-API coverage.
import { expect, test } from "@/support/fixtures";
import { chooseFromDropdown, openNewTicketForm } from "@/support/widgets";

test("files a ticket via the form, sees it on the board, opens it, and adds a comment", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "File a ticket" })).toBeVisible();

  await openNewTicketForm(page);
  await page.getByLabel("Title").fill("Wire up leases");
  await page.getByLabel("Body").fill("Add the claim path");
  await chooseFromDropdown(page, "Priority", "high");
  await page.getByLabel("Path scopes (one per line)").fill("server/internal/lease/");
  // Not "Preparing" -- this ticket is filed ready for the queue, and the
  // board column check right below relies on it landing straight in Open.
  await page.getByRole("button", { name: "Open", exact: true }).click();

  // Filed via the "Open" button, so the new card must land in the board's
  // Open column specifically, not the Preparing one it would have landed
  // in from the other button, or anywhere else on the page.
  const openColumn = page.locator(".board__column").filter({ has: page.getByRole("heading", { name: "Open" }) });
  await expect(openColumn.getByText("Wire up leases")).toBeVisible();

  await page.getByText("Wire up leases").click();
  await expect(page.getByRole("heading", { name: /Wire up leases/ })).toBeVisible();
  await expect(page.getByText("Add the claim path")).toBeVisible();
  await expect(page.getByText("server/internal/lease/")).toBeVisible();

  await expect(page.getByText("No comments yet.")).toBeVisible();
  await page.getByLabel("Add a comment").fill("Started on this");
  await page.getByRole("button", { name: "Post comment" }).click();

  await expect(page.getByText("Started on this")).toBeVisible();
  await expect(page.getByText("No comments yet.")).not.toBeVisible();
  // The instant itself is the server's, so only its presence is checked --
  // the unit tests pin down which value lands where.
  const comment = page.getByRole("listitem").filter({ hasText: "Started on this" });
  await expect(comment.locator("time")).toHaveAttribute("datetime", /^\d{4}-\d{2}-\d{2}T/);
});

test("the detail view shows when a ticket was created and updated, and when it closes", async ({ page }) => {
  await page.goto("/");

  await openNewTicketForm(page);
  await page.getByLabel("Title").fill("Stamp me");
  await page.getByRole("button", { name: "Open", exact: true }).click();
  await page.getByText("Stamp me").click();
  await expect(page.getByRole("heading", { name: /Stamp me/ })).toBeVisible();

  const timestamps = page.locator(".ticket-detail__timestamps");
  await expect(timestamps.getByText("Created")).toBeVisible();
  await expect(timestamps.getByText("Updated")).toBeVisible();
  await expect(timestamps.getByText("Closed")).toHaveCount(0);

  await chooseFromDropdown(page, "Status", "done");
  await expect(timestamps.getByText("Closed")).toBeVisible();
  await expect(timestamps.locator("time")).toHaveCount(3);
});

test("changing status in the detail view moves the ticket to a different board column on return", async ({ page }) => {
  await page.goto("/");

  await openNewTicketForm(page);
  await page.getByLabel("Title").fill("Move me");
  await page.getByRole("button", { name: "Open", exact: true }).click();

  const openColumn = page.locator(".board__column").filter({ has: page.getByRole("heading", { name: "Open" }) });
  await expect(openColumn.getByText("Move me")).toBeVisible();

  await page.getByText("Move me").click();
  await expect(page.getByRole("heading", { name: /Move me/ })).toBeVisible();
  await chooseFromDropdown(page, "Status", "in progress");

  await page.getByRole("link", { name: "← Back to board" }).click();

  const inProgressColumn = page
    .locator(".board__column")
    .filter({ has: page.getByRole("heading", { name: "In progress" }) });
  await expect(inProgressColumn.getByText("Move me")).toBeVisible();
  await expect(openColumn.getByText("Move me")).not.toBeVisible();
});

// Checked in a real browser because the collapse is CSS as much as markup:
// an author `display` on the fields would override [hidden] and leave them
// showing, which jsdom (no stylesheets) cannot see.
test("the ticket form starts collapsed and stays open while filing several tickets", async ({ page }) => {
  await page.goto("/");

  const toggle = page.getByRole("button", { name: "File a ticket" });
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await expect(page.getByLabel("Title")).toBeHidden();
  await expect(page.getByRole("button", { name: "Preparing", exact: true })).toBeHidden();

  await openNewTicketForm(page);

  for (const button of ["Preparing", "Open"]) {
    const title = `filed-with-${button}-${Date.now()}`;
    await page.getByLabel("Title").fill(title);
    await page.getByRole("button", { name: button, exact: true }).click();
    await expect(page.locator(".board__column").getByText(title)).toBeVisible();

    await expect(toggle).toHaveAttribute("aria-expanded", "true");
    await expect(page.getByLabel("Title")).toBeVisible();
    await expect(page.getByLabel("Title")).toHaveValue("");
  }
});

// Against the real server, so the ?status= filter the board sends is the
// one the API actually applies, not a mock's idea of it.
test("the board leaves done tickets out until Include done is ticked", async ({ page }) => {
  const title = `finish-me-${Date.now()}`;
  await page.goto("/");

  await openNewTicketForm(page);
  await page.getByLabel("Title").fill(title);
  await page.getByRole("button", { name: "Open", exact: true }).click();
  await page.getByText(title).click();
  await chooseFromDropdown(page, "Status", "done");
  await expect(page.locator(".ticket-detail__timestamps").getByText("Closed")).toBeVisible();
  await page.getByRole("link", { name: "← Back to board" }).click();

  const includeDone = page.getByRole("checkbox", { name: "Include done" });
  await expect(includeDone).not.toBeChecked();
  await expect(page.getByRole("heading", { name: "Open", exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Done", exact: true })).toHaveCount(0);
  await expect(page.getByText(title)).toHaveCount(0);

  await includeDone.check();
  const doneColumn = page.locator(".board__column").filter({ has: page.getByRole("heading", { name: "Done" }) });
  await expect(doneColumn.getByText(title)).toBeVisible();

  await includeDone.uncheck();
  await expect(page.getByRole("heading", { name: "Done", exact: true })).toHaveCount(0);
  await expect(page.getByText(title)).toHaveCount(0);
});

// Against the real server: the words are matched by the API's query, not
// in the page, and a comment -- which the board's list never carries -- is
// searched too. The filter is in the address, so a reload keeps it, and a
// ticket's Back link returns to it.
test("the board's text filter finds a ticket by its comment and survives a reload and a ticket's Back link", async ({
  page,
}) => {
  const run = Date.now();
  const commented = `commented-${run}`;
  const other = `other-${run}`;
  const word = `kumquat${run}`;
  await page.goto("/");

  await openNewTicketForm(page);
  await page.getByLabel("Title").fill(other);
  await page.getByRole("button", { name: "Open", exact: true }).click();
  await expect(page.getByText(other)).toBeVisible();
  await page.getByLabel("Title").fill(commented);
  await page.getByRole("button", { name: "Open", exact: true }).click();
  await page.getByText(commented).click();
  await page.getByLabel("Add a comment").fill(`the ${word} importer`);
  await page.getByRole("button", { name: "Post comment" }).click();
  await expect(page.getByText(`the ${word} importer`)).toBeVisible();
  await page.getByRole("link", { name: "← Back to board" }).click();
  await expect(page.getByText(other)).toBeVisible();

  await page.getByRole("searchbox", { name: "Search tickets" }).fill(`${word.toUpperCase()} importer`);

  await expect(page.getByText(other)).toHaveCount(0);
  await expect(page.getByText(commented)).toBeVisible();
  await expect(page).toHaveURL(new RegExp(`[?&]q=${word.toUpperCase()}\\+importer`));

  await page.reload();
  await expect(page.getByRole("searchbox", { name: "Search tickets" })).toHaveValue(`${word.toUpperCase()} importer`);
  await expect(page.getByText(commented)).toBeVisible();
  await expect(page.getByText(other)).toHaveCount(0);

  await page.getByText(commented).click();
  await page.getByRole("link", { name: "← Back to board" }).click();
  await expect(page).toHaveURL(new RegExp(`[?&]q=${word.toUpperCase()}\\+importer`));
  await expect(page.getByRole("searchbox", { name: "Search tickets" })).toHaveValue(`${word.toUpperCase()} importer`);
  await expect(page.getByText(commented)).toBeVisible();
  await expect(page.getByText(other)).toHaveCount(0);
});

// "#N" in the search box offers ticket N itself, looked up by the real
// server in the session's tenant, and Enter opens it.
test("typing #<number> into the board's search and pressing Enter opens that ticket", async ({ page, api }) => {
  const created = await api.post("/api/tickets", { data: { title: "Jump target" } });
  expect(created.status(), await created.text()).toBe(201);
  const { id } = await created.json();
  await api.post("/api/tickets", { data: { title: "Not the target" } });
  await page.goto("/");

  // The board keeps showing its unfiltered cards until the filtered list
  // lands, so the target's own card -- a link with the same number and
  // title -- is on screen beside the offer. The offer is looked for in its
  // own region: a page-wide match either hit both (a strict-mode failure)
  // or passed on the card alone before the offer arrived. Having the card
  // up before typing and holding the filtered list back makes that overlap
  // certain on every run rather than only on some.
  await expect(page.locator(".ticket-card__select", { hasText: "Jump target" })).toBeVisible();
  let releaseFilteredBoard!: () => void;
  const filteredBoardHeld = new Promise<void>((resolve) => {
    releaseFilteredBoard = resolve;
  });
  await page.route(
    (url) => url.pathname === "/api/tickets" && url.searchParams.has("q"),
    async (route) => {
      await filteredBoardHeld;
      await route.continue();
    },
  );

  const search = page.getByRole("searchbox", { name: "Search tickets" });
  await search.fill(`#${id}`);
  const offer = page.locator(".ticket-jump").getByRole("link", { name: new RegExp(`#${id}.*Jump target`) });
  await expect(offer).toBeVisible();
  releaseFilteredBoard();
  await search.press("Enter");

  await expect(page).toHaveURL(new RegExp(`/tickets/${id}\\?.*from=board`));
  await expect(page.getByRole("heading", { name: /Jump target/ })).toBeVisible();
});
