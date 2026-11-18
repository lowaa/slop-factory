import { describe, expect, it, vi } from "vitest";
import { MemoryStore } from "./memory_store.ts";

describe("MemoryStore", () => {
  it("stores, removes and notifies per key", () => {
    const store = new MemoryStore();
    const onChange = vi.fn();
    const unsubscribe = store.subscribe("k", onChange);
    expect(store.get("k")).toBeNull();
    expect(store.set("k", "v")).toBe(true);
    expect(store.get("k")).toBe("v");
    store.set("other", "x");
    store.remove("k");
    expect(store.get("k")).toBeNull();
    expect(onChange).toHaveBeenCalledTimes(2);
    unsubscribe();
    store.set("k", "again");
    expect(onChange).toHaveBeenCalledTimes(2);
  });
});
