// A same-origin /api request from the real page carries a W3C traceparent
// through nginx to the server, and the frontend's report of a failed one
// carries the same trace id (the id the server's logs for it are found by).
// The telemetry proxy's own request carries none.
import type { Request } from "@playwright/test";

import { expect, test } from "@/support/fixtures";

const TRACEPARENT = /^00-([0-9a-f]{32})-([0-9a-f]{16})-[0-9a-f]{2}$/;

function attributesOf(request: Request): Record<string, string | undefined> {
  const body = request.postDataJSON();
  const records = body.resourceLogs.flatMap((r: { scopeLogs: Array<{ logRecords: never[] }> }) => r.scopeLogs.flatMap((s) => s.logRecords));
  return Object.fromEntries(
    records.flatMap((rec: { attributes: Array<{ key: string; value: { stringValue?: string } }> }) => rec.attributes.map((a) => [a.key, a.value.stringValue])),
  );
}

test("a failed request's traceparent is the trace id its error report carries", async ({ anonPage }) => {
  let traceparent: string | undefined;
  await anonPage.route("**/api/auth/config", async (route) => {
    traceparent = route.request().headers()["traceparent"];
    await route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ code: "e2e_forced" }) });
  });
  const report = anonPage.waitForRequest((req) => req.url().endsWith("/api/otel/v1/logs") && req.method() === "POST" && req.postData()?.includes("e2e_forced") === true, {
    timeout: 30_000,
  });

  await anonPage.goto("/login");

  const request = await report;
  const match = TRACEPARENT.exec(traceparent ?? "");
  expect(match, `traceparent ${traceparent}`).not.toBeNull();
  expect(attributesOf(request).trace_id).toBe(match![1]);
  expect(request.headers()["traceparent"], "the telemetry proxy is called without one").toBeUndefined();
});
