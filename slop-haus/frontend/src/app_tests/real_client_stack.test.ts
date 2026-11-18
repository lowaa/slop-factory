import { beforeEach, describe, expect, it, vi } from "vitest";
import { http } from "msw/http";
import { makeProfile, makeTicket } from "@/test/fixtures";
import { problem, serve, server, useMockApi } from "@/test/msw_server";

// These run apiClient for real, down to fetch, so the parts method-level
// mocks skip are exercised: the tenant header, the CSRF token, one refresh
// and retry on a 401, and the error envelope's mapping to ApiError.
describe("the real client stack against a mock API", () => {
  useMockApi();

  let api: typeof import("@/api");

  // The client's session is module state, so each test gets a fresh copy.
  beforeEach(async () => {
    vi.resetModules();
    api = await import("@/api");
  });

  const me = () => serve("get", "/api/auth/me", { ...makeProfile({ tenant_id: "tenant-a" }), csrf_token: "csrf-1" });

  it("scopes every request to the tenant the page signed in to, and sends the CSRF token on writes", async () => {
    const seen: Request[] = [];
    server.use(
      me(),
      serve("get", "/api/tickets/{id}", makeTicket({ id: 1 }), (r) => seen.push(r)),
      serve("patch", "/api/tickets/{id}", makeTicket({ id: 1, priority: "high" }), (r) => seen.push(r)),
    );

    await api.apiClient.me();
    await api.apiClient.getTicket(1);
    await api.apiClient.updateTicket(1, { priority: "high" });

    expect(seen.map((r) => r.headers.get("X-Tenant-ID"))).toEqual(["tenant-a", "tenant-a"]);
    expect(seen.map((r) => r.headers.get(api.CSRF_HEADER))).toEqual([null, "csrf-1"]);
  });

  it("refreshes once on a 401 and retries the request with the new session", async () => {
    let tickets = 0;
    let refreshes = 0;
    server.use(
      me(),
      http.post("/api/auth/refresh", () => {
        refreshes++;
        return new Response(null, { status: 204 });
      }),
      http.get("/api/tickets/:id", () => {
        tickets++;
        return tickets === 1 ? problem(401, "token expired", "unauthorized") : Response.json(makeTicket({ id: 1, title: "Back again" }));
      }),
    );
    await api.apiClient.me();

    const ticket = await api.apiClient.getTicket(1);

    expect({ title: ticket.title, tickets, refreshes }).toEqual({ title: "Back again", tickets: 2, refreshes: 1 });
  });

  it("maps the server's error envelope to an ApiError carrying its status, detail and code", async () => {
    server.use(me(), http.get("/api/tickets/:id", () => problem(404, "no such ticket", "not_found")));
    await api.apiClient.me();

    const err = await api.apiClient.getTicket(9).catch((e: unknown) => e);

    expect(err).toBeInstanceOf(api.ApiError);
    expect(err).toMatchObject({ status: 404, message: "no such ticket", code: "not_found" });
  });

  it("refuses a handler body that breaks the spec, naming the path", () => {
    expect(() => serve("get", "/api/tickets/{id}", { ...makeTicket(), created_at: 5 })).toThrow(/response 200\/created_at: must be string/);
  });
});
