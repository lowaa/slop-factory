import { requestTrace, type RequestTrace } from "../tracing/traceparent";

// A request that failed in a way worth reporting (see shouldReportFailure).
// route is the OpenAPI path template, never the URL, which holds ids.
export interface ApiFailure {
  method: string;
  route: string;
  // Absent for a network failure, where there was no response.
  status?: number;
  code?: string;
  // Where the request sat in a trace, when it was sent with a traceparent.
  trace?: RequestTrace;
}

// 5xx and network failures are always the system's fault. Of the 4xx, only
// those no user action explains: 403 other than a session that can't write,
// a request the server or a proxy refused as malformed or too big (405, 408,
// 413, 415), and 429. The rest are outcomes the UI already shows --
// 400 (a dead invite or token), 401 (wrong password, expired session),
// 404, 409 and 422 (validation) -- and logging them would bury the faults.
const REPORTED_CLIENT_ERRORS: ReadonlySet<number> = new Set([403, 405, 408, 413, 415, 429]);

export function shouldReportFailure(status: number, code: string | undefined): boolean {
  if (status >= 500) return true;
  if (status === 403 && code === "csrf") return false;
  return REPORTED_CLIENT_ERRORS.has(status);
}

// Who hears of failed requests. One per API client module.
export class ApiFailureBus {
  private readonly listeners = new Set<(failure: ApiFailure) => void>();

  // Returns an unsubscribe function.
  subscribe(listener: (failure: ApiFailure) => void): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  announce(failure: ApiFailure): void {
    for (const listener of this.listeners) {
      try {
        listener(failure);
      } catch {
        // Reporting must never break the request it reports on.
      }
    }
  }
}

interface FailureMiddleware {
  onResponse(ctx: { request: Request; response: Response; schemaPath: string }): Promise<void>;
  onError(ctx: { request: Request; schemaPath: string }): void;
}

// An openapi-fetch middleware that announces failed requests on bus. It
// reports from a middleware rather than the caller's unwrap: a network
// failure throws out of fetch before unwrap sees a result, and the
// middleware is handed the route template, which a response alone doesn't
// carry. extractCode reads the server's error code out of a parsed error
// body; the body is never kept.
export function failureMiddleware(bus: ApiFailureBus, extractCode: (body: unknown) => string | undefined): FailureMiddleware {
  return {
    async onResponse({ request, response, schemaPath }) {
      if (response.ok) return;
      let code: string | undefined;
      try {
        code = extractCode(await response.clone().json());
      } catch {
        // Not a JSON error body: the status says enough.
      }
      if (shouldReportFailure(response.status, code)) {
        bus.announce(withTrace({ method: request.method, route: schemaPath, status: response.status, code }, request));
      }
    },
    onError({ request, schemaPath }) {
      bus.announce(withTrace({ method: request.method, route: schemaPath }, request));
    },
  };
}

function withTrace(failure: ApiFailure, request: Request): ApiFailure {
  const trace = requestTrace(request);
  return trace ? { ...failure, trace } : failure;
}
