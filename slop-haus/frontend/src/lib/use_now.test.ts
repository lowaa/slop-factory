import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useNow } from "./use_now";

describe("useNow", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("re-reads the clock each interval and stops on unmount", () => {
    vi.useFakeTimers();
    vi.setSystemTime(1_000);
    const { result, unmount } = renderHook(() => useNow(60_000));
    expect(result.current).toBe(1_000);

    act(() => {
      vi.advanceTimersByTime(60_000);
    });
    expect(result.current).toBe(61_000);

    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });
});
