import { describe, expect, it } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { MemoryStore } from "@slop-factory/storage";
import { BrowserStorage } from "@/lib/browser_storage";
import { stubNarrowLayout } from "@/test/layout";
import { usePanelOpen } from "./use_panel_open";

const KEY = "remote_control.sessions_panel";

function setup() {
  const local = new MemoryStore();
  return { local, storage: new BrowserStorage({ local_storage: local, session_storage: new MemoryStore() }) };
}

describe("usePanelOpen on an injected store", () => {
  it("writes nothing until the person chooses, then the existing texts", () => {
    const { local, storage } = setup();
    const { result } = renderHook(() => usePanelOpen(storage));
    expect(result.current[0]).toBe(true);
    expect(local.get(KEY)).toBeNull();

    act(() => result.current[1](false));

    expect(local.get(KEY)).toBe("closed");
    expect(result.current[0]).toBe(false);
  });

  it("defaults closed on a phone, but a stored choice wins", () => {
    stubNarrowLayout();
    const { local, storage } = setup();
    expect(renderHook(() => usePanelOpen(storage)).result.current[0]).toBe(false);
    local.set(KEY, "open");
    expect(renderHook(() => usePanelOpen(storage)).result.current[0]).toBe(true);
  });

  it("keeps two panels in step", () => {
    const { storage } = setup();
    const a = renderHook(() => usePanelOpen(storage));
    const b = renderHook(() => usePanelOpen(storage));
    act(() => a.result.current[1](false));
    expect(b.result.current[0]).toBe(false);
  });
});
