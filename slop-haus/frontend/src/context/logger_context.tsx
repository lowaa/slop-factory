import { useEffect, useState, type ReactNode } from "react";
import { OtelLogger } from "@slop-factory/logging/otel_logger";
import { sessionWriteHeaders } from "@/api";
import { LoggerContext } from "./logger";

// Same-origin, so the browser needs no CORS and nginx's /api/ proxy already
// reaches it. Served by server/internal/otelproxy, which rate limits, forces
// service.name and filters the payload to the operational tier again -- the
// log store itself is not reachable from the browser.
const OTEL_COLLECTOR_PATH = "/api/otel";

// Must match server/internal/otelproxy's ServiceName, which forces it.
const SERVICE_NAME = "slop-haus-frontend";

// The only attributes that leave the browser; everything else is dropped.
// The operational tier's allowlist from go-privacy/README.md -- route
// template (http.route, with the request method and the server's error code
// for a failed request, and the trace id of its traceparent), error type, message and stack, build tag, user
// agent. The signed-in user's id is not sent: the server's otelproxy adds it
// from the verified session, so a report can't name someone else. What fills them is limited at the
// source (error_reporting/): this list is the second gate, and the server's
// otelproxy.OperationalAttributes the third, which must stay the same list.
// No analytics attribute is allowed: slop-haus has no analytics events.
const ATTRIBUTE_ALLOWLIST = {
  operational: [
    "status",
    "error.type",
    "error.message",
    "error.stack",
    "error.code",
    "http.request.method",
    "http.route",
    "trace_id",
    "build.tag",
    "build.tag.server",
    "user_agent.original",
  ],
  analytics: [],
};

// slop-haus has no analytics events, so none can be exported.
const ANALYTICS_EVENTS: readonly string[] = [];

// Analytics records are exported only while analytics consent is in force.
// slop-haus emits none today (ANALYTICS_EVENTS is empty), so until it does
// this decides nothing: it is wired so the first event can't ship without it.
const NO_CONSENT = () => false;

interface LoggerProviderProps {
  children: ReactNode;
  // Reads the live consent state, so a withdrawal applies to the next
  // record. Absent (a test, a page without consent) nothing is exported.
  hasAnalyticsConsent?: () => boolean;
  // Says when consent changes, so what the logger keeps on the device (the
  // session id) is cleared at the moment of a withdrawal rather than at the
  // next record -- of which slop-haus emits few.
  subscribeToConsent?: (listener: () => void) => () => void;
}

// Provides the app's Logger. Its config is a constant: this must need no
// ancestor providers, including no auth, because a failed sign-in is
// itself reported.
export function LoggerProvider({ children, hasAnalyticsConsent = NO_CONSENT, subscribeToConsent }: LoggerProviderProps) {
  const [logger] = useState(
    () =>
      new OtelLogger({
        serviceName: SERVICE_NAME,
        collectorPublicUrl: OTEL_COLLECTOR_PATH,
        // The proxy is OptionalUser: a signed-in browser's write is checked
        // for the session's CSRF token like any other.
        headers: sessionWriteHeaders,
        attributeAllowlist: ATTRIBUTE_ALLOWLIST,
        analyticsEvents: ANALYTICS_EVENTS,
        hasAnalyticsConsent,
        warnOnDrop: import.meta.env.DEV,
      }),
  );

  useEffect(() => {
    const unsubscribe = subscribeToConsent?.(() => logger.syncConsent());
    return () => {
      unsubscribe?.();
      // Quiet: this is teardown, and there is nowhere left to report a logger failure.
      logger.shutdown().catch(() => {});
    };
  }, [logger, subscribeToConsent]);

  return <LoggerContext.Provider value={logger}>{children}</LoggerContext.Provider>;
}
