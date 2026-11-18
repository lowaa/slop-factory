import { describe, expect, it, vi } from "vitest";
import { BrowserStore } from "./browser_store.ts";
import { FakeStorage, FakeWindow } from "./test_storage.ts";

function setup() {
  const storage = new FakeStorage();
  const events = new FakeWindow();
  const store = new BrowserStore({ storage: () => storage, events });
  return { storage, events, store };
}

describe("BrowserStore", () => {
  it("reads and writes through the storage", () => {
    const { storage, store } = setup();
    expect(store.get("k")).toBeNull();
    expect(store.set("k", "v")).toBe(true);
    expect(storage.data.get("k")).toBe("v");
    expect(store.get("k")).toBe("v");
    store.remove("k");
    expect(storage.data.has("k")).toBe(false);
  });

  it("does not throw when the storage lookup itself throws", () => {
    const events = new FakeWindow();
    const store = new BrowserStore({
      storage: () => {
        throw new DOMException("blocked", "SecurityError");
      },
      events,
    });
    expect(store.get("k")).toBeNull();
    expect(store.set("k", "v")).toBe(false);
    expect(store.get("k")).toBe("v");
    expect(() => store.remove("k")).not.toThrow();
    expect(store.get("k")).toBeNull();
  });

  it("does not throw when get throws", () => {
    const { storage, store } = setup();
    storage.failGet = true;
    expect(store.get("k")).toBeNull();
  });

  it("keeps a value in memory when set throws, and says it was not kept", () => {
    const { storage, store } = setup();
    storage.failSet = true;
    expect(store.set("k", "v")).toBe(false);
    expect(store.get("k")).toBe("v");
    expect(storage.data.size).toBe(0);
  });

  it("goes back to the storage once a write succeeds", () => {
    const { storage, store } = setup();
    storage.failSet = true;
    store.set("k", "memory");
    storage.failSet = false;
    expect(store.set("k", "stored")).toBe(true);
    storage.data.set("k", "changed elsewhere");
    expect(store.get("k")).toBe("changed elsewhere");
  });

  it("hides a value it could not remove", () => {
    const { storage, store } = setup();
    storage.data.set("k", "old");
    storage.failRemove = true;
    expect(() => store.remove("k")).not.toThrow();
    expect(store.get("k")).toBeNull();
  });

  it("notifies this tab's subscribers of its writes, including kept-in-memory ones", () => {
    const { storage, store } = setup();
    const onChange = vi.fn();
    const other = vi.fn();
    store.subscribe("k", onChange);
    store.subscribe("other", other);
    store.set("k", "1");
    storage.failSet = true;
    store.set("k", "2");
    store.remove("k");
    expect(onChange).toHaveBeenCalledTimes(3);
    expect(other).not.toHaveBeenCalled();
  });

  it("notifies of another tab's write and shows it over a value held in memory", () => {
    const { storage, events, store } = setup();
    storage.failSet = true;
    store.set("k", "mine");
    storage.failSet = false;
    const onChange = vi.fn();
    store.subscribe("k", onChange);

    storage.data.set("k", "theirs");
    events.storageEvent("k", storage);

    expect(onChange).toHaveBeenCalledTimes(1);
    expect(store.get("k")).toBe("theirs");
  });

  it("notifies everything on another tab's clear, and drops values held in memory", () => {
    const { storage, events, store } = setup();
    storage.failSet = true;
    store.set("a", "mine");
    storage.failSet = false;
    const a = vi.fn();
    const b = vi.fn();
    store.subscribe("a", a);
    store.subscribe("b", b);
    events.storageEvent(null, storage);
    expect(a).toHaveBeenCalledTimes(1);
    expect(b).toHaveBeenCalledTimes(1);
    expect(store.get("a")).toBeNull();
  });

  it("still takes another tab's write that arrives while access is revoked", () => {
    const storage = new FakeStorage();
    const events = new FakeWindow();
    let revoked = false;
    const store = new BrowserStore({
      storage: () => {
        if (revoked) throw new DOMException("blocked", "SecurityError");
        return storage;
      },
      events,
    });
    storage.failSet = true;
    store.set("k", "mine");
    storage.failSet = false;
    store.subscribe("k", () => {});

    revoked = true;
    events.storageEvent("k", storage);
    revoked = false;
    storage.data.set("k", "theirs");

    expect(store.get("k")).toBe("theirs");
  });

  it("ignores the other storage area's events", () => {
    const { events, store } = setup();
    const onChange = vi.fn();
    store.subscribe("k", onChange);
    events.storageEvent("k", new FakeStorage());
    expect(onChange).not.toHaveBeenCalled();
  });

  it("listens to the window only while something is subscribed", () => {
    const { events, store } = setup();
    expect(events.listenerCount).toBe(0);
    const first = store.subscribe("a", () => {});
    const second = store.subscribe("b", () => {});
    expect(events.listenerCount).toBe(1);
    first();
    expect(events.listenerCount).toBe(1);
    second();
    expect(events.listenerCount).toBe(0);
  });
});
