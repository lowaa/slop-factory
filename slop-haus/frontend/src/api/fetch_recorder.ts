// Test support for the api/ tests: reads what a stubbed fetch was sent,
// whichever way the caller handed it over -- a URL plus RequestInit, or a
// ready-made Request (what openapi-fetch passes). The tests assert on the
// wire (method, path, headers, body), not on which of the two shapes the
// client happens to use, so they hold across a change of implementation.
import { vi, type Mock } from "vitest";

export type FetchMock = Mock<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>;

// Node's Request, unlike a browser's, has no page to resolve a relative URL
// against, and api/index.ts's base URL is "" on purpose (same-origin behind
// nginx or vite's proxy). This subclass puts a leading-slash URL on a fake
// origin, which urlOf takes off again. It is the page's own, so the
// same-origin checks (the traceparent middleware's) see these as same-origin.
const TEST_ORIGIN = window.location.origin;

class TestRequest extends Request {
  constructor(input: RequestInfo | URL, init?: RequestInit) {
    super(typeof input === "string" && input.startsWith("/") ? `${TEST_ORIGIN}${input}` : input, init);
  }
}

export interface SentRequest {
  method: string;
  // Path and query, as the server sees them.
  url: string;
  headers: Headers;
  // The JSON body, parsed; undefined when there is none.
  body: unknown;
}

// installFetch stubs fetch (and Request, for the reason above) and loads a
// fresh copy of api/index.ts against them: a client library binds Request
// when it is created, and the session state (CSRF token, sign-out listener)
// is module state that must not leak between tests.
export async function installFetch(): Promise<{ fetchMock: FetchMock; api: typeof import("./index") }> {
  const fetchMock: FetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  vi.stubGlobal("Request", TestRequest);
  vi.resetModules();
  const api = await import("./index");
  return { fetchMock, api };
}

function stripOrigin(url: string): string {
  return url.startsWith(TEST_ORIGIN) ? url.slice(TEST_ORIGIN.length) : url;
}

// urlOf is the path and query of one fetch call's input -- synchronous, for
// a mock implementation that answers by URL.
export function urlOf(input: RequestInfo | URL): string {
  if (typeof input === "string") return stripOrigin(input);
  if (input instanceof URL) return stripOrigin(input.href);
  return stripOrigin(input.url);
}

function methodOf(input: RequestInfo | URL, init?: RequestInit): string {
  if (init?.method) return init.method.toUpperCase();
  if (input instanceof Request) return input.method.toUpperCase();
  return "GET";
}

function headersOf(input: RequestInfo | URL, init?: RequestInit): Headers {
  const headers = new Headers(input instanceof Request ? input.headers : undefined);
  new Headers(init?.headers).forEach((value, name) => headers.set(name, value));
  return headers;
}

async function bodyOf(input: RequestInfo | URL, init?: RequestInit): Promise<unknown> {
  let text: string | undefined;
  if (init?.body !== undefined && init.body !== null) {
    text = typeof init.body === "string" ? init.body : await new Response(init.body).text();
  } else if (input instanceof Request) {
    text = await input.clone().text();
  }
  return text ? JSON.parse(text) : undefined;
}

// sent is the index'th request the mock received.
export async function sent(fetchMock: FetchMock, index = 0): Promise<SentRequest> {
  const call = fetchMock.mock.calls[index];
  if (!call) throw new Error(`fetch was called ${fetchMock.mock.calls.length} time(s), not ${index + 1}`);
  const [input, init] = call;
  return {
    method: methodOf(input, init),
    url: urlOf(input),
    headers: headersOf(input, init),
    body: await bodyOf(input, init),
  };
}

export async function allSent(fetchMock: FetchMock): Promise<SentRequest[]> {
  return Promise.all(fetchMock.mock.calls.map((_, i) => sent(fetchMock, i)));
}

// urlsOf is every call's path and query, in order.
export function urlsOf(fetchMock: FetchMock): string[] {
  return fetchMock.mock.calls.map(([input]) => urlOf(input));
}

// headerSent is one header of the index'th call, or undefined when it was
// not sent.
export function headerSent(fetchMock: FetchMock, index: number, name: string): string | undefined {
  const [input, init] = fetchMock.mock.calls[index];
  return headersOf(input, init).get(name) ?? undefined;
}

export function jsonResponse(status: number, body: unknown = {}, statusText = ""): Response {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    statusText,
    headers: { "Content-Type": "application/json" },
  });
}

// The method, path and body of one call, for tests that assert on all three.
export async function wireOf(fetchMock: FetchMock, index = 0): Promise<{ url: string; method: string; body: unknown }> {
  const { url, method, body } = await sent(fetchMock, index);
  return { url, method, body };
}
