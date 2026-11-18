// A mock API for tests that run the real client stack (api/client.ts: the
// tenant header, CSRF token, 401 refresh and retry, error envelope), which
// vi.spyOn(apiClient) bypasses. Handlers answer from the same fixtures the
// rest of the suite uses, and check each answer against openapi.json, so a
// handler cannot serve a body the server would not.
import { afterAll, afterEach, beforeAll, vi } from "vitest";
import { http, HttpResponse, type HttpHandler } from "msw/http";
import { setupServer } from "msw/node";
import { openApiSchemas } from "./openapi_schema";

// Node's Request, unlike a browser's, cannot resolve a relative URL, and the
// client's base URL is "" (same-origin behind nginx). See api/fetch_recorder.ts.
class PageRequest extends Request {
  constructor(input: RequestInfo | URL, init?: RequestInit) {
    super(typeof input === "string" && input.startsWith("/") ? `${window.location.origin}${input}` : input, init);
  }
}

type Method = "get" | "post" | "put" | "patch" | "delete";

// serve answers method + path (an OpenAPI template, e.g. "/api/tickets/{id}")
// with a body checked against that status's schema. `onRequest` sees each request.
export function serve(
  method: Method,
  template: string,
  body: unknown,
  onRequest?: (request: Request) => void,
  status = 200,
): HttpHandler {
  const concrete = template.replace(/\{[^}]+\}/g, "1");
  const errors = openApiSchemas.responseErrors(method, concrete, status, body);
  if (errors.length > 0) throw new Error(`handler body breaks the spec:\n${errors.join("\n")}`);
  return http[method](template.replace(/\{([^}]+)\}/g, ":$1"), ({ request }) => {
    onRequest?.(request);
    return HttpResponse.json(body as object, { status });
  });
}

// problem is the server's error envelope (huma's application/problem+json).
export function problem(status: number, detail: string, code: string) {
  return HttpResponse.json({ detail, code, status, title: detail }, { status, headers: { "Content-Type": "application/problem+json" } });
}

export const server = setupServer();

// useMockApi starts the server for a test file and resets its handlers
// between tests; unhandled requests fail the test rather than reaching out.
export function useMockApi(): void {
  beforeAll(() => {
    vi.stubGlobal("Request", PageRequest);
    server.listen({ onUnhandledFrame: "error" });
  });
  afterEach(() => server.resetHandlers());
  afterAll(() => {
    server.close();
    vi.unstubAllGlobals();
  });
}
