import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { ApiError } from "@/api";
import { usePolledResource } from "./use_polled_resource";

// A request the test settles by hand, so ordering is held, not raced.
function deferred<T>() {
  let resolve: (v: T) => void = () => {};
  let reject: (err: unknown) => void = () => {};
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe("usePolledResource", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("is loading, then ready with the data", async () => {
    const req = deferred<string>();
    const { result } = renderHook(() => usePolledResource(() => req.promise));
    expect(result.current.status).toBe("loading");

    await act(async () => req.resolve("a"));

    expect(result.current).toMatchObject({ status: "ready", data: "a", error: null });
  });

  it("ignores a late response after a newer request was applied", async () => {
    const first = deferred<string>();
    const second = deferred<string>();
    const fetcher = vi.fn().mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    const { result } = renderHook(() => usePolledResource(fetcher));

    act(() => {
      void result.current.reload();
    });
    await act(async () => second.resolve("new"));
    await act(async () => first.resolve("old"));

    expect(result.current.data).toBe("new");
  });

  it("ignores a late failure after a newer request succeeded", async () => {
    const first = deferred<string>();
    const second = deferred<string>();
    const fetcher = vi.fn().mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    const { result } = renderHook(() => usePolledResource(fetcher));

    act(() => {
      void result.current.reload();
    });
    await act(async () => second.resolve("new"));
    await act(async () => first.reject(new Error("late")));

    expect(result.current).toMatchObject({ status: "ready", data: "new", error: null });
  });

  it("applies nothing after unmount", async () => {
    const req = deferred<string>();
    const { result, unmount } = renderHook(() => usePolledResource(() => req.promise));
    unmount();
    await act(async () => req.resolve("a"));

    expect(result.current.status).toBe("loading");
  });

  it("stops polling on unmount", async () => {
    const fetcher = vi.fn().mockResolvedValue("a");
    const { unmount } = renderHook(() => usePolledResource(fetcher, { intervalMs: 1000 }));
    await act(async () => {});
    unmount();
    await act(async () => vi.advanceTimersByTimeAsync(5000));

    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("reloads on the interval", async () => {
    const fetcher = vi.fn().mockResolvedValueOnce("a").mockResolvedValue("b");
    const { result } = renderHook(() => usePolledResource(fetcher, { intervalMs: 1000 }));
    await act(async () => {});
    expect(result.current.data).toBe("a");

    await act(async () => vi.advanceTimersByTimeAsync(1000));

    expect(result.current.data).toBe("b");
  });

  it("skips interval ticks while the tab is hidden", async () => {
    const fetcher = vi.fn().mockResolvedValue("a");
    const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    renderHook(() => usePolledResource(fetcher, { intervalMs: 1000 }));
    await act(async () => vi.advanceTimersByTimeAsync(3000));
    expect(fetcher).toHaveBeenCalledTimes(1);

    visibility.mockReturnValue("visible");
    await act(async () => {
      document.dispatchEvent(new Event("visibilitychange"));
    });

    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it("reload() fetches again on demand", async () => {
    const fetcher = vi.fn().mockResolvedValueOnce("a").mockResolvedValue("b");
    const { result } = renderHook(() => usePolledResource(fetcher));
    await act(async () => {});

    await act(async () => result.current.reload());

    expect(result.current.data).toBe("b");
  });

  it("reports an error, keeping the last good data", async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce("a")
      .mockRejectedValueOnce(new Error("boom"))
      .mockRejectedValueOnce(new ApiError(500, "server said no"));
    const { result } = renderHook(() => usePolledResource(fetcher, { fallbackError: "Could not load a." }));
    await act(async () => {});

    await act(async () => result.current.reload());
    expect(result.current).toMatchObject({ status: "error", data: "a", error: "Could not load a." });

    await act(async () => result.current.reload());
    expect(result.current.error).toBe("server said no");
  });

  it("lets the caller word a failure", async () => {
    const fetcher = vi.fn().mockRejectedValue(new ApiError(503, "upstream"));
    const { result } = renderHook(() =>
      usePolledResource(fetcher, { describeError: (err) => (err instanceof ApiError && err.status === 503 ? "too slow" : "other") }),
    );
    await act(async () => {});
    expect(result.current).toMatchObject({ status: "error", error: "too slow" });
  });

  it("starts over when deps change", async () => {
    const fetcher = vi.fn((id: string) => Promise.resolve(id));
    const { result, rerender } = renderHook(({ id }) => usePolledResource(() => fetcher(id), { deps: [id] }), {
      initialProps: { id: "x" },
    });
    await act(async () => {});
    expect(result.current.data).toBe("x");

    rerender({ id: "y" });
    expect(result.current).toMatchObject({ status: "loading", data: undefined });
    await act(async () => {});

    expect(result.current.data).toBe("y");
  });
});
