// Every fetch-backed operation apiClient exports, pinned in one table: its
// method, path and query, the JSON body it sends, what it makes of the
// response (an envelope unwrapped, a CSRF token taken off, an empty
// response), and the session mode it runs in -- told apart by what a 401
// does. The table is checked against apiClient's own keys, so an operation
// added without a row here fails the suite rather than going untested.
// The *_api.test.ts files and session.test.ts go deeper on the behaviour that needs
// more than one row.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { SessionMode } from "@slop-factory/auth";
import * as fx from "@/test/fixtures";
import { openApiSchemas } from "@/test/openapi_schema";
import { installFetch, jsonResponse, sent, urlsOf, type FetchMock } from "./fetch_recorder";

type Api = typeof import("./index");
export type Client = Api["apiClient"];

export interface Operation {
  call: (client: Client) => Promise<unknown>;
  method: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  url: string;
  // The JSON body sent; omitted means none.
  body?: unknown;
  // What the server answers as JSON; omitted means 204 No Content.
  response?: unknown;
  // A non-JSON answer, in place of response.
  reply?: () => Response;
  // What the call resolves to; omitted means the response unchanged (or
  // undefined, for a 204).
  result?: unknown;
  mode?: SessionMode;
  // Whether a signed-in page's CSRF token goes with it; omitted means on
  // every write and no read.
  csrf?: boolean;
}

export const profile = fx.makeProfile({ id: "u1", email: "a@x.io", display_name: "Ada", tenant_id: "t1", is_admin: false });
export const sessionProfile = { ...profile, csrf_token: "tok" };
export const ticket = fx.makeTicket({ id: 1, title: "A ticket" });
export const agent = fx.makeAgent({ id: "a1", name: "builder" });
export const role = fx.makeAgentRole({ id: "r1", name: "frontend" });
export const invite = fx.makeInvite({ id: "i1", email: "b@x.io", kind: "join_tenant" });
export const graph = fx.makeBlockingGraph();
export const blockerList = fx.makeBlockerList({ id: 2, title: "Other", status: "open" });
export const scope = fx.makeHarnessConfigScope({ agent_id: null, revision: 1, updated_by: "u1" });
export const setting = fx.makeSetting({ name: "s", type: "bool", bool_val: true });
export const fleetVersion = {
  id: "v1",
  name: "house",
  version: 1,
  kind: "fleet" as const,
  body: "Be brief.",
  created_by: null,
  created_at: "2026-10-04T12:00:00Z",
  archived: true,
  archived_at: "2026-10-05T12:00:00Z",
};
export const variantVersion = fx.makeVariantVersion({ id: "vv1", name: "fast", version: 1, harness: "claude", settings: { model: "haiku" } });
export const decision = { approve: true as const, agent_id: "a1" };

export function shellScript(): Response {
  return new Response("#!/bin/sh\necho hi\n", {
    status: 200,
    headers: { "Content-Type": "text/x-shellscript", "Content-Disposition": 'attachment; filename="setup-x.sh"' },
  });
}

// replyTo is a row's successful answer.
function replyTo(op: Operation): Response {
  if (op.reply) return op.reply();
  return op.response === undefined ? jsonResponse(204) : jsonResponse(200, op.response);
}


// describeOperations runs every row of one resource's table through the
// checks below, and checks the table has a row for each operation the
// resource exports.
export function describeOperations(
  title: string,
  load: () => Promise<Record<string, unknown>>,
  operations: Record<string, Operation>,
  notFetched: string[] = [],
): void {
  const rows = Object.entries(operations);

  describe(title, () => {
    let fetchMock: FetchMock;
    let api: Api;

    beforeEach(async () => {
      ({ fetchMock, api } = await installFetch());
    });

    afterEach(() => {
      vi.unstubAllGlobals();
    });

    it("has a row for every operation the resource exports", async () => {
      const covered = [...Object.keys(operations), ...notFetched].sort();
      expect(Object.keys(await load()).sort()).toEqual(covered);
    });

    it.each(rows)("%s sends its method, path and body", async (name, op) => {
      fetchMock.mockResolvedValue(replyTo(op));
      // The row's call must be the operation it is filed under, or the table
      // could cover one operation twice and another not at all.
      const called = vi.spyOn(api.apiClient, name as keyof Client);

      await op.call(api.apiClient);

      expect(called).toHaveBeenCalledTimes(1);
      expect(fetchMock).toHaveBeenCalledTimes(1);
      const request = await sent(fetchMock);
      expect({ method: request.method, url: request.url, body: request.body }).toEqual({
        method: op.method,
        url: op.url,
        body: op.body,
      });
      // The session rides in cookies; no bearer token is ever sent.
      expect(request.headers.has("Authorization")).toBe(false);
      if (op.body !== undefined) expect(request.headers.get("Content-Type")).toBe("application/json");
    });

    // The recorded bodies are the table's claim about the wire; checked
    // against the spec, a row cannot drift from the server unnoticed.
    it.each(rows)("%s sends and receives bodies that match the API spec", (_, op) => {
      const errors: string[] = [];
      if (op.body !== undefined) errors.push(...openApiSchemas.requestErrors(op.method, op.url, op.body));
      if (op.response !== undefined) errors.push(...openApiSchemas.successErrors(op.method, op.url, op.response));
      expect(errors).toEqual([]);
    });

    it.each(rows)("%s resolves to what the caller should see", async (_, op) => {
      fetchMock.mockResolvedValue(replyTo(op));

      const result = await op.call(api.apiClient);

      expect(result).toEqual("result" in op ? op.result : op.response);
    });

    // The CSRF token goes on every write the page makes once it holds one,
    // and never on a read.
    it.each(rows)("%s carries the CSRF token if and only if it writes", async (_, op) => {
      fetchMock.mockResolvedValueOnce(jsonResponse(200, { ...profile, csrf_token: "the-token" }));
      await api.apiClient.me();
      fetchMock.mockResolvedValue(replyTo(op));

      await op.call(api.apiClient);

      const request = await sent(fetchMock, 1);
      const carries = op.csrf ?? op.method !== "GET";
      expect(request.headers.get(api.CSRF_HEADER)).toBe(carries ? "the-token" : null);
    });

    it.each(rows)("%s throws an ApiError carrying the server's detail and code", async (_, op) => {
      fetchMock.mockResolvedValue(jsonResponse(404, { detail: "no such thing", code: "not_found", status: 404 }));

      const err = await op.call(api.apiClient).catch((e: unknown) => e);

      expect(err).toBeInstanceOf(api.ApiError);
      expect(err).toMatchObject({ status: 404, message: "no such thing", code: "not_found" });
    });

    it.each(rows)("%s falls back to the status line when the error body is not JSON", async (_, op) => {
      fetchMock.mockResolvedValue(new Response("upstream down", { status: 502, statusText: "Bad Gateway" }));

      const err = await op.call(api.apiClient).catch((e: unknown) => e);

      expect(err).toBeInstanceOf(api.ApiError);
      expect(err).toMatchObject({ status: 502, message: "Bad Gateway" });
    });

    it.each(rows)("%s throws on a failure with an empty body", async (_, op) => {
      fetchMock.mockResolvedValue(
        new Response(null, { status: 409, statusText: "Conflict", headers: { "Content-Length": "0" } }),
      );

      const err = await op.call(api.apiClient).catch((e: unknown) => e);

      expect(err).toBeInstanceOf(api.ApiError);
      expect(err).toMatchObject({ status: 409, message: "Conflict" });
    });

    // What a 401 means is the route's session mode: a signed-in route
    // refreshes once and, failing that, announces the sign-out; the probe
    // refreshes but a final 401 just means "nobody"; a public route's 401 is
    // the answer itself; and a credential route's wrong password is too.
    const expectedOn401: Record<SessionMode, { refreshes: boolean; signsOut: boolean }> = {
      session: { refreshes: true, signsOut: true },
      probe: { refreshes: true, signsOut: false },
      public: { refreshes: false, signsOut: false },
      credential: { refreshes: false, signsOut: false },
    };

    it.each(rows)("%s handles a 401 as its session mode says", async (_, op) => {
      const signedOut = vi.fn();
      api.onSignedOut(signedOut);
      fetchMock.mockResolvedValue(jsonResponse(401, { detail: "no", code: "invalid_credentials" }));

      await expect(op.call(api.apiClient)).rejects.toMatchObject({ status: 401 });

      const expected = expectedOn401[op.mode ?? "session"];
      expect(urlsOf(fetchMock).includes("/api/auth/refresh")).toBe(expected.refreshes);
      expect(signedOut).toHaveBeenCalledTimes(expected.signsOut ? 1 : 0);
    });
  });
}
