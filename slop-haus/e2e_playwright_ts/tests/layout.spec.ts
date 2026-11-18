// Layout assertions. Like theme.spec.ts these have to live in the e2e
// suite: vitest runs with `css: false`, so jsdom applies no stylesheet and
// has no layout at all -- every box there is zero by zero.
//
// Each of these pins a specific way the panels were wrong, or went wrong
// while being fixed. None of them pin a pixel value, so a design change
// stays a design change rather than a test change.
import { expect, test } from "@/support/fixtures";
import { execute } from "@/support/db";
import type { APIRequestContext, Locator, Page } from "@playwright/test";
import { seedPendingProposals } from "@/support/proposals";
import { headerTab, openHeaderTab, openNewTicketForm } from "@/support/widgets";

const WIDE = { width: 1600, height: 900 };
// A common phone, portrait.
const NARROW = { width: 390, height: 844 };
// The usual guidance for something a finger has to hit.
const TOUCH_TARGET = 44;

// .app__main's padding, either side.
const GUTTERS = 48;

test.describe("wide window", () => {
  test.use({ viewport: WIDE });

  // The panels were capped at 720px and 640px, which on a wide monitor
  // left the roster -- the screen the human actually lives on -- in a
  // narrow strip with two thirds of the window empty beside it.
  test("the agents roster fills the window", async ({ page }) => {
    await page.goto("/");
    await openHeaderTab(page, "Workers");

    const roster = page.locator(".agents-panel");
    await expect(roster).toBeVisible();

    const box = (await roster.boundingBox())!;
    expect(box.width).toBeGreaterThan(WIDE.width - GUTTERS - 4);
  });
});

// The header is what makes the roster a table rather than a row of
// floating pills, and a header that does not sit over its own column is
// worse than none. Both are laid out from one grid definition, which is
// the only reason they agree.
//
// The rows alone aligned before this change too -- the name absorbed the
// slack -- so this deliberately measures the header against them rather
// than the rows against each other.
test("the roster header sits over the columns it names", async ({ page, api }) => {
  // Registering an agent needs the signed-in user `api` already is (see
  // support/fixtures.ts), not an admin token.
  for (const name of ["alpha", "a-considerably-longer-agent-name", "omega"]) {
    const resp = await api.post("/api/agents", { data: { name } });
    expect(resp.status(), await resp.text()).toBe(201);
  }

  await page.goto("/");
  await openHeaderTab(page, "Workers");
  await expect(page.locator(".agents-panel__row")).toHaveCount(3);

  const headerState = (await page.locator(".agents-panel__head span").nth(1).boundingBox())!;
  const rowStates = await page
    .locator(".agents-panel__row .agent-lifecycle__badge")
    .evaluateAll((els) => els.map((el) => el.getBoundingClientRect().left));

  expect(new Set(rowStates).size, `state column starts at ${rowStates.join(", ")}`).toBe(1);
  expect(
    Math.abs(rowStates[0] - headerState.x),
    `header "State" at ${headerState.x}, rows at ${rowStates[0]}`,
  ).toBeLessThan(2);
});

interface TicketText {
  body: string;
  comment: string;
  // Oldest first, as the page lists them: the last is the latest.
  handoffs?: string[];
}

async function ticketWithText(api: APIRequestContext, text: TicketText): Promise<string> {
  const title = `terminal text ${Date.now()}`;
  const resp = await api.post("/api/tickets", { data: { title, body: text.body } });
  expect(resp.status(), await resp.text()).toBe(201);
  const ticket = await resp.json();
  const commentResp = await api.post(`/api/tickets/${ticket.id}/comments`, { data: { body: text.comment } });
  expect(commentResp.status(), await commentResp.text()).toBe(201);

  if (text.handoffs?.length) {
    const agentResp = await api.post("/api/agents", {
      data: { name: `handoff-writer-${Date.now()}` },
    });
    expect(agentResp.status(), await agentResp.text()).toBe(201);
    const agent = await agentResp.json();
    // Written straight to the table: the only way in over the wire is an
    // agent's ticket_discuss over MCP, and what is under test here is how
    // the page lays a handoff out, not how one gets written. This connection
    // is the owner's, with no tenant set, so the row names the agent's
    // tenant itself rather than relying on the column's current_tenant()
    // default.
    for (const body of text.handoffs) {
      await execute(
        "INSERT INTO agent_contexts (tenant_id, ticket_id, attempt_number, agent_id, body) SELECT tenant_id, $1, 1, id, $3 FROM agents WHERE id = $2",
        [ticket.id, agent.id, body],
      );
    }
  }
  return title;
}

test.describe("ticket detail", () => {
  test.use({ viewport: WIDE });

  // It was capped at a 720px reading column, with a breakout only while
  // the body was being edited. It fills the window the way the new-ticket
  // form does.
  test("fills the window", async ({ page, api }) => {
    const title = await ticketWithText(api, { body: "body", comment: "comment" });

    await page.goto("/");
    await page.getByText(title).click();

    const detail = page.locator(".ticket-detail");
    await expect(detail).toBeVisible();

    const box = (await detail.boundingBox())!;
    expect(box.width).toBeGreaterThan(WIDE.width - GUTTERS - 4);
  });

  // In the plain view. Comments were a plain <p>, so every newline collapsed into one run-on
  // paragraph, and handoffs stayed in the proportional face after the body
  // and comments left it. All of them render as a terminal would: lines and
  // indentation kept, long paths wrapped rather than run off the page, and
  // in the same monospaced face the rest of the app uses for machine text.
  test("the body, comments and handoffs keep their lines, in a monospaced font", async ({ page, api }) => {
    // Far wider than the page and with no break opportunity in it, so only
    // overflow-wrap can bring it back inside the box. A slash-separated path
    // would not do: the browser will already break a line after a "/".
    const unbroken = "deadbeef".repeat(60);
    const text = (first: string) => `${first}\n    indented\n${unbroken}`;
    const title = await ticketWithText(api, {
      body: text("body line one"),
      comment: text("comment line one"),
      handoffs: [text("earlier handoff line one"), text("latest handoff line one")],
    });

    await page.goto("/");
    await page.getByText(title).click();
    // Rendered markdown folds single newlines like any markdown; the lines
    // and indentation are kept in the plain view.
    await page.getByRole("button", { name: "Plain" }).click();

    const mono = await page.evaluate(() => {
      const probe = document.createElement("span");
      probe.style.fontFamily = "var(--font-mono)";
      document.body.appendChild(probe);
      const family = getComputedStyle(probe).fontFamily;
      probe.remove();
      return family;
    });

    // Each tab is laid out only while it is on show, so each is measured
    // then: the main tab first, then every handoff on the agent context
    // tab, with the earlier one opened out of its fold.
    const history = page.locator(".agent-context-history__body");
    const tabs = [
      {
        open: async () => {
          // One panel on show at a time: the other is hidden rather than
          // unmounted, and the panel's own display must not undo that.
          await expect(page.locator(".agent-context-history")).toBeHidden();
        },
        texts: [
          [page.locator(".ticket-detail__body"), "body line one"],
          [page.locator(".ticket-detail__comments .ticket-detail__comment-body"), "comment line one"],
          [page.locator(".ticket-detail__handoff--latest .ticket-detail__handoff-body"), "latest handoff line one"],
        ],
      },
      {
        open: async () => {
          await page.getByRole("tab", { name: /Agent context history/ }).click();
          await expect(page.locator(".ticket-detail__body")).toBeHidden();
          await page.locator(".agent-context-history__preview", { hasText: "earlier handoff line one" }).click();
        },
        texts: [
          [history.first(), "latest handoff line one"],
          [history.last(), "earlier handoff line one"],
        ],
      },
    ] as const;

    for (const tab of tabs) {
      await tab.open();
      for (const [text, first] of tab.texts) {
        await expect(text).toContainText(first);

        // Two lines tall. Measured in the element's own font size, so this
        // holds whatever the size is: one line of normal line-height is
        // about 1.2 of it, two are about 2.4.
        const { height, fontSize } = await text.evaluate((el) => ({
          height: el.getBoundingClientRect().height,
          fontSize: parseFloat(getComputedStyle(el).fontSize),
        }));
        expect(height, `${first}: the newline should start a second line`).toBeGreaterThan(fontSize * 1.8);

        // ...and the indentation survives: the second line's text starts
        // well in from the edge rather than being collapsed back to it.
        const indent = await text.evaluate((el) => {
          const node = el.firstChild!;
          const range = document.createRange();
          const at = node.textContent!.indexOf("indented");
          range.setStart(node, at);
          range.setEnd(node, at + 1);
          return {
            by: range.getBoundingClientRect().left - el.getBoundingClientRect().left,
            fontSize: parseFloat(getComputedStyle(el).fontSize),
          };
        });
        expect(indent.by, `${first}: leading spaces should indent the line`).toBeGreaterThan(indent.fontSize);

        // Measured on the text itself against the detail panel, not on the
        // element: the body's <p> is a flex item that grows to fit its
        // content, so its own scrollWidth never shows the overflow.
        const overflow = await text.evaluate((el) => {
          const range = document.createRange();
          range.selectNodeContents(el);
          return range.getBoundingClientRect().right - el.closest(".ticket-detail")!.getBoundingClientRect().right;
        });
        expect(overflow, `${first}: a long unbroken token should wrap inside the panel`).toBeLessThanOrEqual(0);

        await expect(text).toHaveCSS("font-family", mono);
      }
    }
  });
});

// The phone layout. Nothing here pins how it looks beyond what makes it
// usable at all on a phone: nothing runs off the side, the header does not
// eat the screen, the board fits, and the controls are finger-sized.
test.describe("narrow window", () => {
  test.use({ viewport: NARROW });

  // Sideways scrolling is the page being wider than the phone. Measured on
  // the document and on .app__main, which is what scrolls: a box that
  // scrolls its own overflow (a code block) is fine, and not counted.
  async function expectNoSidewaysScroll(page: Page, what: string): Promise<void> {
    const widths = await page.evaluate(() => {
      const main = document.querySelector(".app__main")!;
      return {
        page: document.documentElement.scrollWidth - document.documentElement.clientWidth,
        main: main.scrollWidth - main.clientWidth,
      };
    });
    expect(widths, `${what} should not scroll sideways`).toEqual({ page: 0, main: 0 });
  }

  async function fileTicket(api: APIRequestContext, title: string, status?: string): Promise<number> {
    const resp = await api.post("/api/tickets", { data: { title } });
    expect(resp.status(), await resp.text()).toBe(201);
    const { id } = await resp.json();
    if (status) {
      const patch = await api.patch(`/api/tickets/${id}`, { data: { status } });
      expect(patch.status(), await patch.text()).toBe(200);
    }
    return id;
  }

  async function expectFingerSized(control: Locator, what: string): Promise<void> {
    await expect(control).toBeVisible();
    const box = (await control.boundingBox())!;
    expect(box.height, `${what} should be finger-sized`).toBeGreaterThanOrEqual(TOUCH_TARGET);
    expect(box.width, `${what} should be finger-sized`).toBeGreaterThanOrEqual(TOUCH_TARGET);
  }

  // The desktop header wraps to three or four rows at this width, and it
  // never scrolls away.
  test("the header is one row, with the tabs in its menu", async ({ page }) => {
    await page.goto("/");

    const header = page.locator(".app__header");
    const menu = page.getByRole("button", { name: "Menu" });
    const title = page.getByRole("heading", { name: "slop-haus" });
    const processing = page.getByRole("switch", { name: "Ticket processing" });
    await expect(processing).toBeVisible();
    await expect(headerTab(page, "Workers")).toBeHidden();

    // One row: every item's vertical middle inside every other item's box.
    const boxes = await Promise.all([title, processing, menu].map(async (l) => (await l.boundingBox())!));
    const headerBox = (await header.boundingBox())!;
    for (const box of boxes) {
      const middle = box.y + box.height / 2;
      for (const other of boxes) {
        expect(middle).toBeGreaterThan(other.y);
        expect(middle).toBeLessThan(other.y + other.height);
      }
    }
    expect(headerBox.height, "the header should be one row tall").toBeLessThan(TOUCH_TARGET * 1.6);
    await expectFingerSized(menu, "the menu button");

    await menu.click();
    await expectNoSidewaysScroll(page, "the page with the menu open");
    await expectFingerSized(headerTab(page, "Workers"), "a tab in the menu");
    await expectFingerSized(page.getByRole("button", { name: "Sign out" }), "Sign out");

    await headerTab(page, "Workers").click();
    await expect(page).toHaveURL(/\/workers$/);
    await expect(headerTab(page, "Workers")).toBeHidden();
  });

  test("the board shows one column at a time, picked by its chips", async ({ page, api }) => {
    await fileTicket(api, "narrow open ticket");
    await fileTicket(api, "narrow review ticket", "review");

    await page.goto("/");

    const columns = page.locator(".board__column");
    const chips = page.getByRole("group", { name: "Board column" });
    // Nothing in progress, so it opens on the first column after with
    // anything in it.
    await expect(chips.getByRole("button", { name: /^Review/ })).toHaveAttribute("aria-pressed", "true");
    await expect(columns.filter({ visible: true })).toHaveCount(1);
    await expect(page.getByText("narrow review ticket")).toBeVisible();
    await expect(page.getByText("narrow open ticket")).toBeHidden();

    const openChip = chips.getByRole("button", { name: /^Open/ });
    await expectFingerSized(openChip, "a column chip");
    await openChip.click();
    await expect(page).toHaveURL(/\?column=open$/);
    await expect(columns.filter({ visible: true })).toHaveCount(1);
    await expect(page.getByText("narrow open ticket")).toBeVisible();
    await expect(page.getByText("narrow review ticket")).toBeHidden();

    // The column only ever fills the screen's width, never more.
    const column = (await columns.filter({ visible: true }).boundingBox())!;
    expect(column.x).toBeGreaterThanOrEqual(0);
    expect(column.x + column.width).toBeLessThanOrEqual(NARROW.width);
    await expectNoSidewaysScroll(page, "the board");

    // A reload keeps the column.
    await page.reload();
    await expect(chips.getByRole("button", { name: /^Open/ })).toHaveAttribute("aria-pressed", "true");
  });

  // Tapping a submit button submits, so its tooltip is no explanation on a
  // phone; the same words show under the buttons instead.
  test("the new-ticket form explains its submit buttons without hover", async ({ page }) => {
    await page.goto("/");
    await openNewTicketForm(page);

    const hints = page.locator(".new-ticket-form__submit-hints");
    await expect(hints).toBeVisible();
    await expect(hints).toContainText("the next free agent may pick it up");
    await expectNoSidewaysScroll(page, "the new-ticket form");
  });

  // The fields row alone was two 170px dropdowns and two checkboxes, and
  // the banners' buttons would not wrap.
  test("a ticket fits the screen, with finger-sized controls", async ({ page, api }) => {
    // Preparing, for the banner with its Open button.
    const title = "a ticket on a phone";
    const id = await fileTicket(api, title, "preparing");

    await page.goto(`/tickets/${id}?from=board`);
    await expect(page.getByRole("heading", { name: new RegExp(title) })).toBeVisible();

    await expectNoSidewaysScroll(page, "the ticket");
    await expectFingerSized(page.getByRole("combobox", { name: "Status" }), "the status picker");
    await expectFingerSized(page.getByRole("button", { name: "Open" }), "the banner's Open button");
    await expectFingerSized(page.getByRole("button", { name: "Post comment" }), "Post comment");

    // Stacked: the status picker is the full width of the page's content.
    const status = (await page.getByRole("combobox", { name: "Status" }).boundingBox())!;
    const detail = (await page.locator(".ticket-detail").boundingBox())!;
    expect(status.width).toBeGreaterThan(detail.width - 2);

    // iOS zooms the page in on focusing a field under 16px.
    await expect(page.getByLabel("Add a comment")).toHaveCSS("font-size", "16px");
  });
});

// The phone layout's markup is on the page at every width (the chips, the
// column marked shown); a wide window must not show any of it.
test("a wide window still shows every board column, and no chips", async ({ page }) => {
  await page.setViewportSize(WIDE);
  await page.goto("/?column=open");

  await expect(page.locator(".board__column-header h2").first()).toBeVisible();
  await expect(page.locator(".board__column").filter({ visible: true })).toHaveCount(6);
  await expect(page.getByRole("group", { name: "Board column" })).toBeHidden();
  await expect(page.getByRole("button", { name: "Menu" })).toHaveCount(0);
});

// The Blocked and Unreviewed pages share one list (paged_ticket_list), which
// fills the window down to its "Load more". Every level from .app__main to the
// viewport has to be a flex column with min-height: 0 for that to hold; one
// missing and the box falls back to its floor, or the page scrolls as a whole.
test.describe("paged ticket list fills the window", () => {
  // Main's bottom padding, plus a little for the borders.
  const SLACK = 24 + 8;
  // ticket_query.ts's DEFAULT_PAGE_SIZE.
  const PAGE_SIZE = 50;
  const FILE_BATCH = 8;

  for (const height of [700, 1000]) {
    test(`Blocked: Load more ends just above the footer at ${height}px high`, async ({ page, api }) => {
      await page.setViewportSize({ width: 1280, height });
      const stamp = Date.now();
      const blocker = await (await api.post("/api/tickets", { data: { title: `blocker ${stamp}` } })).json();
      // One more than a page, so the list has a Load more. Filed in small
      // batches: `api` sends Connection: close, so every request opens a new
      // socket, and a burst of PAGE_SIZE+1 at once got one reset ("socket hang
      // up") on pipeline 11523.
      const fileDependent = async (i: number) => {
        const dependent = await (await api.post("/api/tickets", { data: { title: `waits ${i} ${stamp}` } })).json();
        const resp = await api.post(`/api/tickets/${dependent.id}/blockers`, { data: { blocker_id: blocker.id } });
        expect(resp.status(), await resp.text()).toBe(200);
      };
      for (let start = 0; start <= PAGE_SIZE; start += FILE_BATCH) {
        const batch = Array.from({ length: Math.min(FILE_BATCH, PAGE_SIZE + 1 - start) }, (_, i) => start + i);
        await Promise.all(batch.map(fileDependent));
      }

      await page.goto("/blocked");
      const loadMore = page.getByRole("button", { name: "Load more" });
      await expect(loadMore).toBeVisible();

      const loadMoreBox = (await loadMore.boundingBox())!;
      const viewportBox = (await page.locator(".virtual-list__viewport").boundingBox())!;
      const footerBox = (await page.locator(".app-footer").boundingBox())!;
      expect(footerBox.y - (loadMoreBox.y + loadMoreBox.height), "gap between Load more and the footer").toBeLessThan(SLACK);
      expect(viewportBox.height, "the list is more than its old fixed height").toBeGreaterThan(height === 1000 ? 480 : 240);

      // The rows scroll inside the list; the page does not scroll as a whole.
      const main = await page.locator(".app__main").evaluate((el) => ({ scroll: el.scrollHeight, client: el.clientHeight }));
      expect(main.scroll).toBeLessThanOrEqual(main.client + 1);
    });
  }
});

// The Approvals page puts a VirtualList of its own in the same chain, under
// batch controls that change height: the list gives up what they take, down to
// the page's floor of 480px for a tall card.
test.describe("approvals list fills the window", () => {
  const SLACK = 24 + 8;
  // use_pending_proposals.ts's PAGE_SIZE.
  const PAGE_SIZE = 50;

  async function measure(page: Page) {
    const loadMoreBox = (await page.getByRole("button", { name: "Load more" }).boundingBox())!;
    const viewportBox = (await page.locator(".virtual-list__viewport").boundingBox())!;
    const footerBox = (await page.locator(".app-footer").boundingBox())!;
    return { viewport: viewportBox.height, gapToFooter: footerBox.y - (loadMoreBox.y + loadMoreBox.height) };
  }

  // The list sizes itself after Load more is first visible (the batch controls
  // and rows settle), so a single read caught it 29px under the footer on
  // pipeline 11961. Read until several reads in a row agree.
  const SETTLED_READS = 3;
  async function measureSettled(page: Page) {
    let previous = "";
    let agreeing = 0;
    await expect
      .poll(async () => {
        const current = JSON.stringify(await measure(page));
        agreeing = current === previous ? agreeing + 1 : 1;
        previous = current;
        return agreeing;
      })
      .toBeGreaterThanOrEqual(SETTLED_READS);
    return JSON.parse(previous) as Awaited<ReturnType<typeof measure>>;
  }

  // Tall enough that the list stays above its floor even when the filter's
  // fields wrap to a second row, which a new status in the Statuses summary
  // is enough to cause at this width.
  const TALL = 1100;
  for (const height of [700, TALL]) {
    test(`Load more ends just above the footer at ${height}px high`, async ({ page, api }) => {
      await page.setViewportSize({ width: 1280, height });
      await seedPendingProposals(api, PAGE_SIZE + 1);

      await page.goto("/approvals");
      await expect(page.getByRole("button", { name: "Load more" })).toBeVisible();

      const closed = await measureSettled(page);
      expect(closed.gapToFooter, "gap between Load more and the footer").toBeLessThan(SLACK);
      // At 700px the window leaves less than the floor, so the page scrolls instead.
      expect(closed.viewport, "the list is at least its floor").toBeGreaterThanOrEqual(480 - 1);
      if (height === TALL) {
        // Past the footer would also be under SLACK, so say it is not.
        expect(closed.gapToFooter, "Load more is not under the footer").toBeGreaterThanOrEqual(-1);
        expect(closed.viewport, "the list is taller than its floor").toBeGreaterThan(480);
        const main = await page.locator(".app__main").evaluate((el) => ({ scroll: el.scrollHeight, client: el.clientHeight }));
        expect(main.scroll).toBeLessThanOrEqual(main.client + 1);

        // The reject form takes height from the list, not from the footer.
        await page.getByRole("button", { name: /^Reject all/ }).click();
        await expect(page.getByRole("button", { name: /^Reject all .* 50$/ })).toBeVisible();
        const open = await measureSettled(page);
        expect(open.gapToFooter, "gap between Load more and the footer, form open").toBeLessThan(SLACK);
        expect(open.viewport, "the list gave up height to the form").toBeLessThan(closed.viewport);
      }
    });
  }
});
