// The session half of api/index.ts: the CSRF token, the refresh-and-retry
// on an expired access token, and telling the app when the session is
// over. Each test loads a fresh copy of the module, since the in-flight
// refresh and the sign-out listener are module state.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { headerSent, installFetch, jsonResponse, sent, urlOf, urlsOf, type FetchMock } from "./fetch_recorder";

type Api = typeof import("./index");

describe("session handling", () => {
  let fetchMock: FetchMock;
  let api: Api;
  let signedOut: ReturnType<typeof vi.fn<() => void>>;

  beforeEach(async () => {
    ({ fetchMock, api } = await installFetch());
    signedOut = vi.fn<() => void>();
    api.onSignedOut(signedOut);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  function tokenSent(call: number): string | undefined {
    return headerSent(fetchMock, call, api.CSRF_HEADER);
  }

  const signedInProfile = { id: "u1", email: "a@example.com", display_name: "A", tenant_id: "t1", is_admin: false };

  // Without the token the server refuses any write the session cookie
  // authenticates; it comes from the session's profile, and goes on writes
  // only.
  it("sends the session's CSRF token on writes, and only on writes", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(200, { ...signedInProfile, csrf_token: "from-me" }));
    const profile = await api.apiClient.me();
    expect(profile).toEqual(signedInProfile);
    expect(profile).not.toHaveProperty("csrf_token");

    fetchMock.mockImplementation(() => Promise.resolve(jsonResponse(200, { tickets: [], id: 1 })));
    await api.apiClient.listTicketsPage({});
    await api.apiClient.createTicket({ title: "t" });
    await api.apiClient.updateTicket(1, { status: "done" });
    await api.apiClient.revokeInvite("i1");

    expect(tokenSent(0)).toBeUndefined();
    expect(tokenSent(1)).toBeUndefined();
    expect(tokenSent(2)).toBe("from-me");
    expect(tokenSent(3)).toBe("from-me");
    expect(tokenSent(4)).toBe("from-me");
  });

  it("sends nothing before there is a session", async () => {
    fetchMock.mockResolvedValue(jsonResponse(401, { detail: "invalid email or password" }));
    await api.apiClient.login("a@example.com", "wrong").catch(() => {});
    expect(tokenSent(0)).toBeUndefined();
  });

  // A sign-in is a new session: its token replaces whatever the page held.
  it("takes the token from a sign-in, and a new one from the next", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(200, { ...signedInProfile, csrf_token: "first" }));
    expect(await api.apiClient.login("a@example.com", "pw")).toEqual(signedInProfile);
    fetchMock.mockResolvedValueOnce(jsonResponse(200, { ...signedInProfile, csrf_token: "second" }));
    await api.apiClient.acceptInviteAsNewAccount("t1", "A", "a-strong-password");
    fetchMock.mockResolvedValue(jsonResponse(204));

    await api.apiClient.removePassword();

    expect(tokenSent(2)).toBe("second");
  });

  // A tenant switch re-issues the same session, and answers with its
  // (unchanged) token, which the page keeps using.
  it("keeps the session's token through a tenant switch", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(200, { ...signedInProfile, csrf_token: "the-session" }));
    await api.apiClient.me();
    fetchMock.mockResolvedValueOnce(jsonResponse(200, { ...signedInProfile, tenant_id: "t2", csrf_token: "the-session" }));
    await api.apiClient.switchTenant("t2");
    fetchMock.mockResolvedValue(jsonResponse(204));

    await api.apiClient.removePassword();

    expect(tokenSent(1)).toBe("the-session");
    expect(tokenSent(2)).toBe("the-session");
  });

  it("names the page's tenant on every request, reads included, and on the streams", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(200, { ...signedInProfile, csrf_token: "tok" }));
    await api.apiClient.me();
    fetchMock.mockImplementation(() => Promise.resolve(jsonResponse(200, { tickets: [], id: 1 })));
    await api.apiClient.listTicketsPage({});
    await api.apiClient.createTicket({ title: "t" });
    expect(headerSent(fetchMock, 1, "X-Tenant-ID")).toBe("t1");
    expect(headerSent(fetchMock, 2, "X-Tenant-ID")).toBe("t1");

    const sources: string[] = [];
    vi.stubGlobal(
      "EventSource",
      class {
        constructor(url: string) {
          sources.push(url);
        }
        close() {}
      },
    );
    api.apiClient.streamBoardChanges(vi.fn());
    api.apiClient.streamBoardChanges(vi.fn(), 7);
    api.apiClient.streamConversation("c1", "s1", 3, vi.fn());
    expect(sources).toEqual([
      "/api/tickets/stream?tenant=t1",
      "/api/tickets/stream?since=7&tenant=t1",
      "/api/conductors/c1/conversations/s1/stream?after=3&tenant=t1",
    ]);
  });

  // The two calls that move the session into another tenant tell the other
  // tabs, whose cookies just changed under them.
  it("tells other tabs when this one switches tenant or accepts an invite", async () => {
    const posted: unknown[] = [];
    vi.stubGlobal(
      "BroadcastChannel",
      class {
        onmessage = null;
        postMessage(message: unknown) {
          posted.push(message);
        }
        close() {}
      },
    );
    ({ fetchMock, api } = await installFetch());
    fetchMock.mockResolvedValueOnce(jsonResponse(200, { ...signedInProfile, csrf_token: "tok" }));
    await api.apiClient.me();

    fetchMock.mockResolvedValueOnce(jsonResponse(200, { ...signedInProfile, tenant_id: "t2", csrf_token: "tok" }));
    await api.apiClient.switchTenant("t2");
    fetchMock.mockResolvedValueOnce(jsonResponse(200, { ...signedInProfile, tenant_id: "t3", csrf_token: "tok" }));
    await api.apiClient.acceptInvite("invite-token");

    expect(posted).toEqual([
      { from: "t1", tenant_id: "t2" },
      { from: "t2", tenant_id: "t3" },
    ]);
  });

  it("forgets the token on sign-out", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(200, { ...signedInProfile, csrf_token: "the-session" }));
    await api.apiClient.me();
    fetchMock.mockResolvedValue(jsonResponse(204));
    await api.apiClient.logout();
    await api.apiClient.removePassword().catch(() => {});

    expect(tokenSent(1)).toBeUndefined();
    expect(tokenSent(2)).toBeUndefined();
  });

  // A session from before CSRF tokens can read but never write, so the page
  // treats it as no session at all -- sign in again -- rather than showing
  // a board whose first change fails.
  it("treats a session without a CSRF token as signed out", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(200, { ...signedInProfile, csrf_token: "" }));

    await expect(api.apiClient.me()).rejects.toMatchObject({ status: 401, code: "csrf" });

    expect(signedOut).not.toHaveBeenCalled();
  });

  // The server's "csrf" refusal means this session cannot write -- another
  // tab signed in since, say. It is the session being over: announced, and
  // never retried as whoever the browser is now signed in as.
  it("announces the sign-out on a csrf refusal, without retrying", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(200, { ...signedInProfile, csrf_token: "stale" }));
    await api.apiClient.me();
    fetchMock.mockResolvedValue(jsonResponse(403, { detail: "sign in again", code: "csrf" }));

    await expect(api.apiClient.createTicket({ title: "t" })).rejects.toMatchObject({ status: 403, code: "csrf" });

    expect(urlsOf(fetchMock)).toEqual(["/api/auth/me", "/api/tickets"]);
    expect(signedOut).toHaveBeenCalledTimes(1);
    fetchMock.mockResolvedValue(jsonResponse(204));
    await api.apiClient.removePassword();
    expect(tokenSent(2)).toBeUndefined();
  });

  it("announces the sign-out on a csrf refusal of the retry after a refresh", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse(401))
      .mockResolvedValueOnce(jsonResponse(204))
      .mockResolvedValueOnce(jsonResponse(403, { code: "csrf" }));

    await expect(api.apiClient.createTicket({ title: "t" })).rejects.toMatchObject({ status: 403 });

    expect(urlsOf(fetchMock)).toEqual(["/api/tickets", "/api/auth/refresh", "/api/tickets"]);
    expect(signedOut).toHaveBeenCalledTimes(1);
  });

  // Any other 403 -- a step-up needed, a non-member -- is the answer, not
  // the session ending.
  it("leaves other refusals alone", async () => {
    fetchMock.mockResolvedValue(jsonResponse(403, { code: "reauth_required" }));

    await expect(api.apiClient.removePassword()).rejects.toMatchObject({ status: 403, code: "reauth_required" });

    expect(signedOut).not.toHaveBeenCalled();
  });

  it("refreshes an expired session and retries the request", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(200, { ...signedInProfile, csrf_token: "the-session" }));
    await api.apiClient.me();
    fetchMock
      .mockResolvedValueOnce(jsonResponse(401))
      .mockResolvedValueOnce(jsonResponse(204))
      .mockResolvedValueOnce(jsonResponse(200, { id: 1 }));

    const ticket = await api.apiClient.createTicket({ title: "t" });

    expect(ticket).toEqual({ id: 1 });
    expect(urlsOf(fetchMock)).toEqual(["/api/auth/me", "/api/tickets", "/api/auth/refresh", "/api/tickets"]);
    // The refresh is public: it carries no token, and the retry carries
    // the same one -- a refresh does not change the session.
    expect(tokenSent(2)).toBeUndefined();
    expect(tokenSent(3)).toBe("the-session");
    expect(signedOut).not.toHaveBeenCalled();
  });

  // A board full of panels loads at once; when the token has expired they
  // all fail at once, and one refresh must serve them all.
  it("shares one refresh between requests that fail together", async () => {
    let releaseRefresh: (r: Response) => void = () => {};
    fetchMock.mockImplementation((input) => {
      const url = urlOf(input);
      if (url === "/api/auth/refresh") return new Promise<Response>((resolve) => (releaseRefresh = resolve));
      const retried = fetchMock.mock.calls.filter((c) => urlOf(c[0]) === url).length > 1;
      return Promise.resolve(retried ? jsonResponse(200, { tickets: [], agents: [] }) : jsonResponse(401));
    });

    const both = Promise.all([api.apiClient.listTicketsPage({}), api.apiClient.listAgents()]);
    await vi.waitFor(() => expect(urlsOf(fetchMock)).toContain("/api/auth/refresh"));
    releaseRefresh(jsonResponse(204));
    await both;

    expect(urlsOf(fetchMock).filter((u) => u === "/api/auth/refresh")).toHaveLength(1);
  });

  it("announces the sign-out when the session cannot be refreshed", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(401)).mockResolvedValueOnce(jsonResponse(401));

    await expect(api.apiClient.listTicketsPage({})).rejects.toMatchObject({ status: 401 });

    expect(signedOut).toHaveBeenCalledTimes(1);
  });

  it("announces the sign-out when the retry is refused too", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse(401))
      .mockResolvedValueOnce(jsonResponse(204))
      .mockResolvedValueOnce(jsonResponse(401));

    await expect(api.apiClient.listTicketsPage({})).rejects.toMatchObject({ status: 401 });

    expect(signedOut).toHaveBeenCalledTimes(1);
  });

  // A request from the old session can still be failing when the user has
  // already signed in again; its verdict is about a session that is gone.
  it("does not sign out a session begun while a stale request was failing", async () => {
    let releaseRefresh: (r: Response) => void = () => {};
    fetchMock.mockImplementation((input) => {
      const url = urlOf(input);
      if (url === "/api/auth/refresh") return new Promise<Response>((resolve) => (releaseRefresh = resolve));
      if (url === "/api/auth/login") return Promise.resolve(jsonResponse(200, { id: "u1" }));
      return Promise.resolve(jsonResponse(401));
    });

    const stale = api.apiClient.listTicketsPage({}).catch(() => {});
    await vi.waitFor(() => expect(urlsOf(fetchMock)).toContain("/api/auth/refresh"));
    await api.apiClient.login("a@example.com", "pw");
    releaseRefresh(jsonResponse(401));
    await stale;

    expect(signedOut).not.toHaveBeenCalled();
  });

  // A wrong password is a 401 too, and must not trigger a refresh or look
  // like an expired session.
  it("does not refresh or sign out on a failed sign-in", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(401, { detail: "invalid email or password" }));

    await expect(api.apiClient.login("a@example.com", "wrong")).rejects.toMatchObject({ status: 401 });

    expect(urlsOf(fetchMock)).toEqual(["/api/auth/login"]);
    expect(signedOut).not.toHaveBeenCalled();
  });

  // getAuthConfig, previewInvite and acceptInviteAsNewAccount all run
  // before there's a session to refresh (the sign-in page itself, and an
  // invite link possibly opened by someone with no account at all) -- a
  // 401 from any of them has to be taken at face value, the same as a
  // wrong password above, not retried through refreshSession the way an
  // expired *session's* 401 is.
  it("does not refresh on a 401 from the public auth-config route", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(401));

    await expect(api.apiClient.getAuthConfig()).rejects.toMatchObject({ status: 401 });

    expect(urlsOf(fetchMock)).toEqual(["/api/auth/config"]);
    expect(signedOut).not.toHaveBeenCalled();
  });

  it("does not refresh on an invalid-token 400 from previewInvite", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(400, { detail: "invalid or expired token" }));

    await expect(api.apiClient.previewInvite("dead-token")).rejects.toMatchObject({ status: 400 });

    expect(urlsOf(fetchMock)).toEqual(["/api/auth/invite/preview"]);
    expect(signedOut).not.toHaveBeenCalled();
  });

  it("does not refresh on a 409 from acceptInviteAsNewAccount", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(409, { detail: "that address already has an account" }));

    await expect(api.apiClient.acceptInviteAsNewAccount("t1", "Bea", "a-strong-password")).rejects.toMatchObject({
      status: 409,
    });

    expect(urlsOf(fetchMock)).toEqual(["/api/auth/invite/accept"]);
    expect(signedOut).not.toHaveBeenCalled();
  });

  // On first load there is nothing to sign out of: me() tries a refresh --
  // the access cookie may simply have expired -- but a final 401 is only
  // the answer to "is anyone signed in".
  it("probes for a session without announcing a sign-out", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(401)).mockResolvedValueOnce(jsonResponse(401));

    await expect(api.apiClient.me()).rejects.toMatchObject({ status: 401 });

    expect(urlsOf(fetchMock)).toEqual(["/api/auth/me", "/api/auth/refresh"]);
    expect(signedOut).not.toHaveBeenCalled();
  });

  it("resolves the no-content auth routes", async () => {
    fetchMock.mockResolvedValue(jsonResponse(204));
    await expect(api.apiClient.logout()).resolves.toBeUndefined();
    fetchMock.mockResolvedValue(new Response(null, { status: 202 }));
    await expect(api.apiClient.requestPasswordReset("a@example.com")).resolves.toBeUndefined();
  });
});

// jsdom has no EventSource; this fake records each connection and lets a
// test play the browser's part -- delivering messages, or closing the
// stream the way a refused (401) reconnect does.
class FakeEventSource {
  static readonly CLOSED = 2;
  static instances: FakeEventSource[] = [];
  readyState = 1;
  onmessage: ((event: MessageEvent<string>) => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;

  url: string;

  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }

  close() {
    this.closed = true;
    this.readyState = FakeEventSource.CLOSED;
  }

  refuse() {
    this.readyState = FakeEventSource.CLOSED;
    this.onerror?.();
  }
}

describe("board stream across an expired session", () => {
  let fetchMock: FetchMock;
  let api: Api;
  let signedOut: ReturnType<typeof vi.fn<() => void>>;

  beforeEach(async () => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    ({ fetchMock, api } = await installFetch());
    signedOut = vi.fn<() => void>();
    api.onSignedOut(signedOut);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("refreshes and reconnects from the last revision when the stream is refused", async () => {
    fetchMock.mockResolvedValue(jsonResponse(204));
    const onMessage = vi.fn();
    api.apiClient.streamBoardChanges(onMessage, 5);
    const first = FakeEventSource.instances[0];
    first.onmessage?.({ data: JSON.stringify({ revision: 9 }) } as MessageEvent<string>);

    first.refuse();

    await vi.waitFor(() => expect(FakeEventSource.instances).toHaveLength(2));
    expect(urlsOf(fetchMock)).toEqual(["/api/auth/refresh"]);
    expect(FakeEventSource.instances[1].url).toBe("/api/tickets/stream?since=9");
  });

  // A transient drop is the browser's to retry; only a closed stream is
  // this wrapper's business.
  it("leaves a stream the browser is still retrying alone", async () => {
    api.apiClient.streamBoardChanges(vi.fn());
    FakeEventSource.instances[0].onerror?.();

    await Promise.resolve();
    expect(fetchMock).not.toHaveBeenCalled();
    expect(FakeEventSource.instances).toHaveLength(1);
  });

  it("announces the sign-out when the session cannot be refreshed", async () => {
    fetchMock.mockResolvedValue(jsonResponse(401));
    api.apiClient.streamBoardChanges(vi.fn());

    FakeEventSource.instances[0].refuse();

    await vi.waitFor(() => expect(signedOut).toHaveBeenCalledTimes(1));
    expect(FakeEventSource.instances).toHaveLength(1);
  });

  // The caller can close the stream -- navigating away from the board -- while
  // the refresh is still in flight; the refresh finishing must not bring
  // it back.
  it("does not reconnect when closed while the refresh is in flight", async () => {
    let releaseRefresh: (r: Response) => void = () => {};
    fetchMock.mockImplementation(() => new Promise<Response>((resolve) => (releaseRefresh = resolve)));
    const close = api.apiClient.streamBoardChanges(vi.fn());
    FakeEventSource.instances[0].refuse();
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalled());

    close();
    releaseRefresh(jsonResponse(204));

    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(FakeEventSource.instances).toHaveLength(1);
  });

  // A step-up's wrong password is a 401 too. It must be the answer --
  // not read as an expired session, which would refresh, send the
  // password a second time, and sign the person out.
  it("a step-up's wrong password is refused without a refresh or a sign-out", async () => {
    fetchMock.mockResolvedValue(jsonResponse(401, { detail: "invalid email or password", code: "invalid_credentials" }));

    await expect(api.apiClient.reauthWithPassword("wrong")).rejects.toMatchObject({
      status: 401,
      code: "invalid_credentials",
    });

    expect(urlsOf(fetchMock)).toEqual(["/api/auth/reauth/password"]);
    expect(signedOut).not.toHaveBeenCalled();
  });

  it("a step-up on an expired session still refreshes and retries", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse(401, { detail: "session expired", code: "invalid_session" }))
      .mockResolvedValueOnce(jsonResponse(204))
      .mockResolvedValueOnce(jsonResponse(204));

    await api.apiClient.reauthWithPassword("right");

    expect(urlsOf(fetchMock)).toEqual(["/api/auth/reauth/password", "/api/auth/refresh", "/api/auth/reauth/password"]);
  });

  // The access token can expire *and* the retyped password be wrong: the
  // first 401 looks like an expired session (no code), so it refreshes and
  // retries -- and that retry's 401 is the wrong-credential answer, not a
  // second expired session. It must be read the same way the first
  // response's 401 would have been, rather than falling through to
  // announceSignedOut and signing a still-valid session out.
  it("a wrong credential on the post-refresh retry is refused without a sign-out", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse(401, { detail: "session expired" }))
      .mockResolvedValueOnce(jsonResponse(204))
      .mockResolvedValueOnce(jsonResponse(401, { detail: "invalid email or password", code: "invalid_credentials" }));

    await expect(api.apiClient.reauthWithPassword("wrong")).rejects.toMatchObject({
      status: 401,
      code: "invalid_credentials",
    });

    expect(urlsOf(fetchMock)).toEqual(["/api/auth/reauth/password", "/api/auth/refresh", "/api/auth/reauth/password"]);
    expect(signedOut).not.toHaveBeenCalled();
  });

  it("carries the server's code on a refusal", async () => {
    fetchMock.mockResolvedValue(jsonResponse(403, { detail: "confirm it's you", code: "reauth_required" }));

    await expect(api.apiClient.startLink("dex", "/account")).rejects.toMatchObject({
      status: 403,
      code: "reauth_required",
      message: "confirm it's you",
    });
    const req = await sent(fetchMock);
    expect(req.url).toBe("/api/auth/link/dex");
    expect(req.method).toBe("POST");
    expect(req.body).toEqual({ next: "/account" });
  });
});
