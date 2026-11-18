import { randomId } from "../session_id";

export const TRACEPARENT_HEADER = "traceparent";

// A request's place in a trace, and when it ran.
export interface RequestTrace {
  traceId: string;
  spanId: string;
  startedAtMs: number;
  endedAtMs: number;
}

const TRACEPARENT = /^00-([0-9a-f]{32})-([0-9a-f]{16})-[0-9a-f]{2}$/;

// A W3C traceparent for one request. The trace id is made fresh for every
// request and is never derived from the user, the session or the page, so a
// trace says nothing about who sent it and two requests can't be joined by it
// (go-privacy/README.md: the operational tier carries no persistent
// identifier). The sampled flag is set so the server logs the request's id,
// though only failures are ever exported from the browser.
export function newTraceparent(): { traceId: string; spanId: string; header: string } {
  const traceId = nonZeroId(32);
  const spanId = nonZeroId(16);
  return { traceId, spanId, header: `00-${traceId}-${spanId}-01` };
}

// W3C Trace Context makes an all-zero id invalid, and a receiver drops the
// header.
function nonZeroId(length: number): string {
  for (;;) {
    const id = randomId().slice(0, length);
    if (/[1-9a-f]/.test(id)) return id;
  }
}

const ALL_ZEROS = /^0+$/;

export function parseTraceparent(header: string | null): { traceId: string; spanId: string } | undefined {
  const m = header === null ? null : TRACEPARENT.exec(header);
  return m && !ALL_ZEROS.test(m[1]) && !ALL_ZEROS.test(m[2]) ? { traceId: m[1], spanId: m[2] } : undefined;
}

export interface TraceparentOptions {
  // The origin the page is served from: only requests to it carry the header,
  // so a trace id is never sent to a third party.
  origin: () => string;
  // Path prefixes that get one. Everything else, including the OTel proxy
  // itself, goes without.
  includePrefixes: readonly string[];
  excludePrefixes?: readonly string[];
}

const startedAt = new WeakMap<Request, number>();

// An openapi-fetch middleware that propagates a W3C traceparent on
// same-origin API requests, and notes when each started. It starts no
// spans and exports nothing: a failed request is reported from the failure
// bus (see SpanReporter), which is what keeps this operational rather than a
// stream of what each user did.
export function traceparentMiddleware(options: TraceparentOptions) {
  return {
    onRequest({ request }: { request: Request }): void {
      try {
        const url = new URL(request.url, options.origin());
        if (url.origin !== options.origin()) return;
        const path = url.pathname;
        if (!options.includePrefixes.some((p) => path.startsWith(p))) return;
        if (options.excludePrefixes?.some((p) => path.startsWith(p))) return;
        request.headers.set(TRACEPARENT_HEADER, newTraceparent().header);
        startedAt.set(request, Date.now());
      } catch {
        // Tracing must never break the request it rides on.
      }
    },
  };
}

// The trace of a request the middleware tagged, or undefined for one it left
// alone.
export function requestTrace(request: Request, now: number = Date.now()): RequestTrace | undefined {
  const ids = parseTraceparent(request.headers.get(TRACEPARENT_HEADER));
  if (!ids) return undefined;
  return { ...ids, startedAtMs: startedAt.get(request) ?? now, endedAtMs: now };
}
