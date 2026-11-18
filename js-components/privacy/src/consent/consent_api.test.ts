import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw/http";
import { setupServer } from "msw/node";
import { createFetchConsentApi, parseConsentState } from "./consent_api";
import { ConsentApiError } from "./types";
import { BASE, ServerStub, decidedState, freshState, wirePurpose, wireState, type PutBody } from "./test/fixtures";

const stub = new ServerStub();
const server = setupServer(...stub.handlers);

beforeAll(() => server.listen({ onUnhandledFrame: "error" }));
afterEach(() => {
  server.resetHandlers();
  stub.state = freshState();
  stub.nextPut = null;
  stub.gets = 0;
  stub.puts = [];
});
afterAll(() => server.close());

const REFUSED = () =>
  HttpResponse.json(
    { status: 403, title: "Forbidden", code: "credential_rejected" },
    { status: 403, headers: { "X-Auth-Credential": "rejected" } },
  );

describe("reading the state", () => {
  it("maps the server's answer, and offers only the purposes it reports", async () => {
    stub.state = wireState([wirePurpose("necessary"), wirePurpose("analytics", { based_on: 7 })], { gpc: true });
    const state = await createFetchConsentApi({ basePath: BASE }).get();
    expect(state.purposes.map((p) => p.id)).toEqual(["necessary", "analytics"]);
    expect(state.purposes[1]).toMatchObject({ needsDecision: true, basedOn: 7, documentVersion: "2026-10-01" });
    expect(state.gpcHonoured).toBe(true);
  });

  it("drops a purpose it can't read rather than guessing at it", () => {
    const state = parseConsentState({ purposes: [{ id: "x", kind: "mystery" }, wirePurpose("analytics")] });
    expect(state.purposes.map((p) => p.id)).toEqual(["analytics"]);
  });

  it("refuses a body that isn't a consent state", () => {
    expect(() => parseConsentState({ nope: true })).toThrow(ConsentApiError);
  });
});

describe("writing a decision", () => {
  it("cites the document version and based_on the person was shown", async () => {
    stub.state = freshState();
    const api = createFetchConsentApi({ basePath: BASE });
    const current = await api.get();
    stub.state = decidedState({ analytics: false });
    await api.put("banner", [{ purpose: "analytics", granted: false }], current);
    expect(stub.puts).toEqual<PutBody[]>([
      {
        source: "banner",
        decisions: [{ purpose: "analytics", granted: false, document_version: "2026-10-01", based_on: 0 }],
      },
    ]);
  });

  it("raises the server's code for a refused decision", async () => {
    const api = createFetchConsentApi({ basePath: BASE });
    const current = await api.get();
    stub.nextPut = () => HttpResponse.json({ code: "gpc_active" }, { status: 409 });
    await expect(api.put("banner", [{ purpose: "marketing", granted: true }], current)).rejects.toMatchObject({
      code: "gpc_active",
      status: 409,
    });
  });
});

describe("a write refused with credential_rejected", () => {
  it("refreshes the session and sends the decision again exactly once", async () => {
    const refresh = vi.fn(async () => true);
    const api = createFetchConsentApi({ basePath: BASE, refreshSession: refresh });
    const current = await api.get();
    let n = 0;
    stub.nextPut = () => (++n === 1 ? REFUSED() : HttpResponse.json(decidedState({ analytics: true })));
    const after = await api.put("banner", [{ purpose: "analytics", granted: true }], current);
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(stub.puts).toHaveLength(2);
    expect(stub.puts[1]).toEqual(stub.puts[0]);
    expect(after.purposes.find((p) => p.id === "analytics")?.granted).toBe(true);
  });

  it("gives up after the one retry, and says so", async () => {
    const refresh = vi.fn(async () => true);
    const api = createFetchConsentApi({ basePath: BASE, refreshSession: refresh });
    const current = await api.get();
    stub.nextPut = REFUSED;
    await expect(api.put("banner", [{ purpose: "analytics", granted: true }], current)).rejects.toMatchObject({
      code: "credential_rejected",
    });
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(stub.puts).toHaveLength(2);
  });

  it("does not resend when the refresh fails", async () => {
    const api = createFetchConsentApi({ basePath: BASE, refreshSession: async () => false });
    const current = await api.get();
    stub.nextPut = REFUSED;
    await expect(api.put("banner", [{ purpose: "analytics", granted: true }], current)).rejects.toMatchObject({
      code: "credential_rejected",
    });
    expect(stub.puts).toHaveLength(1);
  });

  it("is an error, not a recorded decision, when nothing can refresh", async () => {
    const api = createFetchConsentApi({ basePath: BASE });
    const current = await api.get();
    stub.nextPut = REFUSED;
    await expect(api.put("banner", [{ purpose: "analytics", granted: true }], current)).rejects.toMatchObject({
      code: "credential_rejected",
    });
    expect(stub.puts).toHaveLength(1);
  });

  it("treats a bare rejected-credential header on a 403 the same", async () => {
    const api = createFetchConsentApi({ basePath: BASE, refreshSession: async () => false });
    const current = await api.get();
    stub.nextPut = () => new Response(null, { status: 403, headers: { "X-Auth-Credential": "rejected" } });
    await expect(api.put("banner", [{ purpose: "analytics", granted: true }], current)).rejects.toMatchObject({
      code: "credential_rejected",
    });
  });
});

describe("a read answered as anonymous on a rejected credential", () => {
  it("refreshes and reads again, once", async () => {
    const refresh = vi.fn(async () => true);
    let n = 0;
    server.use(
      http.get(BASE, () => {
        n++;
        return HttpResponse.json(n === 1 ? freshState() : decidedState({ analytics: true }), {
          headers: n === 1 ? { "X-Auth-Credential": "rejected" } : {},
        });
      }),
    );
    const state = await createFetchConsentApi({ basePath: BASE, refreshSession: refresh }).get();
    expect(n).toBe(2);
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(state.signedIn).toBe(false);
    expect(state.purposes.find((p) => p.id === "analytics")?.granted).toBe(true);
  });
});

describe("a network failure", () => {
  it("is a network error", async () => {
    server.use(http.get(BASE, () => HttpResponse.error()));
    await expect(createFetchConsentApi({ basePath: BASE }).get()).rejects.toMatchObject({ code: "network" });
  });
});
