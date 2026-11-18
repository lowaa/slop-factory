// The consent client goes through the session, not the global fetch: a
// signed-in person's write needs the CSRF token, and one made on a refused
// credential must be refreshed and sent again once -- the server refuses to
// record it, so the choice is never filed under two identities.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  headerSent,
  installFetch,
  jsonResponse,
  urlsOf,
  type FetchMock,
} from "./fetch_recorder";

type Api = typeof import("./index");

describe("consentFetch", () => {
  let fetchMock: FetchMock;
  let api: Api;

  beforeEach(async () => {
    ({ fetchMock, api } = await installFetch());
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  const signedInProfile = {
    id: "u1",
    email: "a@example.com",
    display_name: "A",
    tenant_id: "t1",
    is_admin: false,
  };
  const tokenSent = (call: number) =>
    headerSent(fetchMock, call, api.CSRF_HEADER);

  const rejected = () =>
    new Response(JSON.stringify({ code: "credential_rejected" }), {
      status: 403,
      headers: {
        "Content-Type": "application/json",
        "X-Auth-Credential": "rejected",
      },
    });

  async function signIn(token: string) {
    fetchMock.mockResolvedValueOnce(
      jsonResponse(200, { ...signedInProfile, csrf_token: token }),
    );
    await api.apiClient.me();
  }

  it("sends the CSRF token on a write and not on a read", async () => {
    await signIn("csrf-1");
    fetchMock.mockImplementation(() => Promise.resolve(jsonResponse(200)));

    await api.consentFetch("/api/privacy/consent", {
      method: "PUT",
      body: "{}",
    });
    await api.consentFetch("/api/privacy/consent");

    expect(tokenSent(1)).toBe("csrf-1");
    expect(tokenSent(2)).toBeUndefined();
  });

  it("refreshes and sends a write refused for its credential again, once", async () => {
    await signIn("csrf-2");
    fetchMock
      .mockResolvedValueOnce(rejected())
      .mockResolvedValueOnce(jsonResponse(204))
      .mockResolvedValueOnce(jsonResponse(200));

    const res = await api.consentFetch("/api/privacy/consent", {
      method: "PUT",
      body: "{}",
    });

    expect(res.ok).toBe(true);
    expect(urlsOf(fetchMock).slice(1)).toEqual([
      "/api/privacy/consent",
      "/api/auth/refresh",
      "/api/privacy/consent",
    ]);
  });

  it("does not send it a third time when the retry is refused as well", async () => {
    await signIn("csrf-3");
    fetchMock
      .mockResolvedValueOnce(rejected())
      .mockResolvedValueOnce(jsonResponse(204))
      .mockResolvedValueOnce(rejected());

    const res = await api.consentFetch("/api/privacy/consent", {
      method: "PUT",
      body: "{}",
    });

    expect(res.status).toBe(403);
    expect(urlsOf(fetchMock).slice(1)).toEqual([
      "/api/privacy/consent",
      "/api/auth/refresh",
      "/api/privacy/consent",
    ]);
  });
});
