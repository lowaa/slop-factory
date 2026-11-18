import { describe, expect, it } from "vitest";
import { SpanReporter, type SpanReporterOptions } from "./span_reporter";
import type { ApiFailure } from "../error_reporting/api_failures";

const ALLOWLIST = { operational: ["http.request.method", "http.route", "http.response.status_code", "error.code"] };

const failure: ApiFailure = {
  method: "post",
  route: "/api/invoices/{id}",
  status: 500,
  code: "internal_error",
  trace: { traceId: "4bf92f3577b34da6a3ce929d0e0e4736", spanId: "00f067aa0ba902b7", startedAtMs: 1_700_000_000_000, endedAtMs: 1_700_000_000_250 },
};

function setup(overrides: Partial<SpanReporterOptions> = {}) {
  const sent: { url: string; init: RequestInit }[] = [];
  const reporter = new SpanReporter({
    serviceName: "svc",
    collectorPublicUrl: "/api/otel",
    attributeAllowlist: ALLOWLIST,
    headers: () => ({ "X-CSRF-Token": "t" }),
    fetchImpl: async (url, init) => {
      sent.push({ url: String(url), init: init! });
      return new Response(null, { status: 204 });
    },
    ...overrides,
  });
  return { reporter, sent };
}

const body = (c: { init: RequestInit }) => JSON.parse(c.init.body as string);

describe("SpanReporter", () => {
  it("exports one client span for a failed request, to the proxy's traces route", () => {
    const { reporter, sent } = setup();
    reporter.report(failure);

    expect(sent).toHaveLength(1);
    expect(sent[0].url).toBe("/api/otel/v1/traces");
    expect((sent[0].init.headers as Record<string, string>)["X-CSRF-Token"]).toBe("t");
    const doc = body(sent[0]);
    expect(doc.resourceSpans[0].resource.attributes).toEqual([{ key: "service.name", value: { stringValue: "svc" } }]);
    const span = doc.resourceSpans[0].scopeSpans[0].spans[0];
    expect(span).toMatchObject({
      traceId: failure.trace!.traceId,
      spanId: failure.trace!.spanId,
      name: "POST /api/invoices/{id}",
      kind: 3,
      startTimeUnixNano: "1700000000000000000",
      endTimeUnixNano: "1700000000250000000",
      status: { code: 2 },
    });
    expect(span.attributes).toEqual([
      { key: "http.request.method", value: { stringValue: "POST" } },
      { key: "http.route", value: { stringValue: "/api/invoices/{id}" } },
      { key: "http.response.status_code", value: { intValue: "500" } },
      { key: "error.code", value: { stringValue: "internal_error" } },
    ]);
  });

  it("exports nothing for a failure that carries no trace", () => {
    const { reporter, sent } = setup();
    reporter.report({ ...failure, trace: undefined });
    expect(sent).toEqual([]);
  });

  it("leaves out an attribute the app's allowlist does not list", () => {
    const { reporter, sent } = setup({ attributeAllowlist: { operational: ["http.request.method", "http.route"] } });
    reporter.report(failure);
    const keys = body(sent[0]).resourceSpans[0].scopeSpans[0].spans[0].attributes.map((a: { key: string }) => a.key);
    expect(keys).toEqual(["http.request.method", "http.route"]);
  });

  it("drops a code that is not a machine code, so server text can't ride on it", () => {
    const { reporter, sent } = setup();
    reporter.report({ ...failure, code: "Jane Doe 13.5" });
    expect(JSON.stringify(body(sent[0]))).not.toMatch(/Jane|13\.5/);
  });

  it("exports no more than the limit in a window, so a failing page can't flood", () => {
    let now = 0;
    const { reporter, sent } = setup({ now: () => now, maxPerWindow: 3, windowMs: 1000 });
    for (let i = 0; i < 10; i++) reporter.report(failure);
    expect(sent).toHaveLength(3);
    now = 1000;
    reporter.report(failure);
    expect(sent).toHaveLength(4);
  });

  it("never throws into the request, whether the send fails synchronously or later", async () => {
    const { reporter } = setup({
      fetchImpl: () => {
        throw new Error("boom");
      },
    });
    expect(() => reporter.report(failure)).not.toThrow();
    const { reporter: rejecting } = setup({ fetchImpl: () => Promise.reject(new Error("offline")) });
    expect(() => rejecting.report(failure)).not.toThrow();
    await new Promise((r) => setTimeout(r, 0));
  });
});
