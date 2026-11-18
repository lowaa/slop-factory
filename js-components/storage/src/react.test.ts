import { describe, expect, it } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { boolCodec } from "./codecs.ts";
import { MemoryStore } from "./memory_store.ts";
import { usePersisted } from "./react.ts";
import type { StoredValue } from "./stored_value.ts";

const open: StoredValue<boolean> = { key: "k", codec: boolCodec("open", "closed"), fallback: true };

describe("usePersisted", () => {
  it("shows the fallback and writes nothing until set", () => {
    const store = new MemoryStore();
    const { result } = renderHook(() => usePersisted(store, open));
    expect(result.current[0]).toBe(true);
    expect(store.get("k")).toBeNull();

    act(() => result.current[1](false));

    expect(result.current[0]).toBe(false);
    expect(store.get("k")).toBe("closed");
  });

  it("reads what is already stored", () => {
    const store = new MemoryStore();
    store.set("k", "closed");
    expect(renderHook(() => usePersisted(store, open)).result.current[0]).toBe(false);
  });

  it("keeps two components in step", () => {
    const store = new MemoryStore();
    const a = renderHook(() => usePersisted(store, open));
    const b = renderHook(() => usePersisted(store, open));
    act(() => a.result.current[1](false));
    expect(b.result.current[0]).toBe(false);
  });

  it("follows a change made outside the hook, as another tab's would be", () => {
    const store = new MemoryStore();
    const { result } = renderHook(() => usePersisted(store, open));
    act(() => void store.set("k", "closed"));
    expect(result.current[0]).toBe(false);
  });

  it("follows a fallback that changes while nothing is chosen", () => {
    const store = new MemoryStore();
    let wide = true;
    const { result, rerender } = renderHook(() =>
      usePersisted(store, { ...open, fallback: () => wide }),
    );
    expect(result.current[0]).toBe(true);
    wide = false;
    rerender();
    expect(result.current[0]).toBe(false);
  });
});
