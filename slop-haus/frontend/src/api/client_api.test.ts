// The client half of apiClient's wire behaviour, against a mocked fetch; see index.test.ts
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { headerSent, installFetch, jsonResponse, type FetchMock } from "./fetch_recorder";

type Api = typeof import("./index");
type ApiClient = Api["apiClient"];

describe("apiClient requests", () => {
  let fetchMock: FetchMock;
  let api: Api;
  let apiClient: ApiClient;

  beforeEach(async () => {
    ({ fetchMock, api } = await installFetch());
    apiClient = api.apiClient;
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  // A body fetch is handed as a Request clone is sent intact, but Chromium
  // leaves it out of DevTools and Playwright's request.postData(); the e2e
  // suite reads writes that way.
  it("hands fetch each write's body as a string", async () => {
    fetchMock.mockResolvedValue(jsonResponse(201, { id: 1 }));

    await apiClient.createTicket({ title: "New" });

    const [input, init] = fetchMock.mock.calls[0];
    expect(input).not.toBeInstanceOf(Request);
    expect(init?.body).toBe(JSON.stringify({ title: "New" }));
  });

  it("throws ApiError with the server's detail message on failure", async () => {
    const { ApiError } = api;
    fetchMock.mockResolvedValue(jsonResponse(404, { detail: "ticket not found", status: 404 }));

    const err = await apiClient.getTicket(999).catch((e: unknown) => e);

    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 404, message: "ticket not found" });
  });

  it("sends a fresh traceparent on each API request, the one a failure report carries", async () => {
    const failures: (string | undefined)[] = [];
    const unsubscribe = api.onApiFailure((f) => failures.push(f.trace?.traceId));
    fetchMock.mockImplementation(async () => jsonResponse(500, { detail: "boom", status: 500 }));

    await apiClient.getTicket(1).catch(() => undefined);
    await apiClient.getTicket(1).catch(() => undefined);
    unsubscribe();

    const headers = [headerSent(fetchMock, 0, "traceparent"), headerSent(fetchMock, 1, "traceparent")];
    expect(headers[0]).toMatch(/^00-[0-9a-f]{32}-[0-9a-f]{16}-01$/);
    expect(headers[0]).not.toEqual(headers[1]);
    expect(failures).toEqual(headers.map((h) => h!.split("-")[1]));
  });

  it("falls back to the response's status line when the error body has no detail", async () => {
    fetchMock.mockResolvedValue(new Response("", { status: 500, statusText: "Internal Server Error" }));

    const err = await apiClient.getTicket(1).catch((e: unknown) => e);

    expect(err).toMatchObject({ status: 500, message: "Internal Server Error" });
  });
});
