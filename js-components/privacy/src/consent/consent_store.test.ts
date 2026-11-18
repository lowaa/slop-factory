import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";
import { createFetchConsentApi } from "./consent_api";
import { ConsentStore } from "./consent_store";
import {
  BASE,
  ServerStub,
  decidedState,
  freshState,
  newStore,
  wirePurpose,
  wireState,
} from "./test/fixtures";

const stub = new ServerStub();
const server = setupServer(...stub.handlers);

beforeAll(() => server.listen({ onUnhandledRequest: "error" }));
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
    { status: 403, code: "credential_rejected" },
    { status: 403, headers: { "X-Auth-Credential": "rejected" } },
  );

async function loaded(options?: Parameters<typeof newStore>[0]) {
  const store = newStore(options);
  await store.load();
  return store;
}

describe("loading", () => {
  it("grants nothing until the server has answered", async () => {
    stub.state = decidedState({ analytics: true });
    const store = newStore();
    expect(store.getSnapshot().status).toBe("loading");
    expect(store.isGranted("analytics")).toBe(false);
    await store.load();
    expect(store.getSnapshot().status).toBe("ready");
    expect(store.isGranted("analytics")).toBe(true);
  });

  it("makes one request for loads started together", async () => {
    const store = newStore();
    await Promise.all([store.load(), store.load()]);
    expect(stub.gets).toBe(1);
  });

  it("offers nothing, and grants nothing, when the server can't be reached", async () => {
    server.use(http.get(BASE, () => HttpResponse.error()));
    const store = await loaded();
    expect(store.getSnapshot()).toMatchObject({ status: "error", purposes: [] });
    expect(store.isGranted("analytics")).toBe(false);
  });

  it("shows only the purposes the server reports", async () => {
    stub.state = wireState([wirePurpose("necessary"), wirePurpose("analytics")]);
    const store = await loaded();
    expect(store.getSnapshot().purposes.map((p) => p.id)).toEqual(["necessary", "analytics"]);
  });
});

describe("Reject all", () => {
  it("denies every non-necessary cookie purpose in one write, and nothing else", async () => {
    stub.state = wireState([
      wirePurpose("necessary"),
      wirePurpose("analytics"),
      wirePurpose("interaction_recording"),
      wirePurpose("marketing"),
      wirePurpose("health_data_processing", { kind: "processing" }),
    ]);
    const store = await loaded();
    stub.state = decidedState({ analytics: false, interaction_recording: false, marketing: false });
    await expect(store.decideAll("banner", false)).resolves.toBe(true);
    expect(stub.puts).toHaveLength(1);
    expect(stub.puts[0]?.source).toBe("banner");
    expect(stub.puts[0]?.decisions).toEqual([
      expect.objectContaining({ purpose: "analytics", granted: false }),
      expect.objectContaining({ purpose: "interaction_recording", granted: false }),
      expect.objectContaining({ purpose: "marketing", granted: false }),
    ]);
  });

  it("leaves a purpose already denied alone", async () => {
    stub.state = wireState([
      wirePurpose("necessary"),
      wirePurpose("analytics", { needs_decision: false, granted: false }),
      wirePurpose("marketing"),
    ]);
    const store = await loaded();
    await store.decideAll("banner", false);
    expect(stub.puts[0]?.decisions.map((d) => d.purpose)).toEqual(["marketing"]);
  });
});

describe("a decision", () => {
  it("shows the server's answer, not the request: nothing changes until the server has taken it", async () => {
    const store = await loaded();
    let release: () => void = () => undefined;
    const gate = new Promise<void>((r) => (release = r));
    server.use(
      http.put(BASE, async () => {
        await gate;
        return HttpResponse.json(decidedState({ analytics: true, marketing: false }));
      }),
    );
    const pending = store.decide("settings", [{ purpose: "analytics", granted: true }]);
    await vi.waitFor(() => expect(store.getSnapshot().saving).toBe(true));
    expect(store.isGranted("analytics")).toBe(false);
    release();
    await pending;
    expect(store.isGranted("analytics")).toBe(true);
    expect(store.getSnapshot().saving).toBe(false);
  });

  it("is not claimed when the server refuses it", async () => {
    const store = await loaded();
    stub.nextPut = () => HttpResponse.json({ code: "invalid_purpose" }, { status: 400 });
    await expect(store.decide("banner", [{ purpose: "analytics", granted: true }])).resolves.toBe(false);
    expect(store.isGranted("analytics")).toBe(false);
    expect(store.getSnapshot().error?.code).toBe("invalid_purpose");
  });

  it("ignores a purpose the server doesn't offer", async () => {
    const store = await loaded();
    await expect(store.decide("banner", [{ purpose: "interaction_recording", granted: true }])).resolves.toBe(false);
    expect(stub.puts).toHaveLength(0);
  });

  it("reloads the state when the server says it changed underneath", async () => {
    const store = await loaded();
    stub.nextPut = () => {
      stub.state = decidedState({ analytics: false, marketing: false });
      return HttpResponse.json({ code: "consent_changed" }, { status: 409 });
    };
    await expect(store.decide("banner", [{ purpose: "analytics", granted: true }])).resolves.toBe(false);
    expect(store.isGranted("analytics")).toBe(false);
    expect(store.getSnapshot().purposes.find((p) => p.id === "analytics")?.needsDecision).toBe(false);
    expect(store.getSnapshot().error?.code).toBe("consent_changed");
  });

  it("is not undone by a read that was already in flight", async () => {
    const store = await loaded();
    let answerRead: () => void = () => undefined;
    const readHeld = new Promise<void>((r) => (answerRead = r));
    const stale = freshState();
    server.use(
      http.get(BASE, async () => {
        await readHeld;
        return HttpResponse.json(stale);
      }),
    );
    const read = store.load();
    stub.nextPut = () => HttpResponse.json(decidedState({ analytics: true, marketing: false }));
    await store.decide("settings", [{ purpose: "analytics", granted: true }]);
    answerRead();
    await read;
    expect(store.isGranted("analytics")).toBe(true);
  });

  it("sends decisions one at a time, each on the state the one before left", async () => {
    const order: string[] = [];
    const locks = {
      request: async <T>(_name: string, cb: () => Promise<T>): Promise<T> => {
        order.push("lock");
        return cb();
      },
    };
    const store = newStore({ locks: locks as unknown as Pick<LockManager, "request"> });
    await store.load();
    stub.nextPut = () => {
      order.push("put");
      // Each answer moves the purpose's event on, as the server's would.
      const n = order.filter((o) => o === "put").length;
      return HttpResponse.json(
        wireState([
          wirePurpose("necessary"),
          wirePurpose("analytics", { granted: n === 1, needs_decision: false, based_on: n }),
          wirePurpose("marketing", { granted: false, needs_decision: false, based_on: n }),
        ]),
      );
    };
    const a = store.decide("settings", [{ purpose: "analytics", granted: true }]);
    const b = store.decide("settings", [{ purpose: "analytics", granted: false }]);
    await Promise.all([a, b]);
    expect(order).toEqual(["lock", "put", "lock", "put"]);
    expect(stub.puts.map((p) => p.decisions[0]?.based_on)).toEqual([0, 1]);
  });

  it("tells other tabs, and reloads when another tab tells it", async () => {
    let channel: { postMessage: ReturnType<typeof vi.fn>; onmessage: (() => void) | null; close: () => void } | null =
      null;
    const store = newStore({
      createChannel: () => {
        channel = { postMessage: vi.fn(), onmessage: null, close: vi.fn() };
        return channel as unknown as BroadcastChannel;
      },
    });
    const stop = store.start();
    await store.load();
    stub.nextPut = () => HttpResponse.json(decidedState({ analytics: true, marketing: false }));
    await store.decide("banner", [{ purpose: "analytics", granted: true }]);
    expect(channel!.postMessage).toHaveBeenCalledTimes(1);

    stub.state = decidedState({ analytics: false, marketing: false });
    channel!.onmessage?.();
    await vi.waitFor(() => expect(store.isGranted("analytics")).toBe(false));
    stop();
  });
});

describe("a read in flight when the state moves on", () => {
  // The read started before the change, so its answer predates it.
  function slowFirstRead() {
    let n = 0;
    let answer: () => void = () => undefined;
    const held = new Promise<void>((r) => (answer = r));
    const before = JSON.parse(JSON.stringify(stub.state));
    server.use(
      http.get(BASE, async () => {
        stub.gets++;
        if (++n === 1) {
          await held;
          return HttpResponse.json(before);
        }
        return HttpResponse.json(stub.state);
      }),
    );
    return answer;
  }

  it("reads again when another tab says it decided", async () => {
    stub.state = decidedState({ analytics: true, marketing: false });
    const store = await loaded();
    const answer = slowFirstRead();
    const joined = store.load();
    stub.state = decidedState({ analytics: false, marketing: false });
    const refreshed = store.refresh();
    answer();
    await Promise.all([joined, refreshed]);
    expect(store.isGranted("analytics")).toBe(false);
  });
});

describe("Global Privacy Control", () => {
  const gpcState = () =>
    wireState(
      [
        wirePurpose("necessary"),
        wirePurpose("analytics"),
        wirePurpose("marketing", { gpc_locked: true, granted: false }),
      ],
      { gpc: true },
    );

  it("keeps the sale/share purpose off and says the signal was honoured, leaving analytics alone", async () => {
    stub.state = gpcState();
    const store = await loaded({ gpcSignal: () => true });
    const snap = store.getSnapshot();
    expect(snap.gpc).toEqual({ signalled: true, honoured: true });
    expect(snap.purposes.find((p) => p.id === "marketing")).toMatchObject({ gpcLocked: true, granted: false });
    expect(snap.purposes.find((p) => p.id === "analytics")?.gpcLocked).toBe(false);
  });

  it("shows a locked purpose denied even if it was reported granted", async () => {
    stub.state = wireState([wirePurpose("marketing", { gpc_locked: true, granted: true, needs_decision: false })], {
      gpc: true,
    });
    const store = await loaded();
    expect(store.isGranted("marketing")).toBe(false);
  });

  it("denies the locked purpose on Accept all, since granting it would be refused", async () => {
    stub.state = gpcState();
    const store = await loaded({ gpcSignal: () => true });
    await store.decideAll("banner", true);
    expect(stub.puts[0]?.decisions).toEqual([
      expect.objectContaining({ purpose: "analytics", granted: true }),
      expect.objectContaining({ purpose: "marketing", granted: false }),
    ]);
  });

  it("reports a browser that signals GPC the server did not see as signalled but not honoured", async () => {
    const store = await loaded({ gpcSignal: () => true });
    expect(store.getSnapshot().gpc).toEqual({ signalled: true, honoured: false });
  });
});

describe("a decision refused with credential_rejected", () => {
  it("is refreshed and retried once, with no error shown and no decision claimed along the way", async () => {
    const refresh = vi.fn(async () => true);
    const api = createFetchConsentApi({ basePath: BASE, refreshSession: refresh });
    const store = new ConsentStore({ api, locks: null, createChannel: null });
    await store.load();
    const seen: (string | undefined)[] = [];
    store.subscribe(() => seen.push(store.getSnapshot().error?.code));
    let n = 0;
    stub.nextPut = () =>
      ++n === 1 ? REFUSED() : HttpResponse.json(decidedState({ analytics: true, marketing: false }));
    await expect(store.decide("banner", [{ purpose: "analytics", granted: true }])).resolves.toBe(true);
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(stub.puts).toHaveLength(2);
    expect(seen.filter(Boolean)).toEqual([]);
    expect(store.isGranted("analytics")).toBe(true);
  });

  it("reports it, and records nothing, when the retry is refused too", async () => {
    const refresh = vi.fn(async () => true);
    const store = new ConsentStore({
      api: createFetchConsentApi({ basePath: BASE, refreshSession: refresh }),
      locks: null,
      createChannel: null,
    });
    await store.load();
    stub.nextPut = REFUSED;
    await expect(store.decide("banner", [{ purpose: "analytics", granted: true }])).resolves.toBe(false);
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(stub.puts).toHaveLength(2);
    expect(store.isGranted("analytics")).toBe(false);
    expect(store.getSnapshot().error?.code).toBe("credential_rejected");
  });
});

describe("the check handed to the logger", () => {
  it("follows the live state, so a withdrawal applies to the very next record", async () => {
    stub.state = decidedState({ analytics: true });
    const store = await loaded();
    const hasAnalytics = store.check("analytics");
    expect(hasAnalytics()).toBe(true);
    stub.nextPut = () => HttpResponse.json(decidedState({ analytics: false }));
    await store.decide("settings", [{ purpose: "analytics", granted: false }]);
    expect(hasAnalytics()).toBe(false);
  });

  it("gates a script on the store: nothing loads until the purpose is granted, and withdrawal tears it down", async () => {
    const store = await loaded();
    const teardown = vi.fn();
    const loader = vi.fn(async () => teardown);
    store.loadWhenConsented("analytics", loader);
    await Promise.resolve();
    expect(loader).not.toHaveBeenCalled();
    stub.nextPut = () => HttpResponse.json(decidedState({ analytics: true, marketing: false }));
    await store.decide("banner", [{ purpose: "analytics", granted: true }]);
    await vi.waitFor(() => expect(loader).toHaveBeenCalledTimes(1));
    stub.nextPut = () => HttpResponse.json(decidedState({ analytics: false, marketing: false }));
    await store.decide("settings", [{ purpose: "analytics", granted: false }]);
    expect(teardown).toHaveBeenCalledTimes(1);
  });
});
