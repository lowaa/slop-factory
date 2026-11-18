import type { ErrorReporter } from "./error_reporter";

// Reports what nothing else caught: an uncaught exception and a promise
// rejected with no handler. Returns the uninstall function.
//
// onPreloadError is told when Vite fails to preload a lazy route's chunk
// (the event fires before the import's own rejection), where a reload can
// still help. The failure is reported either way.
export function installGlobalHandlers(reporter: ErrorReporter, target: Window = window, onPreloadError?: () => void): () => void {
  const onError = (event: ErrorEvent) => {
    // A script from another origin hides its error; a bare message is all
    // there is, and safeErrorMessage decides whether it may be said.
    reporter.reportError("uncaught error", event.error ?? new Error(event.message));
  };
  const onRejection = (event: PromiseRejectionEvent) => {
    reporter.reportError("unhandled rejection", event.reason);
  };
  const onPreload = (event: Event) => {
    reporter.reportError("chunk load failed", (event as Event & { payload?: unknown }).payload);
    onPreloadError?.();
  };
  target.addEventListener("vite:preloadError", onPreload);
  target.addEventListener("error", onError);
  target.addEventListener("unhandledrejection", onRejection);
  return () => {
    target.removeEventListener("vite:preloadError", onPreload);
    target.removeEventListener("error", onError);
    target.removeEventListener("unhandledrejection", onRejection);
  };
}
