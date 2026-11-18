import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Middleware } from "openapi-fetch";
import { CSRF_HEADER } from "../session/session_client";
import { ApiError, BulkRefusedError, createApiClients, errorCode } from "./api_client";

interface TestPaths {
  "/api/things": {
    get: { responses: { 200: { content: { "application/json": { name: string } } } } };
    post: { responses: { 200: { content: { "application/json": { name: string } } } } };
  };
}

const json = (status: number, body: unknown, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json", ...headers } });

interface Sent {
  method: string;
  path: string;
  csrf: string | null;
}

describe("createApiClients", () => {
  let sent: Sent[];
  let answers: Response[];

  beforeEach(() => {
    sent = [];
    answers = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const request = input instanceof Request ? input : new Request(input, init);
        sent.push({
          method: request.method,
          path: new URL(request.url).pathname,
          csrf: request.headers.get(CSRF_HEADER),
        });
        return answers.shift() ?? json(200, { name: "ok" });
      }),
    );
  });
  afterEach(() => vi.unstubAllGlobals());

  const make = (options: Partial<Parameters<typeof createApiClients>[0]> = {}) =>
    createApiClients<TestPaths>({ baseUrl: "http://api.test", ...options });

  describe("unwrap", () => {
    it("returns the data of a successful call, from a result or a promise of one", async () => {
      const { client, unwrap } = make();
      const result = await client.GET("/api/things");
      expect(unwrap(result)).toEqual({ name: "ok" });
      expect(await unwrap(client.GET("/api/things"))).toEqual({ name: "ok" });
    });

    it("raises an ApiError with the envelope's detail and code", async () => {
      const { client, unwrap } = make();
      answers.push(json(409, { detail: "taken", code: "name_taken" }));
      const err = await unwrap(client.GET("/api/things")).catch((e: unknown) => e);
      expect(err).toBeInstanceOf(ApiError);
      expect(err).not.toBeInstanceOf(BulkRefusedError);
      expect(err).toMatchObject({ status: 409, message: "taken", code: "name_taken" });
    });

    it("falls back to the status line when the body has no detail", async () => {
      const { client, unwrap } = make();
      answers.push(new Response("bad gateway", { status: 502, statusText: "Bad Gateway" }));
      await expect(unwrap(client.GET("/api/things"))).rejects.toMatchObject({
        status: 502,
        message: "Bad Gateway",
        code: undefined,
      });
    });

    it("falls back to a generic message with no detail and no status line", async () => {
      const { client, unwrap } = make();
      answers.push(new Response("", { status: 500 }));
      await expect(unwrap(client.GET("/api/things"))).rejects.toMatchObject({
        status: 500,
        message: "request failed with status 500",
      });
    });

    it("raises on a failure whose body is empty, which openapi-fetch reports with no error", async () => {
      const { client, unwrap } = make();
      answers.push(new Response(null, { status: 500, headers: { "Content-Length": "0", "Content-Type": "application/json" } }));
      await expect(unwrap(client.GET("/api/things"))).rejects.toMatchObject({ status: 500 });
    });

    it("raises a BulkRefusedError carrying the refusals for code bulk_refused", async () => {
      const { client, unwrap } = make();
      const refusals = [{ id: "a", reason: "locked" }];
      answers.push(json(409, { detail: "refused", code: "bulk_refused", refusals }));
      const err = await unwrap(client.GET("/api/things")).catch((e: unknown) => e);
      expect(err).toBeInstanceOf(BulkRefusedError);
      expect(err).toBeInstanceOf(ApiError);
      expect(err).toMatchObject({ status: 409, code: "bulk_refused", refusals });
    });

    it("raises a plain ApiError for bulk_refused without a refusals list", async () => {
      const { client, unwrap } = make();
      answers.push(json(409, { detail: "refused", code: "bulk_refused" }));
      const err = await unwrap(client.GET("/api/things")).catch((e: unknown) => e);
      expect(err).not.toBeInstanceOf(BulkRefusedError);
      expect(err).toMatchObject({ status: 409, code: "bulk_refused" });
    });

    it("lets an app turn an envelope into its own error type", async () => {
      class InUse extends ApiError {}
      const { client, unwrap } = make({
        refine: ({ status, detail, code }) => (code === "in_use" ? new InUse(status, detail, code) : undefined),
      });
      answers.push(json(409, { detail: "busy", code: "in_use" }), json(409, { detail: "x", code: "other" }));
      expect(await unwrap(client.GET("/api/things")).catch((e: unknown) => e)).toBeInstanceOf(InUse);
      expect(await unwrap(client.GET("/api/things")).catch((e: unknown) => e)).not.toBeInstanceOf(InUse);
    });

    it("tells onApiError the status and code only, and survives a throwing listener", async () => {
      const { client, unwrap, onApiError } = make();
      const heard = vi.fn();
      onApiError(() => {
        throw new Error("listener broke");
      });
      const off = onApiError(heard);
      answers.push(json(403, { detail: "secret detail", code: "nope" }), json(403, { detail: "d", code: "nope" }));
      await expect(unwrap(client.GET("/api/things"))).rejects.toMatchObject({ message: "secret detail" });
      expect(heard).toHaveBeenCalledWith({ status: 403, code: "nope" });
      off();
      await expect(unwrap(client.GET("/api/things"))).rejects.toBeInstanceOf(ApiError);
      expect(heard).toHaveBeenCalledTimes(1);
    });
  });

  describe("per-mode clients", () => {
    // Each case: which client is used, and what a first 401 from the route
    // leads to (a refresh, then the retry's answer).
    const refreshes = () => sent.filter((s) => s.path === "/api/auth/refresh").length;

    it("session: refreshes an expired access token and retries once", async () => {
      const { client, unwrap } = make();
      answers.push(json(401, { detail: "expired" }), json(200, {}), json(200, { name: "again" }));
      expect(await unwrap(client.GET("/api/things"))).toEqual({ name: "again" });
      expect(refreshes()).toBe(1);
      expect(sent.filter((s) => s.path === "/api/things")).toHaveLength(2);
    });

    it("session: announces a session that cannot be refreshed", async () => {
      const { client, unwrap, onSignedOut } = make();
      const signedOut = vi.fn();
      onSignedOut(signedOut);
      answers.push(json(401, { detail: "expired" }), json(401, { detail: "gone" }));
      await expect(unwrap(client.GET("/api/things"))).rejects.toMatchObject({ status: 401 });
      expect(signedOut).toHaveBeenCalledTimes(1);
    });

    it("probe: refreshes, but a final 401 is not announced", async () => {
      const { probeClient, unwrap, onSignedOut } = make();
      const signedOut = vi.fn();
      onSignedOut(signedOut);
      answers.push(json(401, { detail: "no" }), json(401, { detail: "no" }));
      await expect(unwrap(probeClient.GET("/api/things"))).rejects.toMatchObject({ status: 401 });
      expect(refreshes()).toBe(1);
      expect(signedOut).not.toHaveBeenCalled();
    });

    it("public: a 401 is the answer, with no refresh and no sign-out", async () => {
      const { publicClient, unwrap, onSignedOut } = make();
      const signedOut = vi.fn();
      onSignedOut(signedOut);
      answers.push(json(401, { detail: "wrong password", code: "invalid_credentials" }));
      await expect(unwrap(publicClient.POST("/api/things"))).rejects.toMatchObject({
        status: 401,
        code: "invalid_credentials",
      });
      expect(refreshes()).toBe(0);
      expect(signedOut).not.toHaveBeenCalled();
    });

    it("credential: a 401 saying the password was wrong neither refreshes nor signs out", async () => {
      const { credentialClient, unwrap, onSignedOut } = make();
      const signedOut = vi.fn();
      onSignedOut(signedOut);
      answers.push(json(401, { detail: "wrong", code: "invalid_credentials" }));
      await expect(unwrap(credentialClient.POST("/api/things"))).rejects.toMatchObject({ code: "invalid_credentials" });
      expect(refreshes()).toBe(0);
      expect(signedOut).not.toHaveBeenCalled();
    });

    it("sends the session's CSRF token on writes only, through sessionWriteHeaders too", async () => {
      const { client, session, unwrap, sessionWriteHeaders } = make();
      expect(sessionWriteHeaders()).toEqual({});
      session.remember({ csrf_token: "tok" });
      expect(sessionWriteHeaders()).toEqual({ [CSRF_HEADER]: "tok" });
      await unwrap(client.GET("/api/things"));
      await unwrap(client.POST("/api/things"));
      expect(sent.map((s) => s.csrf)).toEqual([null, "tok"]);
    });
  });

  describe("options", () => {
    it("attaches the middleware to every mode's client, in order", async () => {
      const order: string[] = [];
      const tag =
        (name: string): Middleware => ({
          onRequest({ request }) {
            order.push(name);
            return request;
          },
        });
      const clients = make({ middleware: [tag("a"), tag("b")] });
      for (const c of [clients.client, clients.publicClient, clients.credentialClient, clients.probeClient]) {
        order.length = 0;
        await c.GET("/api/things");
        expect(order).toEqual(["a", "b"]);
      }
    });

    it("sends every mode's requests through the app's own fetchFor", async () => {
      const served: string[] = [];
      const clients = make({
        fetchFor: (_session, mode) => async () => {
          served.push(mode);
          return json(200, { name: mode });
        },
      });
      expect(await clients.unwrap(clients.client.GET("/api/things"))).toEqual({ name: "session" });
      expect(await clients.unwrap(clients.publicClient.GET("/api/things"))).toEqual({ name: "public" });
      expect(await clients.unwrap(clients.credentialClient.GET("/api/things"))).toEqual({ name: "credential" });
      expect(await clients.unwrap(clients.probeClient.GET("/api/things"))).toEqual({ name: "probe" });
      expect(served).toEqual(["session", "public", "credential", "probe"]);
      expect(sent).toEqual([]);
    });

    it("refreshes at baseUrl's /api/auth/refresh by default", async () => {
      const { client, unwrap } = make();
      answers.push(json(401, {}), json(200, {}), json(200, { name: "x" }));
      await unwrap(client.GET("/api/things"));
      expect(sent.map((s) => s.path)).toContain("/api/auth/refresh");
    });

    it("refreshes at refreshUrl when given one", async () => {
      const { client, unwrap } = make({ refreshUrl: "http://api.test/custom/refresh" });
      answers.push(json(401, {}), json(200, {}), json(200, { name: "x" }));
      await unwrap(client.GET("/api/things"));
      expect(sent.map((s) => s.path)).toContain("/custom/refresh");
      expect(sent.map((s) => s.path)).not.toContain("/api/auth/refresh");
    });
  });
});

describe("errorCode", () => {
  it("reads a string code and nothing else", () => {
    expect(errorCode({ code: "x" })).toBe("x");
    expect(errorCode({ code: 3 })).toBeUndefined();
    expect(errorCode("text")).toBeUndefined();
    expect(errorCode(null)).toBeUndefined();
  });
});
