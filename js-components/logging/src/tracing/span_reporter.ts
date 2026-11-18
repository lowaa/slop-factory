import { AttributeFilter, type AttributeAllowlist } from "../attribute_filter";
import type { ApiFailure } from "../error_reporting/api_failures";

// The span's attributes, all of the operational tier. No user id, URL or
// query string: the route is the template.
const SPAN_ATTRIBUTES = ["http.request.method", "http.route", "http.response.status_code", "error.code"] as const;

const CODE = /^[a-z0-9_]{1,64}$/;
const SPAN_KIND_CLIENT = 3;
const STATUS_CODE_ERROR = 2;

export interface SpanReporterOptions {
  serviceName: string;
  // Same-origin path of the app's OTel proxy; spans go to `${url}/v1/traces`.
  collectorPublicUrl: string;
  // Extra request headers, read at each export (the session's CSRF token).
  headers?: () => Record<string, string>;
  // The app's attribute allowlist. Span attributes pass through it like a
  // log record's, so a key the app has not allowed never leaves.
  attributeAllowlist: AttributeAllowlist;
  fetchImpl?: typeof fetch;
  now?: () => number;
  // At most this many spans go out per window, so a failing page can't flood
  // the proxy.
  maxPerWindow?: number;
  windowMs?: number;
}

// Exports one client span for each failed API request, and only for those.
// Spans for requests that worked are never made: that would be a record of
// everything each user did, which is the analytics tier's business and
// needs consent. A failed request's span is what ties a frontend error to
// the server's logs for the same request, by trace id.
export class SpanReporter {
  private readonly o: Required<Omit<SpanReporterOptions, "headers">> & Pick<SpanReporterOptions, "headers">;
  private readonly filter: AttributeFilter;
  private windowStart = 0;
  private inWindow = 0;

  constructor(options: SpanReporterOptions) {
    this.o = { fetchImpl: (...a) => fetch(...a), now: Date.now, maxPerWindow: 10, windowMs: 60_000, ...options };
    this.filter = new AttributeFilter(options.attributeAllowlist);
  }

  // Wire to the failure bus: `bus.subscribe((f) => reporter.report(f))`.
  report(failure: ApiFailure): void {
    const trace = failure.trace;
    if (!trace || !this.admit()) return;
    try {
      void this.send(failure, trace).catch(() => {});
    } catch {
      // Export is best effort and must never throw into the request.
    }
  }

  private admit(): boolean {
    const now = this.o.now();
    if (now - this.windowStart >= this.o.windowMs) {
      this.windowStart = now;
      this.inWindow = 0;
    }
    if (this.inWindow >= this.o.maxPerWindow) return false;
    this.inWindow++;
    return true;
  }

  private send(failure: ApiFailure, trace: NonNullable<ApiFailure["trace"]>): Promise<Response> {
    const method = failure.method.toUpperCase();
    const { attributes } = this.filter.filter("operational", {
      "http.request.method": method,
      "http.route": failure.route,
      "http.response.status_code": failure.status,
      "error.code": failure.code !== undefined && CODE.test(failure.code) ? failure.code : undefined,
    });
    const span = {
      traceId: trace.traceId,
      spanId: trace.spanId,
      name: `${method} ${failure.route}`,
      kind: SPAN_KIND_CLIENT,
      startTimeUnixNano: `${trace.startedAtMs}000000`,
      endTimeUnixNano: `${trace.endedAtMs}000000`,
      attributes: SPAN_ATTRIBUTES.filter((k) => k in attributes).map((key) => ({ key, value: otlpValue(attributes[key]) })),
      status: { code: STATUS_CODE_ERROR },
    };
    const body = {
      resourceSpans: [
        {
          resource: { attributes: [{ key: "service.name", value: { stringValue: this.o.serviceName } }] },
          scopeSpans: [{ scope: { name: this.o.serviceName }, spans: [span] }],
        },
      ],
    };
    return this.o.fetchImpl(`${this.o.collectorPublicUrl}/v1/traces`, {
      method: "POST",
      headers: { "Content-Type": "application/json", ...this.o.headers?.() },
      body: JSON.stringify(body),
      // Lets the request outlive a page that is going away.
      keepalive: true,
    });
  }
}

function otlpValue(v: unknown): { stringValue: string } | { intValue: string } {
  return typeof v === "number" ? { intValue: String(Math.trunc(v)) } : { stringValue: String(v) };
}
