import { describe, expect, it } from "vitest";
import { newTraceparent, parseTraceparent, requestTrace, traceparentMiddleware, TRACEPARENT_HEADER } from "./traceparent";

const ORIGIN = "http://app.test";
const mw = traceparentMiddleware({ origin: () => ORIGIN, includePrefixes: ["/api/"], excludePrefixes: ["/api/otel/"] });

function tagged(url: string): Request {
  const request = new Request(url);
  mw.onRequest({ request });
  return request;
}

describe("newTraceparent", () => {
  it("is a valid W3C traceparent", () => {
    const { header, traceId, spanId } = newTraceparent();
    expect(header).toMatch(/^00-[0-9a-f]{32}-[0-9a-f]{16}-01$/);
    expect(parseTraceparent(header)).toEqual({ traceId, spanId });
  });

  it("is new every time, so it names no user, session or page", () => {
    const ids = new Set(Array.from({ length: 50 }, () => newTraceparent().traceId));
    expect(ids.size).toBe(50);
  });
});

describe("parseTraceparent", () => {
  it.each([null, "", "garbage", "00-abc-def-01", `00-${"0".repeat(32)}-${"1".repeat(16)}-01x`,
    `00-${"0".repeat(32)}-${"1".repeat(16)}-01`,
    `00-${"1".repeat(32)}-${"0".repeat(16)}-01`,
  ])("rejects %j", (h) => {
    expect(parseTraceparent(h)).toBeUndefined();
  });
});

describe("traceparentMiddleware", () => {
  it("propagates a traceparent on a same-origin API request", () => {
    const request = tagged(`${ORIGIN}/api/things/42`);
    expect(parseTraceparent(request.headers.get(TRACEPARENT_HEADER))).toBeDefined();
  });

  it("gives each request its own trace id", () => {
    const a = tagged(`${ORIGIN}/api/things`).headers.get(TRACEPARENT_HEADER);
    const b = tagged(`${ORIGIN}/api/things`).headers.get(TRACEPARENT_HEADER);
    expect(a).not.toEqual(b);
  });

  it.each([
    ["another origin", "http://other.test/api/things"],
    ["a path outside the API", `${ORIGIN}/assets/app.js`],
    ["the OTel proxy itself", `${ORIGIN}/api/otel/v1/logs`],
    ["a lookalike host", "http://app.test.evil.example/api/things"],
  ])("does not tag %s", (_name, url) => {
    expect(tagged(url).headers.has(TRACEPARENT_HEADER)).toBe(false);
  });

  it("never throws into the request", () => {
    expect(() => mw.onRequest({ request: { url: "::bad", headers: new Headers() } as unknown as Request })).not.toThrow();
  });
});

describe("requestTrace", () => {
  it("reports the ids of a tagged request with when it started and ended", () => {
    const request = tagged(`${ORIGIN}/api/things`);
    const ids = parseTraceparent(request.headers.get(TRACEPARENT_HEADER));
    const trace = requestTrace(request, Date.now() + 250);
    expect(trace).toMatchObject(ids!);
    expect(trace!.endedAtMs - trace!.startedAtMs).toBeGreaterThanOrEqual(250);
  });

  it("is undefined for a request that was not tagged", () => {
    expect(requestTrace(new Request(`${ORIGIN}/api/otel/v1/logs`))).toBeUndefined();
  });
});
