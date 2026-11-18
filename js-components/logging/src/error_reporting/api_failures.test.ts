import { describe, expect, it } from "vitest";
import { ApiFailureBus, failureMiddleware, shouldReportFailure, type ApiFailure } from "./api_failures";

describe("shouldReportFailure", () => {
  it.each([500, 502, 503])("reports a %i", (status) => expect(shouldReportFailure(status, undefined)).toBe(true));
  it.each([403, 405, 408, 413, 415, 429])("reports a %i, which no user action explains", (status) =>
    expect(shouldReportFailure(status, "x")).toBe(true),
  );
  it.each([400, 401, 404, 409, 422])("does not report an expected %i", (status) =>
    expect(shouldReportFailure(status, "x")).toBe(false),
  );
  it("does not report a 403 csrf, which is a session that ended", () => {
    expect(shouldReportFailure(403, "csrf")).toBe(false);
  });
});

describe("failureMiddleware", () => {
  const code = (body: unknown) => (body as { code?: string }).code;

  function setup() {
    const bus = new ApiFailureBus();
    const heard: ApiFailure[] = [];
    bus.subscribe((f) => heard.push(f));
    return { mw: failureMiddleware(bus, code), heard, bus };
  }
  const request = new Request("http://app.test/api/things/42", { method: "get" });

  it("announces a failure by method, route template, status and code, never the URL or body", async () => {
    const { mw, heard } = setup();
    const response = Response.json({ detail: "Jane Doe 13.5", code: "internal_error" }, { status: 500 });
    await mw.onResponse({ request, response, schemaPath: "/api/things/{id}" });
    expect(heard).toEqual([{ method: "GET", route: "/api/things/{id}", status: 500, code: "internal_error" }]);
    expect(JSON.stringify(heard)).not.toMatch(/42|Jane/);
  });

  it("is silent for a success and for an expected 4xx", async () => {
    const { mw, heard } = setup();
    await mw.onResponse({ request, response: Response.json({}, { status: 200 }), schemaPath: "/x" });
    await mw.onResponse({ request, response: Response.json({ code: "c" }, { status: 422 }), schemaPath: "/x" });
    expect(heard).toEqual([]);
  });

  it("reports a non-JSON error body by status alone", async () => {
    const { mw, heard } = setup();
    await mw.onResponse({ request, response: new Response("<html>", { status: 502 }), schemaPath: "/x" });
    expect(heard).toEqual([{ method: "GET", route: "/x", status: 502, code: undefined }]);
  });

  it("carries the trace of a request that was sent with a traceparent", async () => {
    const { mw, heard } = setup();
    const traced = new Request("http://app.test/api/things/42", {
      headers: { traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01" },
    });
    await mw.onResponse({ request: traced, response: Response.json({}, { status: 500 }), schemaPath: "/api/things/{id}" });
    expect(heard[0].trace).toMatchObject({ traceId: "4bf92f3577b34da6a3ce929d0e0e4736", spanId: "00f067aa0ba902b7" });
  });

  it("reports a network failure without a status", () => {
    const { mw, heard } = setup();
    mw.onError({ request, schemaPath: "/x" });
    expect(heard).toEqual([{ method: "GET", route: "/x" }]);
  });

  it("carries the trace of a request that failed without a response", () => {
    const { mw, heard } = setup();
    const traced = new Request("http://app.test/api/things", {
      headers: { traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01" },
    });
    mw.onError({ request: traced, schemaPath: "/api/things" });
    expect(heard[0].trace).toMatchObject({ traceId: "4bf92f3577b34da6a3ce929d0e0e4736", spanId: "00f067aa0ba902b7" });
  });

  it("survives a listener that throws, and unsubscribes", () => {
    const bus = new ApiFailureBus();
    const heard: ApiFailure[] = [];
    bus.subscribe(() => {
      throw new Error("broke");
    });
    const off = bus.subscribe((f) => heard.push(f));
    bus.announce({ method: "GET", route: "/x" });
    off();
    bus.announce({ method: "GET", route: "/y" });
    expect(heard).toHaveLength(1);
  });
});
