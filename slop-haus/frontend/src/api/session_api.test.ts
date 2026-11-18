// The session half of apiClient's wire behaviour, against a mocked fetch; see index.test.ts
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { wireOf, installFetch, jsonResponse, sent, urlsOf, type FetchMock } from "./fetch_recorder";

type Api = typeof import("./index");
type ApiClient = Api["apiClient"];

// The auth routes are called by the pages through spies, so their paths and
// body keys are pinned here, against the server's wire names
// (server/internal/user/handler.go).
describe("apiClient auth wiring", () => {
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

  it.each([
    ["register", () => ({ call: (a: ApiClient) => a.register("a@x.io", "pw", "Ada"), url: "/api/auth/register", body: { email: "a@x.io", password: "pw", display_name: "Ada" } })],
    ["activate", () => ({ call: (a: ApiClient) => a.activate("t1"), url: "/api/auth/activate", body: { token: "t1" } })],
    ["requestPasswordReset", () => ({ call: (a: ApiClient) => a.requestPasswordReset("a@x.io"), url: "/api/auth/password-reset", body: { email: "a@x.io" } })],
    ["resetPassword", () => ({ call: (a: ApiClient) => a.resetPassword("t1", "new-pw"), url: "/api/auth/password-reset/confirm", body: { token: "t1", password: "new-pw" } })],
    ["logout", () => ({ call: (a: ApiClient) => a.logout(), url: "/api/auth/logout", body: undefined })],
  ])("%s posts to its route with the server's field names", async (_, make) => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));
    const { call, url, body } = make();

    await call(apiClient);

    expect(await wireOf(fetchMock)).toEqual({ url, method: "POST", body });
  });

  it("login posts credentials and returns the profile", async () => {
    const profile = { id: "u1", email: "a@x.io", display_name: "Ada", tenant_id: "t1", is_admin: false };
    fetchMock.mockResolvedValue(new Response(JSON.stringify(profile), { status: 200 }));

    await expect(apiClient.login("a@x.io", "pw")).resolves.toEqual(profile);
    expect(await wireOf(fetchMock)).toEqual({ url: "/api/auth/login", method: "POST", body: { email: "a@x.io", password: "pw" } });
  });

  it("unwraps the {members: [...]} envelope listMembers gets back", async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ members: [{ id: "u1", display_name: "Ada" }] }), { status: 200 }));

    await expect(apiClient.listMembers()).resolves.toEqual([{ id: "u1", display_name: "Ada" }]);
    expect(urlsOf(fetchMock)[0]).toBe("/api/members");
  });

  it("listMemberNames returns the members and the former members of the same response", async () => {
    fetchMock.mockResolvedValue(
      new Response(
        JSON.stringify({ members: [{ id: "u1", display_name: "Ada" }], former: [{ id: "u2", display_name: "Bob" }] }),
        { status: 200 },
      ),
    );

    await expect(apiClient.listMemberNames()).resolves.toEqual({
      members: [{ id: "u1", display_name: "Ada" }],
      former: [{ id: "u2", display_name: "Bob" }],
    });
    expect(urlsOf(fetchMock)[0]).toBe("/api/members");
  });

  it("fetches auth config", async () => {
    const config = { registration_open: false, password_login: true, providers: [{ name: "dex", display_name: "Dex" }] };
    fetchMock.mockResolvedValue(new Response(JSON.stringify(config), { status: 200 }));

    await expect(apiClient.getAuthConfig()).resolves.toEqual(config);
    expect(urlsOf(fetchMock)[0]).toBe("/api/auth/config");
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("does not refresh on a 401 from auth config: it is read before any session", async () => {
    fetchMock.mockResolvedValue(jsonResponse(401, { detail: "no" }));

    await expect(apiClient.getAuthConfig()).rejects.toMatchObject({ status: 401 });
    expect(urlsOf(fetchMock)).toEqual(["/api/auth/config"]);
  });
});

// The invite/tenant routes, pinned the same way apiClient auth wiring above
// pins the plain auth routes -- paths, methods and body keys against the
// server's wire names (server/internal/user/invite_handler.go).
describe("apiClient invites and tenants", () => {
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

  const preview = {
    email: "b@x.io",
    kind: "join_tenant",
    tenant_name: "Ada's workspace",
    invited_by: "Ada",
    account_exists: false,
  };

  it("previews an invite publicly by token", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, preview));

    await expect(apiClient.previewInvite("tok1")).resolves.toEqual(preview);
    expect(await wireOf(fetchMock)).toEqual({ url: "/api/auth/invite/preview", method: "POST", body: { token: "tok1" } });
  });

  it("accepts an invite as a new account and signs in", async () => {
    const profile = { id: "u1", email: "b@x.io", display_name: "Bea", tenant_id: "t1", is_admin: false };
    fetchMock.mockResolvedValue(jsonResponse(200, profile));

    await expect(apiClient.acceptInviteAsNewAccount("tok1", "Bea", "a-strong-password")).resolves.toEqual(profile);
    expect(await wireOf(fetchMock)).toEqual({
      url: "/api/auth/invite/accept",
      method: "POST",
      body: { token: "tok1", display_name: "Bea", password: "a-strong-password" },
    });
  });

  it("accepts an invite for the signed-in user", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { id: "u1", email: "b@x.io", display_name: "Bea", tenant_id: "t1", is_admin: false }));

    await apiClient.acceptInvite("tok1");
    expect(await wireOf(fetchMock)).toEqual({ url: "/api/invites/accept", method: "POST", body: { token: "tok1" } });
  });

  it("creates a member invite", async () => {
    fetchMock.mockResolvedValue(jsonResponse(201, { id: "i1", email: "b@x.io", kind: "join_tenant" }));

    await apiClient.createInvite("b@x.io");
    expect(await wireOf(fetchMock)).toEqual({ url: "/api/invites", method: "POST", body: { email: "b@x.io" } });
  });

  it("unwraps the {invites: [...]} envelope listInvites gets back", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { invites: [{ id: "i1", email: "b@x.io" }] }));

    await expect(apiClient.listInvites()).resolves.toEqual([{ id: "i1", email: "b@x.io" }]);
    expect(urlsOf(fetchMock)[0]).toBe("/api/invites");
  });

  it("revokes an invite by id", async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));

    await apiClient.revokeInvite("i1");
    const req = await sent(fetchMock, 0);
    expect(req.url).toBe("/api/invites/i1");
    expect(req.method).toBe("DELETE");
  });

  it("unwraps the {tenants: [...]} envelope listTenants gets back", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { tenants: [{ id: "t1", name: "Ada's workspace", current: true }] }));

    await expect(apiClient.listTenants()).resolves.toEqual([{ id: "t1", name: "Ada's workspace", current: true }]);
    expect(urlsOf(fetchMock)[0]).toBe("/api/auth/tenants");
  });

  it("switches tenant", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { id: "u1", email: "a@x.io", display_name: "Ada", tenant_id: "t2", is_admin: false }));

    await apiClient.switchTenant("t2");
    expect(await wireOf(fetchMock)).toEqual({ url: "/api/auth/switch-tenant", method: "POST", body: { tenant_id: "t2" } });
  });

  it("creates, lists and revokes admin invites", async () => {

    fetchMock.mockResolvedValueOnce(jsonResponse(201, { id: "i1", email: "c@x.io", kind: "new_tenant" }));
    await apiClient.createAdminInvite("c@x.io");
    expect(await wireOf(fetchMock)).toEqual({ url: "/api/admin/invites", method: "POST", body: { email: "c@x.io" } });

    fetchMock.mockResolvedValueOnce(jsonResponse(200, { invites: [{ id: "i1", email: "c@x.io" }] }));
    await expect(apiClient.listAdminInvites()).resolves.toEqual([{ id: "i1", email: "c@x.io" }]);
    expect(urlsOf(fetchMock)[1]).toBe("/api/admin/invites");

    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }));
    await apiClient.revokeAdminInvite("i1");
    const req = await sent(fetchMock, 2);
    expect(req.url).toBe("/api/admin/invites/i1");
    expect(req.method).toBe("DELETE");
  });
});

// The remaining login-provider and sign-in-methods routes, pinned the same
// way the sections above pin the rest of the client -- method, path, query
// and body against the server's wire names (server/internal/user/
// signin_handler.go).
describe("apiClient login providers and sign-in methods", () => {
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

  it("builds the provider sign-in URL, with and without a next path", async () => {

    expect(apiClient.providerSignInURL("dex")).toBe("/api/auth/login/dex");
    expect(apiClient.providerSignInURL("dex", "/account")).toBe("/api/auth/login/dex?next=%2Faccount");
  });

  it("starts an invite-accept round trip through a provider", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { redirect_url: "http://dex.test/auth" }));

    await expect(apiClient.startInviteWithProvider("dex", "tok1", "/")).resolves.toBe("http://dex.test/auth");
    expect(await wireOf(fetchMock)).toEqual({
      url: "/api/auth/invite/accept/dex",
      method: "POST",
      body: { token: "tok1", next: "/" },
    });
  });

  it("starts a reauth round trip through a provider", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { redirect_url: "http://dex.test/reauth" }));

    await expect(apiClient.startReauth("dex", "/account")).resolves.toBe("http://dex.test/reauth");
    expect(await wireOf(fetchMock)).toEqual({
      url: "/api/auth/reauth/dex",
      method: "POST",
      body: { next: "/account" },
    });
  });

  it("lists the account's sign-in methods", async () => {
    const methods = {
      password: { email: "ada@example.com", verified: true },
      identities: [],
      password_login_enabled: true,
      reauth_required_after: "2026-01-01T00:00:00Z",
    };
    fetchMock.mockResolvedValue(jsonResponse(200, methods));

    await expect(apiClient.listSignInMethods()).resolves.toEqual(methods);
    expect(urlsOf(fetchMock)[0]).toBe("/api/auth/sign-in-methods");
    expect((await sent(fetchMock)).method).toBe("GET");
  });

  it("sets a password by PUTting new_password to the sign-in-methods route", async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));

    await apiClient.setPassword("a-new-password");
    expect(await wireOf(fetchMock)).toEqual({
      url: "/api/auth/sign-in-methods/password",
      method: "PUT",
      body: { new_password: "a-new-password" },
    });
  });

  it("removes the password by DELETEing the same route", async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));

    await apiClient.removePassword();
    const req = await sent(fetchMock, 0);
    expect(req.url).toBe("/api/auth/sign-in-methods/password");
    expect(req.method).toBe("DELETE");
  });

  // provider/subject go in the query string, not the path -- an OIDC
  // subject can hold a slash, which a path segment can't carry raw.
  it("removes an identity by provider and subject, as query params", async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));

    await apiClient.removeIdentity("dex", "sub/with/slash");
    const req = await sent(fetchMock, 0);
    expect(req.method).toBe("DELETE");
    const parsed = new URL(req.url, "http://localhost");
    expect(parsed.pathname).toBe("/api/auth/sign-in-methods/identity");
    expect(parsed.searchParams.get("provider")).toBe("dex");
    expect(parsed.searchParams.get("subject")).toBe("sub/with/slash");
  });

  // Every write carries the session's CSRF token (see session.test.ts);
  // this just confirms these specific routes are wired through
  // send()/request() like everything else, rather than a hand-rolled fetch
  // that forgot it.
  it("carries the CSRF token on a sign-in-methods write", async () => {
    const { CSRF_HEADER } = api;
    fetchMock.mockResolvedValueOnce(jsonResponse(200, { id: "u1", csrf_token: "the-token" }));
    await apiClient.me();
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));

    await apiClient.removePassword();
    expect((await sent(fetchMock, 1)).headers.get(CSRF_HEADER)).toBe("the-token");
  });
});
