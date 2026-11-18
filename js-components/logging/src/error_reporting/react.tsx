import { Component, createContext, useContext, useEffect, useMemo, useRef, useState, type ErrorInfo, type ReactNode } from "react";
import type { Logger } from "../logger";
import type { ApiFailure } from "./api_failures";
import { BuildTag } from "./build_tag";
import { ErrorReporter, type RequestFailure } from "./error_reporter";
import { ErrorFallback } from "./error_fallback";
import { installGlobalHandlers } from "./global_handlers";
import { AppBuildContext, ChunkLoadFallback, UpdateNotice } from "./app_update";
import { ChunkRecovery, isChunkLoadError } from "./chunk_recovery";
import { UpdateWatcher } from "./update_watcher";

export { ErrorFallback, ChunkLoadFallback, UpdateNotice, AppBuildContext };
export { isChunkLoadError };

export const ErrorReporterContext = createContext<ErrorReporter | null>(null);

export function useErrorReporter(): ErrorReporter {
  const ctx = useContext(ErrorReporterContext);
  if (!ctx) throw new Error("useErrorReporter must be used within an ErrorReporterContext provider");
  return ctx;
}

interface BoundaryProps {
  reporter: ErrorReporter;
  // What to show instead. It must not assume the router or any provider
  // still works.
  fallback: ReactNode;
  children: ReactNode;
}

// The last line of defence: catches a render error from anywhere under it,
// including the providers and the router itself, which a route's own error
// component can't, and reports it through the reporter.
export class ReportingErrorBoundary extends Component<BoundaryProps, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  componentDidCatch(error: unknown, info: ErrorInfo) {
    this.props.reporter.reportError("render error", error, info.componentStack);
  }

  render() {
    return this.state.failed ? this.props.fallback : this.props.children;
  }
}

interface RouteErrorProps {
  error: unknown;
  info?: { componentStack: string };
  children: ReactNode;
}

// Reports the error a router's own boundary caught, which would otherwise
// never reach ReportingErrorBoundary, and shows children (the fallback).
export function RouteErrorReporter({ error, info, children }: RouteErrorProps) {
  const reporter = useErrorReporter();
  const chunkFailed = isChunkLoadError(error);
  // The reporter dedupes, so StrictMode's second effect run costs nothing.
  // A failed chunk is reported by ChunkLoadFallback, as its own kind.
  useEffect(() => {
    if (!chunkFailed) reporter.reportError("render error", error, info?.componentStack);
  }, [reporter, error, info, chunkFailed]);
  if (chunkFailed) return <ChunkLoadFallback reporter={reporter} error={error} componentStack={info?.componentStack} />;
  return <>{children}</>;
}

// Builds the router's error component: TanStack catches a route's error in
// its own boundary, which the top-level one never sees. The router is known
// to work here, but the way home is still a plain link, as in ErrorFallback.
export function createRouteErrorFallback(homeHref: string) {
  return function RouteErrorFallback({ error, info }: { error: unknown; info?: { componentStack: string } }) {
    return (
      <RouteErrorReporter error={error} info={info}>
        <ErrorFallback homeHref={homeHref} />
      </RouteErrorReporter>
    );
  };
}

interface ErrorReportingProviderProps {
  logger: Logger;
  // The route template the page is on, from the router.
  currentRoute: () => string | undefined;
  describeFailure: (err: unknown) => RequestFailure | undefined;
  // Registers who hears of failed requests; returns the unsubscribe.
  subscribeToFailures: (listener: (failure: ApiFailure) => void) => () => void;
  // Failures this returns true for are not reported: an outcome the page
  // answers itself, not a fault.
  ignoreFailure?: (failure: ApiFailure) => boolean;
  // What the messages call the app, e.g. "slop-haus". Given, it also turns on
  // the notice that a newer build is live.
  appName?: string;
  // What the top-level boundary shows. It can't assume the router works.
  fallback: ReactNode;
  children: ReactNode;
}

// Always on: crashes and failed requests are operational-tier records,
// reported for every user (go-privacy/README.md §1). Wires one reporter to
// the global handlers, the API client's failures and a top-level error
// boundary.
export function ErrorReportingProvider({
  logger,
  currentRoute,
  describeFailure,
  subscribeToFailures,
  ignoreFailure,
  appName,
  fallback,
  children,
}: ErrorReportingProviderProps) {
  // Created here, at app start: this is the build the tab runs (see BuildTag).
  const [buildTag] = useState(() => new BuildTag());
  const [recovery] = useState(() => new ChunkRecovery({ buildTag }));
  const [watcher] = useState(() => new UpdateWatcher({ buildTag }));
  const build = useMemo(() => ({ recovery, appName: appName ?? "This app" }), [recovery, appName]);
  const [reporter] = useState(
    () =>
      new ErrorReporter({
        logger,
        currentRoute,
        buildTag: () => buildTag.get(),
        serverBuildTag: () => buildTag.serverTag(),
        userAgent: navigator.userAgent,
        describeFailure,
      }),
  );

  // Read through a ref so a new predicate each render neither resubscribes
  // nor leaves the listener on the previous one. Set while rendering for the
  // same reason as ReportingIdentity's id.
  const ignore = useRef(ignoreFailure);
  ignore.current = ignoreFailure;

  useEffect(() => {
    const uninstall = installGlobalHandlers(reporter, window, () => void recovery.attempt());
    const unsubscribe = subscribeToFailures((f) => {
      if (!ignore.current?.(f)) reporter.reportApiFailure(f);
    });
    return () => {
      uninstall();
      unsubscribe();
    };
  }, [reporter, recovery, subscribeToFailures]);

  const watching = appName !== undefined;
  useEffect(() => (watching ? watcher.start() : undefined), [watcher, watching]);

  return (
    <ErrorReporterContext.Provider value={reporter}>
      <AppBuildContext.Provider value={build}>
        <ReportingErrorBoundary reporter={reporter} fallback={fallback}>
          {children}
        </ReportingErrorBoundary>
        {watching && <UpdateNotice watcher={watcher} appName={appName} />}
      </AppBuildContext.Provider>
    </ErrorReporterContext.Provider>
  );
}

// Tells the reporter who is signed in, so a record carries the user id and
// nothing else that identifies a person. Renders nothing.
export function ReportingIdentity({ userId }: { userId: string | undefined }) {
  const reporter = useErrorReporter();
  // Set while rendering, not in an effect: a crash in the render that follows
  // a sign-in or sign-out must already carry the new identity, not the old
  // one. It only stores a value, so repeating it is harmless.
  reporter.setUserId(userId);
  return null;
}
