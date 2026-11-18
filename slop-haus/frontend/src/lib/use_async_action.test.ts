import { createElement, type ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { ErrorReporterContext } from "@slop-factory/logging/error_reporting/react";
import type { ErrorReporter } from "@slop-factory/logging/error_reporting";
import { ApiError } from "@/api";
import { useAsyncAction } from "./use_async_action";

// A task the test settles by hand, so "in flight" is a state the test
// holds rather than a window it races.
function deferred() {
  let resolve: () => void = () => {};
  let reject: (err: unknown) => void = () => {};
  const promise = new Promise<void>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe("useAsyncAction", () => {
  it("is busy while the task is in flight, and idle with no error once it succeeds", async () => {
    const { result } = renderHook(() => useAsyncAction());
    const task = deferred();

    let run: Promise<void> = Promise.resolve();
    act(() => {
      run = result.current.run(() => task.promise, "fallback");
    });
    expect(result.current.busy).toBe(true);

    await act(async () => {
      task.resolve();
      await run;
    });
    expect(result.current.busy).toBe(false);
    expect(result.current.error).toBeNull();
  });

  it("shows the server's message for an ApiError and the fallback for anything else", async () => {
    const { result } = renderHook(() => useAsyncAction());

    await act(() => result.current.run(() => Promise.reject(new ApiError(422, "refused, and why")), "fallback"));
    expect(result.current.error).toBe("refused, and why");
    expect(result.current.busy).toBe(false);

    await act(() => result.current.run(() => Promise.reject(new TypeError("network")), "fallback"));
    expect(result.current.error).toBe("fallback");
  });

  it("clears the last error as soon as the next attempt starts", async () => {
    const { result } = renderHook(() => useAsyncAction());
    await act(() => result.current.run(() => Promise.reject(new ApiError(500, "first failure")), "fallback"));
    expect(result.current.error).toBe("first failure");

    const task = deferred();
    let run: Promise<void> = Promise.resolve();
    act(() => {
      run = result.current.run(() => task.promise, "fallback");
    });
    expect(result.current.error).toBeNull();

    await act(async () => {
      task.resolve();
      await run;
    });
  });

  it("lets a failure function decide the message, or that there is none to show", async () => {
    const { result } = renderHook(() => useAsyncAction());

    await act(() => result.current.run(() => Promise.reject(new Error("x")), () => "decided here"));
    expect(result.current.error).toBe("decided here");

    await act(() => result.current.run(() => Promise.reject(new Error("x")), () => null));
    expect(result.current.error).toBeNull();
    expect(result.current.busy).toBe(false);
  });

  it("does not throw the task's failure to its caller", async () => {
    const { result } = renderHook(() => useAsyncAction());
    await expect(
      act(() => result.current.run(() => Promise.reject(new Error("x")), "fallback")),
    ).resolves.toBeUndefined();
  });

  it("reports a failure that is not an ApiError, which no global handler would see", async () => {
    const reportError = vi.fn();
    const wrapper = ({ children }: { children: ReactNode }) =>
      createElement(ErrorReporterContext.Provider, { value: { reportError } as unknown as ErrorReporter }, children);
    const { result } = renderHook(() => useAsyncAction(), { wrapper });
    const boom = new TypeError("boom");

    await act(() => result.current.run(() => Promise.reject(boom), "fallback"));
    expect(reportError).toHaveBeenCalledWith("action failed", boom);

    reportError.mockClear();
    await act(() => result.current.run(() => Promise.reject(new ApiError(500, "x")), "fallback"));
    expect(reportError).not.toHaveBeenCalled();
  });
});
