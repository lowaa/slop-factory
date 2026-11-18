// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { AppBuildContext, ChunkLoadFallback, UpdateNotice } from "./app_update";
import { createRouteErrorFallback, ErrorReporterContext } from "./react";
import type { ChunkRecovery } from "./chunk_recovery";
import type { ErrorReporter } from "./error_reporter";
import type { UpdateWatcher } from "./update_watcher";

afterEach(cleanup);

const chunkError = () => new TypeError("Failed to fetch dynamically imported module: https://x/assets/a.js");

function withBuild(recovery: Pick<ChunkRecovery, "canReload" | "attempt">, ui: React.ReactNode, reporter?: ErrorReporter) {
  const reporterValue = reporter ?? ({ reportError: vi.fn() } as unknown as ErrorReporter);
  return render(
    <ErrorReporterContext.Provider value={reporterValue}>
      <AppBuildContext.Provider value={{ recovery: recovery as ChunkRecovery, appName: "slop-haus" }}>{ui}</AppBuildContext.Provider>
    </ErrorReporterContext.Provider>,
  );
}

function fakeWatcher(initial: string | undefined) {
  let tag = initial;
  const listeners = new Set<() => void>();
  const dismiss = vi.fn(() => {
    tag = undefined;
    listeners.forEach((l) => l());
  });
  const watcher = {
    getSnapshot: () => tag,
    subscribe: (l: () => void) => (listeners.add(l), () => void listeners.delete(l)),
    dismiss,
  } as unknown as UpdateWatcher;
  return { watcher, dismiss };
}

describe("UpdateNotice", () => {
  it("shows nothing without a newer build", () => {
    render(<UpdateNotice watcher={fakeWatcher(undefined).watcher} appName="slop-haus" />);
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("offers a reload, and a dismiss that hides it", () => {
    const { watcher, dismiss } = fakeWatcher("b2");
    render(<UpdateNotice watcher={watcher} appName="slop-haus" />);
    expect(screen.getByRole("status").textContent).toContain("slop-haus has been updated");
    expect(screen.getByRole("button", { name: "Reload" })).toBeTruthy();
    act(() => fireEvent.click(screen.getByRole("button", { name: "Dismiss" })));
    expect(dismiss).toHaveBeenCalledOnce();
    expect(screen.queryByRole("status")).toBeNull();
  });
});

describe("ChunkLoadFallback", () => {
  it("reloads once and reports the failure as a chunk load failure", () => {
    const attempt = vi.fn(() => "reloading" as const);
    const reportError = vi.fn();
    withBuild({ canReload: () => true, attempt }, <ChunkLoadFallback reporter={{ reportError } as unknown as ErrorReporter} error={chunkError()} />);
    expect(attempt).toHaveBeenCalled();
    expect(reportError.mock.calls[0][0]).toBe("chunk load failed");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("asks for a reload when the attempt itself is refused", () => {
    withBuild({ canReload: () => true, attempt: () => "blocked" }, <ChunkLoadFallback error={chunkError()} />);
    expect(screen.getByRole("alert")).toBeTruthy();
  });

  it("with the guard already set, asks for a reload instead of saying server error", () => {
    withBuild({ canReload: () => false, attempt: () => "blocked" }, <ChunkLoadFallback error={chunkError()} />);
    expect(screen.getByRole("alert").textContent).toContain("slop-haus has been updated (or the connection dropped) — reload to continue");
    expect(screen.getByRole("button", { name: "Reload" })).toBeTruthy();
    expect(screen.queryByText("Something went wrong")).toBeNull();
  });
});

describe("route error fallback", () => {
  const RouteErrorFallback = createRouteErrorFallback("/");

  it("a chunk error reloads once, not the normal error UI", () => {
    const attempt = vi.fn(() => "reloading" as const);
    withBuild({ canReload: () => true, attempt }, <RouteErrorFallback error={chunkError()} />);
    expect(attempt).toHaveBeenCalled();
    expect(screen.queryByText("Something went wrong")).toBeNull();
  });

  it("a non-chunk render error still goes to the normal error UI and never reloads", () => {
    const attempt = vi.fn(() => "reloading" as const);
    const reportError = vi.fn();
    withBuild({ canReload: () => true, attempt }, <RouteErrorFallback error={new Error("boom")} />, { reportError } as unknown as ErrorReporter);
    expect(screen.getByText("Something went wrong")).toBeTruthy();
    expect(attempt).not.toHaveBeenCalled();
    expect(reportError.mock.calls[0][0]).toBe("render error");
  });
});
