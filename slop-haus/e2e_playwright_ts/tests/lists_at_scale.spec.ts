import type { Page, Request } from "@playwright/test";

import { test as base, expect } from "./support/fixtures";

// The browser half of the lists-at-scale guard (the server half is
// server/internal/schema/seeddata/scaleperf). It runs only as the "scale"
// Playwright project, against a tenant seeded with 5,000 tickets by
// cmd/seed_scale: `SCALE=1 slop-haus/e2e_playwright_ts/test.sh`.
//
// It counts, never times: how many rows a list puts in the DOM, and how many
// /api requests it makes to get there. A list that draws everything it was
// sent, or walks every cursor, fails however fast the machine is.

// cleanDatabase would truncate the seed before the first test, and this
// project's tests only read it.
const test = base.extend({
  cleanDatabase: [async ({}, use) => use(), { auto: true }],
});

// seeddata.SeedScale's words: about one ticket in a hundred, and most.
const RARE_WORD = "zebrafish";
const COMMON_WORD = "importer";

interface Limits {
  // The most rows the list may have in the DOM at once. A screenful plus
  // overscan is a dozen or so; a page of 50 drawn whole must fail.
  maxRows: number;
  // The most /api requests from navigation to a settled list. One page per
  // list plus the page's own (session, config): walking the cursors would be
  // a hundred more.
  maxRequests: number;
}

const LIMITS: Limits = { maxRows: 30, maxRequests: 20 };

interface Measure {
  rows: number;
  requests: number;
}

// measure opens path and reports what the list drew and asked for once it has
// settled: its rows are up and no /api request is still in flight. rows is
// the selector of one row of the list.
//
// Not waitForLoadState("networkidle"): the page holds a live-events stream
// open for good, so the network is never idle.
async function measure(page: Page, path: string, rows: string): Promise<Measure> {
  let requests = 0;
  const inFlight = new Set<Request>();
  const isCounted = (req: Request) => new URL(req.url()).pathname.startsWith("/api/") && req.resourceType() !== "eventsource";
  page.on("request", (req) => {
    if (!isCounted(req)) return;
    requests++;
    inFlight.add(req);
  });
  const done = (req: Request) => void inFlight.delete(req);
  page.on("requestfinished", done);
  page.on("requestfailed", done);

  await page.goto(path);
  await expect(page.locator(rows).first()).toBeVisible();
  // A load that follows the last one starts in an effect after the render
  // that brought it in, so being quiet once is not being settled: settled is
  // two looks, a frame or more apart, with nothing in flight and no new
  // request between them.
  let seen = -1;
  await expect
    .poll(
      async () => {
        await page.evaluate(() => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))));
        const settled = inFlight.size === 0 && requests === seen;
        seen = requests;
        return settled;
      },
      { message: "/api requests still arriving" },
    )
    .toBe(true);
  return { rows: await page.locator(rows).count(), requests };
}

const VIRTUAL_ROW = ".virtual-list__row";

const lists: { name: string; path: string; rows: string; limits?: Limits; knownOver?: string }[] = [
  { name: "Board with done", path: "/?done=true", rows: VIRTUAL_ROW },
  { name: "Unreviewed", path: "/unreviewed", rows: VIRTUAL_ROW },
  { name: "Reports, by ticket", path: "/reports", rows: `[aria-label="LLM usage by ticket"] ${VIRTUAL_ROW}` },
  { name: "text search, rare word", path: `/?done=true&q=${RARE_WORD}`, rows: VIRTUAL_ROW },
  { name: "text search, common word", path: `/?done=true&q=${COMMON_WORD}`, rows: VIRTUAL_ROW },
  { name: "Blocked", path: "/blocked", rows: VIRTUAL_ROW },
  { name: "Approvals", path: "/approvals", rows: ".approvals__card" },
];

for (const list of lists) {
  test(`${list.name} draws a screenful and asks for a page`, async ({ page }) => {
    const limits = list.limits ?? LIMITS;
    const got = await measure(page, list.path, list.rows);
    console.log(`${list.name}: ${got.rows} rows, ${got.requests} requests`);
    if (list.knownOver) {
      // This list draws every row it is sent (unwindowed), which the guard
      // exists to catch. Only the row ceiling is inverted, so that the list
      // still has to load, within the request ceiling, and a fix that lands
      // fails here until its ticket removes knownOver.
      expect(got.rows, `rows in the DOM; ${list.knownOver} windows this list, remove knownOver then`).toBeGreaterThan(limits.maxRows);
    } else {
      expect(got.rows, "rows in the DOM").toBeLessThanOrEqual(limits.maxRows);
    }
    expect(got.requests, "/api requests").toBeLessThanOrEqual(limits.maxRequests);
  });
}
