import { beforeEach, describe, expect, it, vi } from "vitest";
import { CSRF_HEADER, SessionClient } from "./session_client";

// The wire name go-auth sets, spelled out so a rename in the client fails here.
const REJECTED_HEADER = "X-Auth-Credential";

function jsonResponse(status: number, body: unknown = {}): Response {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// What each call to the fake fetch was: its URL, method, CSRF header and
// body -- read from a Request or from (url, init), whichever it was given.
interface Sent {
  url: string;
  method: string;
  csrf: string | null;
  body: string | null;
}

// What an OptionalUser route answers when the session credential was
// refused: an ordinary anonymous answer, flagged.
function rejectedResponse(body: unknown = {}): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json", [REJECTED_HEADER]: "rejected" },
  });
}

describe("SessionClient", () => {
  let sent: Sent[];
  let answers: Response[];
  let session: SessionClient;
  let signedOut: ReturnType<typeof vi.fn<() => void>>;

  beforeEach(() => {
    sent = [];
    answers = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : new Request(new URL(String(input), "http://app.test"), init);
      sent.push({
        url: new URL(request.url).pathname,
        method: request.method,
        csrf: request.headers.get(CSRF_HEADER),
        body: request.body ? await request.text() : null,
      });
      return answers.shift() ?? jsonResponse(200);
    });
    session = new SessionClient({ refreshUrl: "http://app.test/api/auth/refresh", fetch: fetchMock });
    signedOut = vi.fn<() => void>();
    session.onSignedOut(signedOut);
  });

  const request = (method: string, path: string, body?: unknown) =>
    new Request(`http://app.test${path}`, {
      method,
      body: body === undefined ? undefined : JSON.stringify(body),
      headers: { "Content-Type": "application/json" },
    });

  it("takes the CSRF token off a session body and sends it on writes only", async () => {
    expect(session.signedIn({ id: "u1", csrf_token: "tok" })).toEqual({ id: "u1" });

    await session.sendRequest(request("GET", "/api/things"));
    await session.sendRequest(request("POST", "/api/things", { a: 1 }));
    await session.sendRequest(request("DELETE", "/api/things/1"));

    expect(sent.map((s) => s.csrf)).toEqual([null, "tok", "tok"]);
  });

  it("offers the CSRF token as headers for a sender of its own, and none without a session", () => {
    expect(session.writeHeaders()).toEqual({});
    session.signedIn({ id: "u1", csrf_token: "tok" });
    expect(session.writeHeaders()).toEqual({ [CSRF_HEADER]: "tok" });
    session.forget();
    expect(session.writeHeaders()).toEqual({});
  });

  // The retry is a second send of the same request: its body must still be
  // there, which a consumed Request's would not be.
  it("refreshes once on a 401 and retries the request with its body", async () => {
    session.remember({ csrf_token: "tok" });
    answers = [jsonResponse(401), jsonResponse(204), jsonResponse(201, { id: 1 })];

    const response = await session.sendRequest(request("POST", "/api/things", { name: "x" }));

    expect(response.status).toBe(201);
    expect(sent.map((s) => `${s.method} ${s.url}`)).toEqual([
      "POST /api/things",
      "POST /api/auth/refresh",
      "POST /api/things",
    ]);
    expect(sent[2].body).toBe(JSON.stringify({ name: "x" }));
    expect(sent[1].csrf).toBeNull();
    expect(signedOut).not.toHaveBeenCalled();
  });

  it("announces the session is over when the refresh fails", async () => {
    answers = [jsonResponse(401), jsonResponse(401)];
    const response = await session.sendRequest(request("GET", "/api/things"));
    expect(response.status).toBe(401);
    expect(sent.map((s) => s.url)).toEqual(["/api/things", "/api/auth/refresh"]);
    expect(signedOut).toHaveBeenCalledTimes(1);
  });

  it("announces the session is over when the retry after a refresh is refused too", async () => {
    answers = [jsonResponse(401), jsonResponse(204), jsonResponse(401)];
    const response = await session.sendRequest(request("GET", "/api/things"));
    expect(response.status).toBe(401);
    expect(sent.map((s) => s.url)).toEqual(["/api/things", "/api/auth/refresh", "/api/things"]);
    expect(signedOut).toHaveBeenCalledTimes(1);
  });

  it("probes with a refresh and retry, answering whatever the retry says", async () => {
    answers = [jsonResponse(401), jsonResponse(204), jsonResponse(200, { id: "u1" })];
    const response = await session.sendRequest(request("GET", "/api/auth/me"), "probe");
    expect(response.status).toBe(200);
    expect(sent.map((s) => s.url)).toEqual(["/api/auth/me", "/api/auth/refresh", "/api/auth/me"]);
  });

  // A step-up's wrong password is the answer, not an expired session.
  it("takes a credential request's wrong password at face value", async () => {
    answers = [jsonResponse(401, { code: "invalid_credentials" })];
    const response = await session.sendRequest(request("POST", "/api/reauth", {}), "credential");
    expect(response.status).toBe(401);
    expect(sent).toHaveLength(1);
    expect(signedOut).not.toHaveBeenCalled();
  });

  it("treats any other 401 on a credential request as an expired session", async () => {
    answers = [jsonResponse(401), jsonResponse(401)];
    await session.sendRequest(request("POST", "/api/reauth", {}), "credential");
    expect(sent.map((s) => s.url)).toEqual(["/api/reauth", "/api/auth/refresh"]);
    expect(signedOut).toHaveBeenCalledTimes(1);
  });

  it("takes a public route's 401 at face value", async () => {
    answers = [jsonResponse(401, { code: "invalid_credentials" })];
    const response = await session.sendRequest(request("POST", "/api/auth/login", {}), "public");
    expect(response.status).toBe(401);
    expect(sent).toHaveLength(1);
    expect(signedOut).not.toHaveBeenCalled();
  });

  it("probes without announcing a sign-out", async () => {
    answers = [jsonResponse(401), jsonResponse(401)];
    await session.sendRequest(request("GET", "/api/auth/me"), "probe");
    expect(sent.map((s) => s.url)).toEqual(["/api/auth/me", "/api/auth/refresh"]);
    expect(signedOut).not.toHaveBeenCalled();
  });

  // A 403 "csrf" means this page's token is for a session the browser no
  // longer holds: never retried, and the session is over.
  it("treats a csrf refusal as the session being over", async () => {
    session.remember({ csrf_token: "stale" });
    answers = [jsonResponse(403, { code: "csrf" })];
    const response = await session.sendRequest(request("POST", "/api/things", {}));
    expect(response.status).toBe(403);
    expect(sent).toHaveLength(1);
    expect(signedOut).toHaveBeenCalledTimes(1);
  });

  it("makes one refresh for a burst of expired requests", async () => {
    let release: (r: Response) => void = () => {};
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = input instanceof Request ? new URL(input.url).pathname : String(input);
      if (url.endsWith("/api/auth/refresh")) return new Promise<Response>((resolve) => (release = resolve));
      return Promise.resolve(jsonResponse(fetchMock.mock.calls.length <= 2 ? 401 : 200));
    });
    const s = new SessionClient({ refreshUrl: "http://app.test/api/auth/refresh", fetch: fetchMock });

    const both = Promise.all([s.sendRequest(request("GET", "/a")), s.sendRequest(request("GET", "/b"))]);
    await vi.waitFor(() => expect(fetchMock.mock.calls.length).toBe(3));
    release(jsonResponse(204));
    await both;

    const refreshes = fetchMock.mock.calls.filter(([input]) => String(input).endsWith("/api/auth/refresh"));
    expect(refreshes).toHaveLength(1);
  });

  // A request from before a new sign-in can still be failing afterwards;
  // its verdict is about a session that is gone.
  it("does not sign out a session begun while a stale request was failing", async () => {
    const generation = session.generation();
    session.signedIn({ csrf_token: "new" });
    session.announceSignedOut(generation);
    expect(signedOut).not.toHaveBeenCalled();
  });

  // The write belongs to the account that issued it. If someone else signs in
  // while its refresh is in flight, replaying it would run it under the new
  // account's cookies and CSRF token.
  it.each(["session", "probe"] as const)(
    "does not replay a %s request whose session was replaced during the refresh",
    async (mode) => {
      session.signedIn({ csrf_token: "old" });
      let finishRefresh: (r: Response) => void = () => {};
      const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const request = input instanceof Request ? input : new Request(new URL(String(input), "http://app.test"), init);
        sent.push({ url: new URL(request.url).pathname, method: request.method, csrf: request.headers.get(CSRF_HEADER), body: null });
        if (request.url.endsWith("/api/auth/refresh")) return new Promise<Response>((resolve) => (finishRefresh = resolve));
        return Promise.resolve(jsonResponse(401));
      });
      const s = new SessionClient({ refreshUrl: "http://app.test/api/auth/refresh", fetch: fetchMock });
      const listener = vi.fn<() => void>();
      s.onSignedOut(listener);
      s.signedIn({ csrf_token: "old" });

      const pending = s.sendRequest(request("POST", "/api/things", { a: 1 }), mode);
      await vi.waitFor(() => expect(sent.some((x) => x.url === "/api/auth/refresh")).toBe(true));
      s.signedIn({ csrf_token: "new" });
      finishRefresh(jsonResponse(204));
      const response = await pending;

      expect(response.status).toBe(401);
      expect(sent.map((x) => `${x.method} ${x.url} ${x.csrf}`)).toEqual([
        "POST /api/things old",
        "POST /api/auth/refresh null",
      ]);
      expect(listener).not.toHaveBeenCalled();
    },
  );

  it("does not let a new session's request ride a refresh begun by the old one", async () => {
    const releases: ((r: Response) => void)[] = [];
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      if (String(input).endsWith("/api/auth/refresh")) return new Promise<Response>((resolve) => releases.push(resolve));
      return Promise.resolve(jsonResponse(200));
    });
    const s = new SessionClient({ refreshUrl: "http://app.test/api/auth/refresh", fetch: fetchMock });

    const old = s.refresh();
    s.signedIn({ csrf_token: "new" });
    const fresh = s.refresh();
    expect(releases).toHaveLength(2);
    releases.forEach((r) => r(jsonResponse(204)));
    await expect(Promise.all([old, fresh])).resolves.toEqual([true, true]);
    // A refresh finishing late does not clear the newer one's slot.
    expect(s.refresh()).not.toBe(old);
  });

  it("sends through (url, init) as well, with the token on writes", async () => {
    session.remember({ csrf_token: "tok" });
    await session.send("http://app.test/api/things", { method: "PUT", body: "{}" });
    await session.send("http://app.test/api/things");
    expect(sent.map((s) => s.csrf)).toEqual(["tok", null]);
  });

  it("fetchFor binds a mode", async () => {
    answers = [jsonResponse(401)];
    await session.fetchFor("public")(request("POST", "/api/auth/login", {}));
    expect(sent).toHaveLength(1);
  });

  describe("rejected credential on a route that never answers 401", () => {
    it("refreshes once and retries a request flagged rejected", async () => {
      session.remember({ csrf_token: "tok" });
      answers = [rejectedResponse({ anonymous: true }), jsonResponse(204), jsonResponse(200, { id: "u1" })];

      const response = await session.sendRequest(request("PUT", "/api/consent", { a: 1 }));

      expect(response.status).toBe(200);
      expect(response.headers.get(REJECTED_HEADER)).toBeNull();
      expect(sent.map((s) => `${s.method} ${s.url}`)).toEqual([
        "PUT /api/consent",
        "POST /api/auth/refresh",
        "PUT /api/consent",
      ]);
      expect(sent[2].body).toBe(JSON.stringify({ a: 1 }));
      expect(signedOut).not.toHaveBeenCalled();
    });

    it("retries a probe the same way", async () => {
      answers = [rejectedResponse(), jsonResponse(204), jsonResponse(200, { id: "u1" })];
      const response = await session.sendRequest(request("GET", "/api/me"), "probe");
      expect(response.headers.get(REJECTED_HEADER)).toBeNull();
      expect(sent.map((s) => s.url)).toEqual(["/api/me", "/api/auth/refresh", "/api/me"]);
    });

    it("does not loop when the retry is rejected too, and announces the session over", async () => {
      answers = [rejectedResponse(), jsonResponse(204), rejectedResponse()];
      const response = await session.sendRequest(request("GET", "/api/me"));
      expect(response.headers.get(REJECTED_HEADER)).toBe("rejected");
      expect(sent.map((s) => s.url)).toEqual(["/api/me", "/api/auth/refresh", "/api/me"]);
      expect(signedOut).toHaveBeenCalledTimes(1);
    });

    it("announces the session over when the refresh fails, but not for a probe", async () => {
      answers = [rejectedResponse(), jsonResponse(401)];
      await session.sendRequest(request("GET", "/api/me"));
      expect(sent.map((s) => s.url)).toEqual(["/api/me", "/api/auth/refresh"]);
      expect(signedOut).toHaveBeenCalledTimes(1);

      sent.length = 0;
      answers = [rejectedResponse(), jsonResponse(401)];
      await session.sendRequest(request("GET", "/api/me"), "probe");
      expect(signedOut).toHaveBeenCalledTimes(1);
    });

    it("leaves a response without the header alone", async () => {
      answers = [jsonResponse(200, { id: "u1" })];
      const response = await session.sendRequest(request("GET", "/api/me"));
      expect(response.status).toBe(200);
      expect(sent).toHaveLength(1);
    });

    it("ignores any other value of the header", async () => {
      answers = [new Response("{}", { status: 200, headers: { [REJECTED_HEADER]: "ok" } })];
      await session.sendRequest(request("GET", "/api/me"));
      expect(sent).toHaveLength(1);
    });

    it("ignores the header on public and credential requests", async () => {
      answers = [rejectedResponse()];
      await session.sendRequest(request("POST", "/api/auth/login", {}), "public");
      answers = [rejectedResponse()];
      await session.sendRequest(request("POST", "/api/reauth", {}), "credential");
      expect(sent.map((s) => s.url)).toEqual(["/api/auth/login", "/api/reauth"]);
    });

    it("makes one refresh for a burst of rejected requests", async () => {
      let refreshes = 0;
      let release!: () => void;
      const gate = new Promise<void>((resolve) => (release = resolve));
      const seen = new Set<string>();
      const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
        const url = input instanceof Request ? input.url : String(input);
        if (url.endsWith("/refresh")) {
          refreshes++;
          await gate;
          return jsonResponse(204);
        }
        if (!seen.has(url)) {
          seen.add(url);
          return rejectedResponse();
        }
        return jsonResponse(200);
      });
      const burst = new SessionClient({ refreshUrl: "http://app.test/refresh", fetch: fetchMock });
      const all = ["/a", "/b", "/c"].map((p) => burst.sendRequest(request("GET", p)));
      await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(4));
      release();
      const responses = await Promise.all(all);
      expect(refreshes).toBe(1);
      expect(responses.map((r) => r.status)).toEqual([200, 200, 200]);
    });
  });
});
