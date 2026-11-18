// The integrations half of apiClient's wire behaviour, against a mocked fetch; see index.test.ts
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { allSent, installFetch, jsonResponse, type FetchMock } from "./fetch_recorder";

type Api = typeof import("./index");
type ApiClient = Api["apiClient"];

describe("apiClient integrations", () => {
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

  // The collection endpoints return {tickets: [...]} / {comments: [...]};
  // the client unwraps the envelope so callers only ever see arrays. This
  // asserts both halves: the envelope is what goes over the wire, and an
  // array is what comes back out.
  it("reads GitHub webhooks at their route", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(200, { enabled: true, deliveries: [] }));

    expect(await apiClient.listGitHubWebhooks()).toEqual({ enabled: true, deliveries: [] });

    const calls = await allSent(fetchMock);
    expect(calls.map(({ method, url }) => [method, url])).toEqual([["GET", "/api/github/webhooks"]]);
  });
});
