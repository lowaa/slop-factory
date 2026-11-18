import { describe, expect, it } from "vitest";
import { renderHook } from "@testing-library/react";
import { useLatestRequest } from "./use_latest_request";

describe("useLatestRequest", () => {
  it("lets only the last request begun act, whatever order they resolve in", () => {
    const { result } = renderHook(() => useLatestRequest());
    const first = result.current();
    const second = result.current();
    expect(first()).toBe(false);
    expect(second()).toBe(true);
  });

  it("drops what is in flight when begun without a request", () => {
    const { result } = renderHook(() => useLatestRequest());
    const inFlight = result.current();
    result.current();
    expect(inFlight()).toBe(false);
  });

  it("keeps begin stable across renders", () => {
    const { result, rerender } = renderHook(() => useLatestRequest());
    const before = result.current;
    rerender();
    expect(result.current).toBe(before);
  });
});
