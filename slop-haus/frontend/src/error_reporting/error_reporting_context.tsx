import type { ReactNode } from "react";
import { ErrorFallback, ErrorReportingProvider as Provider } from "@slop-factory/logging/error_reporting/react";
import { ApiError, onApiFailure } from "@/api";
import { useLogger } from "@/context/logger";

const describeFailure = (err: unknown) => (err instanceof ApiError ? { status: err.status, code: err.code } : undefined);

interface ErrorReportingProviderProps {
  // The route template the page is on, from the router.
  currentRoute: () => string | undefined;
  children: ReactNode;
}

export function ErrorReportingProvider({ currentRoute, children }: ErrorReportingProviderProps) {
  return (
    <Provider
      logger={useLogger()}
      currentRoute={currentRoute}
      appName="slop-haus"
      describeFailure={describeFailure}
      subscribeToFailures={onApiFailure}
      // Reload only: the router (and so a link home) may be what failed.
      fallback={<ErrorFallback />}
    >
      {children}
    </Provider>
  );
}
