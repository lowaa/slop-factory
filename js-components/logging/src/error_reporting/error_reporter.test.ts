import { describe, expect, it } from "vitest";
import type { Attrs, Logger } from "../logger";
import { ErrorReporter, type ErrorReporterOptions } from "./error_reporter";

// Stands for an app's own API error class.
class ApiError extends Error {
  readonly status: number;
  readonly code?: string;
  constructor(status: number, message: string, code?: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

class RecordingLogger implements Logger {
  errors: { message: string; attrs: Attrs }[] = [];
  debug() {}
  info() {}
  warn() {}
  event() {}
  error(message: string, attrs: Attrs = {}) {
    this.errors.push({ message, attrs });
  }
}

const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

function setup(overrides: Partial<ErrorReporterOptions> = {}) {
  const logger = new RecordingLogger();
  let now = 1_000_000;
  const reporter = new ErrorReporter({
    logger,
    currentRoute: () => "/invoices/$id",
    buildTag: async () => "20261003T000000Z-abc",
    userAgent: "test-agent",
    describeFailure: (err) => (err instanceof ApiError ? { status: err.status, code: err.code } : undefined),
    now: () => now,
    ...overrides,
  });
  return { logger, reporter, advance: (ms: number) => (now += ms) };
}

function errorWithStack(message: string, stack: string): Error {
  const err = new Error(message);
  err.stack = stack;
  return err;
}

describe("ErrorReporter", () => {
  it("reports a crash with its type, frames, route template, build tag, user agent and user id", async () => {
    const { logger, reporter } = setup();
    reporter.setUserId("user-1");

    reporter.reportError("uncaught error", errorWithStack("boom", "Error: boom\n    at f (https://a/assets/i.js:1:2)"));
    await flush();

    expect(logger.errors).toEqual([
      {
        message: "uncaught error",
        attrs: expect.objectContaining({
          "error.type": "Error",
          "error.message": "message withheld",
          "error.stack": "    at f (https://a/assets/i.js:1:2)",
          "http.route": "/invoices/$id",
          "build.tag": "20261003T000000Z-abc",
          "user_agent.original": "test-agent",
          "user.id": "user-1",
        }),
      },
    ]);
  });

  it("appends the component stack to the stack", async () => {
    const { logger, reporter } = setup();
    reporter.reportError("render error", errorWithStack("x", "Error: x\n    at f (https://a/i.js:1:2)"), "\n    at Page (https://a/p.js:3:4)");
    await flush();
    expect(logger.errors[0].attrs["error.stack"]).toBe(
      "    at f (https://a/i.js:1:2)\n--- component stack ---\n    at Page (https://a/p.js:3:4)",
    );
  });

  it("carries no user id once the user has signed out", async () => {
    const { logger, reporter } = setup();
    reporter.setUserId("user-1");
    reporter.setUserId(undefined);
    reporter.reportError("uncaught error", new Error("a"));
    await flush();
    expect(logger.errors[0].attrs["user.id"]).toBeUndefined();
  });

  it("tags the record 'unknown' when the build tag can't be had", async () => {
    const { logger, reporter } = setup({
      buildTag: async () => {
        throw new Error("offline");
      },
    });
    reporter.reportError("uncaught error", new Error("a"));
    await flush();
    expect(logger.errors[0].attrs["build.tag"]).toBe("unknown");
  });

  it("reports an ApiError by status and code, never by the server's detail", async () => {
    const { logger, reporter } = setup();
    reporter.reportError("unhandled rejection", new ApiError(422, "email Jane Doe 13.5 is invalid", "validation_failed"));
    await flush();
    expect(logger.errors[0].attrs).toMatchObject({ status: 422, "error.code": "validation_failed" });
    expect(JSON.stringify(logger.errors)).not.toContain("Jane");
  });

  it("ignores browser noise", async () => {
    const { logger, reporter } = setup();
    reporter.reportError("uncaught error", new Error("ResizeObserver loop completed with undelivered notifications."));
    await flush();
    expect(logger.errors).toEqual([]);
  });

  it("reports a failed request by method, route template, status and code", async () => {
    const { logger, reporter } = setup();
    reporter.reportApiFailure({ method: "post", route: "/api/life_events/{id}", status: 500, code: "internal" });
    await flush();
    expect(logger.errors).toEqual([
      {
        message: "request failed",
        attrs: expect.objectContaining({
          "http.request.method": "POST",
          "http.route": "/api/life_events/{id}",
          status: 500,
          "error.code": "internal",
        }),
      },
    ]);
  });

  it("tags a failed request's error record with the trace id, so the server's logs for it can be found", async () => {
    const { logger, reporter } = setup();
    const trace = { traceId: "4bf92f3577b34da6a3ce929d0e0e4736", spanId: "00f067aa0ba902b7", startedAtMs: 1, endedAtMs: 2 };
    reporter.reportApiFailure({ method: "GET", route: "/api/dashboards", status: 500, trace });
    await flush();
    expect(logger.errors[0].attrs.trace_id).toBe(trace.traceId);
    expect(JSON.stringify(logger.errors[0].attrs)).not.toContain(trace.spanId);
  });

  it("reports a network failure as one, with no status", async () => {
    const { logger, reporter } = setup();
    reporter.reportApiFailure({ method: "GET", route: "/api/dashboards" });
    await flush();
    expect(logger.errors[0].message).toBe("network error");
    expect(logger.errors[0].attrs.status).toBeUndefined();
  });

  it("drops a code that isn't a plain machine code", async () => {
    const { logger, reporter } = setup();
    reporter.reportApiFailure({ method: "GET", route: "/api/x", status: 500, code: "Hemoglobin 13.5" });
    await flush();
    expect(logger.errors[0].attrs["error.code"]).toBeUndefined();
  });

  describe("flood control", () => {
    it("reports the same error once per window, and again after it", async () => {
      const { logger, reporter, advance } = setup({ dedupeWindowMs: 1000 });
      const err = errorWithStack("a", "Error: a\n    at f (https://a/i.js:1:2)");
      reporter.reportError("render error", err);
      reporter.reportError("render error", err);
      advance(1001);
      reporter.reportError("render error", err);
      await flush();
      expect(logger.errors).toHaveLength(2);
    });

    it("treats errors from different places as different", async () => {
      const { logger, reporter } = setup();
      reporter.reportError("render error", errorWithStack("a", "Error: a\n    at f (https://a/i.js:1:2)"));
      reporter.reportError("render error", errorWithStack("a", "Error: a\n    at g (https://a/i.js:9:9)"));
      await flush();
      expect(logger.errors).toHaveLength(2);
    });

    it("caps distinct errors per window, so a render loop can't flood the proxy", async () => {
      const { logger, reporter, advance } = setup({ maxPerWindow: 3, rateWindowMs: 1000 });
      for (let i = 0; i < 20; i++) {
        reporter.reportError("render error", errorWithStack("a", `Error: a\n    at f${i} (https://a/i.js:1:2)`));
      }
      await flush();
      expect(logger.errors).toHaveLength(3);

      advance(1001);
      reporter.reportError("render error", errorWithStack("a", "Error: a\n    at later (https://a/i.js:1:2)"));
      await flush();
      expect(logger.errors).toHaveLength(4);
    });
  });

  it("remembers a bounded number of errors, forgetting the oldest, even when none has expired", async () => {
    const { logger, reporter } = setup({ dedupeWindowMs: 1e12, maxPerWindow: 1e6 });
    const err = (n: number) => errorWithStack("x", `Error: x\n    at f${n} (https://a/i.js:${n}:1)`);
    for (let n = 0; n < 500; n++) reporter.reportError("uncaught error", err(n));
    await flush();
    expect(logger.errors).toHaveLength(500);

    reporter.reportError("uncaught error", err(499));
    reporter.reportError("uncaught error", err(0));
    await flush();
    expect(logger.errors, "the newest is still deduped; the oldest was forgotten and reports again").toHaveLength(501);
  });
});

describe("chunk load failures and the server's build", () => {
  it("reports a failed lazy chunk as its own kind, with the server's newer tag once seen", async () => {
    let server: string | undefined;
    const { reporter, logger } = setup({ serverBuildTag: () => server });
    server = "20261004T000000Z-new";
    reporter.reportError("render error", new TypeError("Failed to fetch dynamically imported module: https://x/a.js"));
    await flush();
    expect(logger.errors[0].message).toBe("chunk load failed");
    expect(logger.errors[0].attrs["build.tag"]).toBe("20261003T000000Z-abc");
    expect(logger.errors[0].attrs["build.tag.server"]).toBe("20261004T000000Z-new");
  });

  it("leaves build.tag.server out until a newer build was seen", async () => {
    const { reporter, logger } = setup();
    reporter.reportError("render error", new Error("boom"));
    await flush();
    expect(logger.errors[0].message).toBe("render error");
    expect(logger.errors[0].attrs["build.tag.server"]).toBeUndefined();
  });
});
