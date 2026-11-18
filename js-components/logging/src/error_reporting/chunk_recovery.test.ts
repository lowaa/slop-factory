import { describe, expect, it, vi } from "vitest";
import { ChunkRecovery, isChunkLoadError } from "./chunk_recovery";
import type { BuildTag } from "./build_tag";

function setup(opts: { stored?: Record<string, string>; guardMs?: number } = {}) {
  const store = new Map(Object.entries(opts.stored ?? {}));
  let now = 1_000_000;
  const reload = vi.fn();
  const recovery = new ChunkRecovery({
    buildTag: { peek: () => "b1" } as unknown as BuildTag,
    storage: { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => void store.set(k, v) },
    reload,
    now: () => now,
    path: () => "/approvals",
    guardMs: opts.guardMs ?? 60_000,
  });
  return { recovery, reload, store, advance: (ms: number) => (now += ms) };
}

describe("isChunkLoadError", () => {
  it.each([
    "Failed to fetch dynamically imported module: https://x/assets/a.js",
    "Importing a module script failed.",
    "error loading dynamically imported module: https://x/assets/a.js",
  ])("recognises %s", (message) => {
    expect(isChunkLoadError(new TypeError(message))).toBe(true);
  });

  it("does not take other errors for one", () => {
    expect(isChunkLoadError(new TypeError("Failed to fetch"))).toBe(false);
    expect(isChunkLoadError("Failed to fetch dynamically imported module")).toBe(false);
  });
});

describe("ChunkRecovery", () => {
  it("reloads once, then holds off", () => {
    const { recovery, reload } = setup();
    expect(recovery.canReload()).toBe(true);
    expect(recovery.attempt()).toBe("reloading");
    expect(reload).toHaveBeenCalledOnce();
    expect(recovery.attempt()).toBe("reloading");
    expect(reload).toHaveBeenCalledOnce();
  });

  it("does not reload again after the page came back with the guard set (no loop)", () => {
    const first = setup();
    first.recovery.attempt();
    const { recovery, reload } = setup({ stored: Object.fromEntries(first.store) });
    expect(recovery.canReload()).toBe(false);
    expect(recovery.attempt()).toBe("blocked");
    expect(reload).not.toHaveBeenCalled();
  });

  it("allows a reload again once the guard has expired", () => {
    const first = setup();
    first.recovery.attempt();
    const second = setup({ stored: Object.fromEntries(first.store), guardMs: 60_000 });
    second.advance(61_000);
    expect(second.recovery.attempt()).toBe("reloading");
    expect(second.reload).toHaveBeenCalledOnce();
  });

  it("does not reload when the guard can't be stored", () => {
    const reload = vi.fn();
    const recovery = new ChunkRecovery({
      buildTag: { peek: () => "b1" } as unknown as BuildTag,
      storage: { getItem: () => null, setItem: () => { throw new Error("full"); } },
      reload,
    });
    expect(recovery.attempt()).toBe("blocked");
    expect(reload).not.toHaveBeenCalled();
  });

  it("treats a guard set before the tag was read as the same incident", () => {
    const first = setup();
    first.recovery.attempt();
    const store = new Map<string, string>();
    for (const [k, v] of first.store) store.set(k.replace("|b1", "|unknown"), v);
    const { recovery, reload } = setup({ stored: Object.fromEntries(store) });
    expect(recovery.attempt()).toBe("blocked");
    expect(reload).not.toHaveBeenCalled();
  });
});
