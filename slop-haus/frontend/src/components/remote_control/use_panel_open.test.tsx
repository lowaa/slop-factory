import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { stubNarrowLayout } from "@/test/layout";
import { stubLocalStorage } from "@/test/storage";
import { usePanelOpen } from "./use_panel_open";

const KEY = "remote_control.sessions_panel";

let storage: Storage;
beforeEach(() => {
  storage = stubLocalStorage();
});

describe("usePanelOpen", () => {
  it("is open by default on a wide screen", () => {
    const { result } = renderHook(() => usePanelOpen());
    expect(result.current[0]).toBe(true);
  });

  it("is closed by default on a phone", () => {
    stubNarrowLayout();
    const { result } = renderHook(() => usePanelOpen());
    expect(result.current[0]).toBe(false);
  });

  it("keeps what was chosen in the browser, whatever the screen's default", () => {
    const first = renderHook(() => usePanelOpen());
    act(() => first.result.current[1](false));
    expect(storage.getItem(KEY)).toBe("closed");
    first.unmount();

    // A later visit on this device finds it closed even though it is wide.
    expect(renderHook(() => usePanelOpen()).result.current[0]).toBe(false);
  });

  it("lets an opened panel stay open on a phone", () => {
    storage.setItem(KEY, "open");
    stubNarrowLayout();
    expect(renderHook(() => usePanelOpen()).result.current[0]).toBe(true);
  });

  it("works with storage refused: the default, and a choice for this visit", () => {
    vi.spyOn(storage, "getItem").mockImplementation(() => {
      throw new Error("denied");
    });
    vi.spyOn(storage, "setItem").mockImplementation(() => {
      throw new Error("denied");
    });
    const { result } = renderHook(() => usePanelOpen());
    expect(result.current[0]).toBe(true);

    act(() => result.current[1](false));

    expect(result.current[0]).toBe(false);
  });
});
