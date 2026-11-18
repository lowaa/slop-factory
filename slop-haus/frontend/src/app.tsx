import { useEffect, useMemo, useRef } from "react";
import { RouterProvider } from "@slop-factory/router";
import { ConsentProvider, type ConsentStore } from "@slop-factory/privacy/consent";
import { AuthProvider } from "@/context/auth_context";
import { LoggerProvider } from "@/context/logger_context";
import { ErrorReportingProvider } from "@/error_reporting/error_reporting_context";
import { useAuth } from "@/context/auth";
import { legalDocHref } from "@/privacy/legal_links";
import { PURPOSE_COPY } from "@/privacy/purpose_copy";
import { UsageProvider } from "@/usage/usage_provider";
import type { SlopHausRouter } from "./routes";
import "./app.scss";

interface AppProps {
  // Created by the caller rather than here: a browser-history router hooks
  // window.history for as long as it exists, so there must be exactly one
  // per page (main.tsx's), and a test must be able to tear its own down.
  router: SlopHausRouter;
  // Created by the caller for the same reason: the logger's consent check
  // reads it, and a test supplies one with no network behind it.
  consent: ConsentStore;
}

export default function App({ router, consent }: AppProps) {
  const hasAnalyticsConsent = useMemo(() => consent.check("analytics"), [consent]);
  return (
    <ConsentProvider store={consent} documentHref={legalDocHref} copy={PURPOSE_COPY}>
      <LoggerProvider hasAnalyticsConsent={hasAnalyticsConsent} subscribeToConsent={consent.subscribe}>
        <ErrorReportingProvider currentRoute={() => router.state.matches.at(-1)?.fullPath}>
          <AuthProvider>
            <RootView router={router} />
          </AuthProvider>
        </ErrorReportingProvider>
      </LoggerProvider>
    </ConsentProvider>
  );
}

function RootView({ router }: Pick<AppProps, "router">) {
  const { status, signedOutByUser } = useAuth();
  const previousStatus = useRef(status);

  // The routes' guards decide from auth status, so they have to run again
  // whenever it changes (signing in on /login, a session ending on the
  // board). The first answer needs no re-run: the router has not loaded
  // until then, and loads with it when it mounts.
  useEffect(() => {
    const previous = previousStatus.current;
    previousStatus.current = status;
    if (previous !== "loading" && previous !== status) void router.invalidate();
  }, [router, status]);

  // Nothing is routed until the session is known: a guard deciding before
  // then would flash the login page at a signed-in visitor.
  if (status === "loading") return <p className="app-loading">Loading…</p>;

  return (
    <UsageProvider router={router} enabled={status === "authenticated"}>
      <RouterProvider router={router} context={{ authStatus: status, rememberReturnPath: !signedOutByUser }} />
    </UsageProvider>
  );
}
