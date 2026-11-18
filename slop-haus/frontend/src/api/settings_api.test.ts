// The settings half of apiClient's wire behaviour, against a mocked fetch; see index.test.ts
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { installFetch, jsonResponse, sent, urlsOf, type FetchMock } from "./fetch_recorder";

type Api = typeof import("./index");
type ApiClient = Api["apiClient"];

describe("apiClient settings", () => {
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

  it("unwraps the {settings: [...]} envelope", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { settings: [{ name: "ticket_processing_enabled" }] }));

    const settings = await apiClient.listSettings();

    expect(settings).toEqual([{ name: "ticket_processing_enabled" }]);
    expect(urlsOf(fetchMock)[0]).toBe("/api/settings");
  });

  it("PATCHes a setting change", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { name: "ticket_processing_enabled", bool_val: false }));

    await apiClient.updateSetting("ticket_processing_enabled", { type: "bool", bool_val: false });

    const req = await sent(fetchMock, 0);
    expect(req.url).toBe("/api/settings/ticket_processing_enabled");
    expect(req.method).toBe("PATCH");
    expect(req.body).toEqual({ type: "bool", bool_val: false });
  });

  // A setting name goes into the path, so it is encoded rather than
  // interpolated raw.
  it("encodes the setting name in the path", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, {}));

    await apiClient.getSetting("odd/name");

    expect(urlsOf(fetchMock)[0]).toBe("/api/settings/odd%2Fname");
  });
});
