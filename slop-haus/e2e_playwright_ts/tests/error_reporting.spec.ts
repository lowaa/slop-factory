// A frontend error reaches the server's own /api/otel proxy, through the
// real browser, session and nginx: the proxy accepts it (204), the request
// carries the session's CSRF token (the route authenticates the session, so
// a cookie-authenticated write is checked like any other), and what leaves
// the page is the operational tier and nothing the error said.
import type { Page, Request } from "@playwright/test";

import { expect, test } from "@/support/fixtures";

const SECRET = "ticket about Jane Doe, hemoglobin 13.5";

// The exporter batches, so the report goes out a few seconds after the error.
function nextReport(page: Page): Promise<Request> {
  return page.waitForRequest((req) => req.url().endsWith("/api/otel/v1/logs") && req.method() === "POST", { timeout: 30_000 });
}

async function throwUncaught(page: Page): Promise<void> {
  await page.evaluate((message) => {
    setTimeout(() => {
      throw new Error(message);
    }, 0);
  }, SECRET);
}

function recordsOf(request: Request): Array<{ severityNumber: number; attributes: Array<{ key: string; value: Record<string, unknown> }> }> {
  const body = request.postDataJSON();
  return body.resourceLogs.flatMap((r: { scopeLogs: Array<{ logRecords: never[] }> }) => r.scopeLogs.flatMap((s) => s.logRecords));
}

test("a forced frontend error is reported through the server's proxy, within the operational tier", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "slop-haus" })).toBeVisible();

  const reported = nextReport(page);
  await throwUncaught(page);
  const request = await reported;
  const response = await request.response();

  expect(response?.status()).toBe(204);
  expect(request.headers()["x-csrf-token"], "a signed-in write carries the session's CSRF token").toBeTruthy();
  const records = recordsOf(request);
  expect(records).toHaveLength(1);
  expect(records[0].severityNumber).toBe(17);
  const keys = Object.fromEntries(records[0].attributes.map((a) => [a.key, a.value.stringValue]));
  expect(keys).toMatchObject({ "privacy.tier": "operational", "error.type": "Error", "error.message": "message withheld" });
  expect(request.postData()).not.toContain("Jane");
  expect(request.postData()).not.toContain("13.5");
});

test("a visitor with no session is reported too", async ({ anonPage }) => {
  await anonPage.goto("/login");
  await expect(anonPage.getByRole("heading", { name: "Sign in" })).toBeVisible();

  const reported = nextReport(anonPage);
  await throwUncaught(anonPage);
  const request = await reported;

  expect((await request.response())?.status()).toBe(204);
  expect(request.headers()["x-csrf-token"]).toBeUndefined();
});
