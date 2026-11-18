import { describe, expect, it, vi } from "vitest";
import type { ErrorReporter } from "./error_reporter";
import { installGlobalHandlers } from "./global_handlers";

function setup() {
  const reportError = vi.fn();
  const target = new EventTarget();
  const uninstall = installGlobalHandlers({ reportError } as unknown as ErrorReporter, target as unknown as Window);
  return { reportError, target, uninstall };
}

describe("installGlobalHandlers", () => {
  it("reports an uncaught error by its error object", () => {
    const { reportError, target } = setup();
    const error = new Error("boom");
    target.dispatchEvent(Object.assign(new Event("error"), { error, message: "Uncaught Error: boom" }));
    expect(reportError).toHaveBeenCalledWith("uncaught error", error);
  });

  it("reports an error whose script hid the error object, from its message", () => {
    const { reportError, target } = setup();
    target.dispatchEvent(Object.assign(new Event("error"), { error: null, message: "Script error." }));
    expect(reportError).toHaveBeenCalledTimes(1);
    const [kind, err] = reportError.mock.calls[0];
    expect(kind).toBe("uncaught error");
    expect((err as Error).message).toBe("Script error.");
  });

  it("reports an unhandled rejection by its reason", () => {
    const { reportError, target } = setup();
    const reason = new TypeError("nope");
    target.dispatchEvent(Object.assign(new Event("unhandledrejection"), { reason }));
    expect(reportError).toHaveBeenCalledWith("unhandled rejection", reason);
  });

  it("on a Vite preload error, reports a chunk load failure and asks for a recovery", () => {
    const reportError = vi.fn();
    const onPreloadError = vi.fn();
    const target = new EventTarget();
    installGlobalHandlers({ reportError } as unknown as ErrorReporter, target as unknown as Window, onPreloadError);
    const payload = new TypeError("Failed to fetch dynamically imported module");
    target.dispatchEvent(Object.assign(new Event("vite:preloadError"), { payload }));
    expect(reportError).toHaveBeenCalledWith("chunk load failed", payload);
    expect(onPreloadError).toHaveBeenCalledOnce();
  });

  it("stops reporting once uninstalled", () => {
    const { reportError, target, uninstall } = setup();
    uninstall();
    target.dispatchEvent(Object.assign(new Event("error"), { error: new Error("x"), message: "x" }));
    target.dispatchEvent(Object.assign(new Event("unhandledrejection"), { reason: "x" }));
    target.dispatchEvent(Object.assign(new Event("vite:preloadError"), { payload: new Error("x") }));
    expect(reportError).not.toHaveBeenCalled();
  });
});
