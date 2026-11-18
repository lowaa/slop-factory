import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { ResourceProvider, createResourceClient, invalidate, setResource, useResource, type ResourceKey } from ".";

function deferred<T>() {
  let resolve: (v: T) => void = () => {};
  let reject: (err: unknown) => void = () => {};
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const KEY: ResourceKey = ["test", "thing"];

function setup() {
  const client = createResourceClient();
  const wrapper = ({ children }: { children: ReactNode }) => <ResourceProvider client={client}>{children}</ResourceProvider>;
  return { client, wrapper };
}

describe("useResource", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("drops a slow response that lands after a newer one", async () => {
    const { wrapper } = setup();
    const first = deferred<string>();
    const second = deferred<string>();
    const fetcher = vi.fn().mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    const { result } = renderHook(() => useResource(KEY, fetcher), { wrapper });

    act(() => {
      void result.current.reload();
    });
    await act(async () => second.resolve("new"));
    await act(async () => first.resolve("old"));

    expect(result.current.data).toBe("new");
  });

  it("shares one fetch between two views of a key", async () => {
    const { wrapper } = setup();
    const fetcher = vi.fn().mockResolvedValue("a");
    const { result } = renderHook(() => [useResource(KEY, fetcher), useResource(KEY, fetcher)] as const, { wrapper });
    await act(async () => {});

    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(result.current[1].data).toBe("a");
  });

  it("invalidate refetches the key for every view", async () => {
    const { wrapper, client } = setup();
    const fetcher = vi.fn().mockResolvedValueOnce("a").mockResolvedValue("b");
    const { result } = renderHook(() => useResource(KEY, fetcher), { wrapper });
    await act(async () => {});

    await act(async () => invalidate(KEY, client));

    expect(result.current.data).toBe("b");
  });

  it("setResource replaces the value without a fetch", async () => {
    const { wrapper, client } = setup();
    const fetcher = vi.fn().mockResolvedValue(1);
    const { result } = renderHook(() => useResource<number>(KEY, fetcher), { wrapper });
    await act(async () => {});

    await act(async () => setResource<number>(KEY, (n) => (n ?? 0) + 10, client));

    expect(result.current.data).toBe(11);
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("does not fetch while disabled", async () => {
    const { wrapper } = setup();
    const fetcher = vi.fn().mockResolvedValue("a");
    const { result } = renderHook(() => useResource(KEY, fetcher, { enabled: false }), { wrapper });
    await act(async () => {});

    expect(fetcher).not.toHaveBeenCalled();
    expect(result.current.status).toBe("loading");
  });

  it("keeps the value for the next view after the last one unmounts", async () => {
    const { wrapper } = setup();
    const fetcher = vi.fn().mockResolvedValue("a");
    const first = renderHook(() => useResource(KEY, fetcher), { wrapper });
    await act(async () => {});
    first.unmount();

    const second = renderHook(() => useResource(KEY, fetcher), { wrapper });

    expect(second.result.current).toMatchObject({ status: "ready", data: "a" });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("invalidates only the named key", async () => {
    const { wrapper, client } = setup();
    const other: ResourceKey = ["test", "other"];
    const fetchThing = vi.fn().mockResolvedValueOnce("a").mockResolvedValue("b");
    const fetchOther = vi.fn().mockResolvedValue("x");
    renderHook(() => [useResource(KEY, fetchThing), useResource(other, fetchOther)] as const, { wrapper });
    await act(async () => {});

    await act(async () => invalidate(KEY, client));

    expect(fetchThing).toHaveBeenCalledTimes(2);
    expect(fetchOther).toHaveBeenCalledTimes(1);
  });

  it("setResource wins over a read that was in flight", async () => {
    const { wrapper, client } = setup();
    const slow = deferred<number>();
    const fetcher = vi.fn().mockResolvedValueOnce(1).mockReturnValueOnce(slow.promise);
    const { result } = renderHook(() => useResource<number>(KEY, fetcher), { wrapper });
    await act(async () => {});
    await act(async () => {
      void result.current.reload();
    });

    await act(async () => setResource<number>(KEY, () => 99, client));
    await act(async () => slow.resolve(2));

    expect(result.current.data).toBe(99);
  });

  describe("polling", () => {
    beforeEach(() => vi.useFakeTimers());

    it("lets a request slower than the interval land", async () => {
      const { wrapper } = setup();
      const slow = deferred<string>();
      const fetcher = vi.fn().mockResolvedValueOnce("a").mockReturnValueOnce(slow.promise);
      const { result } = renderHook(() => useResource(KEY, fetcher, { pollMs: 1000 }), { wrapper });
      await act(async () => {});
      await act(async () => vi.advanceTimersByTimeAsync(1000));
      await act(async () => vi.advanceTimersByTimeAsync(3000));
      expect(fetcher).toHaveBeenCalledTimes(2);

      await act(async () => slow.resolve("b"));

      expect(result.current.data).toBe("b");
    });

    it("polls once for views sharing the key", async () => {
      const { wrapper } = setup();
      // The tick's request stays pending, as it would for the instant the two
      // views' timers fire one after the other.
      const fetcher = vi.fn().mockResolvedValueOnce("a").mockReturnValue(new Promise(() => {}));
      renderHook(() => [useResource(KEY, fetcher, { pollMs: 1000 }), useResource(KEY, fetcher, { pollMs: 1000 })] as const, { wrapper });
      await act(async () => {});
      fetcher.mockClear();

      await act(async () => vi.advanceTimersByTimeAsync(1000));

      expect(fetcher).toHaveBeenCalledTimes(1);
    });
  });
});
