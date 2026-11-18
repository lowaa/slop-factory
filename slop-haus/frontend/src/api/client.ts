import { traceparentMiddleware } from "@slop-factory/logging/tracing";
import {
  ApiFailureBus,
  failureMiddleware,
  type ApiFailure,
} from "@slop-factory/logging/error_reporting";
import {
  CSRF_HEADER,
  TENANT_QUERY,
  type SessionClient,
  type SessionMode,
} from "@slop-factory/auth";
import {
  ApiError,
  BulkRefusedError as SharedBulkRefusedError,
  createApiClients,
  errorCode,
  type ErrorEnvelope,
} from "@slop-factory/auth/api_client";
import type { paths } from "@/api_generated/schema";
import type { BulkRefusal, RoleInUseTicket, SetupScript } from "./types";

// filenameFrom reads the name out of a Content-Disposition header. The
// server picks it, because an agent name is not something to let choose a
// filename.
function filenameFrom(disposition: string | null): string | null {
  if (!disposition) return null;
  const match = /filename="([^"]+)"/.exec(disposition);
  return match?.[1] ?? null;
}

export { ApiError };

// RoleInUseError is deleteAgentRole's 409: the role is still required by
// these tickets, and the caller decides whether to remove it from them too
// (deleteAgentRole's clearTickets) or leave it. A subclass of ApiError,
// not a separate return shape -- a caller that doesn't care why still
// catches it as a plain ApiError, and only agent_roles.tsx's delete flow
// needs the ticket list to offer the clear-and-delete choice.
export class RoleInUseError extends ApiError {
  tickets: RoleInUseTicket[];

  constructor(message: string, tickets: RoleInUseTicket[]) {
    super(409, message);
    this.name = "RoleInUseError";
    this.tickets = tickets;
  }
}

// BulkRefusedError is a bulk action's 404 or 409: nothing changed, and
// refusals names every ticket that stopped it and why. A subclass of
// ApiError, like RoleInUseError, so a caller that doesn't need the list
// still catches it as one.
export const BulkRefusedError = SharedBulkRefusedError;
export type BulkRefusedError = SharedBulkRefusedError<BulkRefusal>;

// Requests are same-origin ("/api/..."), not an absolute backend URL: in
// docker-compose-dev the frontend's nginx proxies /api/* to the `api`
// service, and in local `npm run dev` vite.config.ts proxies the same path
// -- this avoids needing any CORS support in the Go backend.
export const BASE_URL = "";

// serializeQuery encodes query parameters the way URLSearchParams does --
// an array as repeated ?status=open&status=blocked ("explode" style, not
// comma-separated), a space as "+" -- and leaves out undefined ones, so an
// option the caller didn't set isn't sent at all.
function serializeQuery(query: Record<string, unknown>): string {
  const params = new URLSearchParams();
  for (const [name, value] of Object.entries(query)) {
    if (value === undefined || value === null) continue;
    for (const item of Array.isArray(value) ? value : [value])
      params.append(name, String(item));
  }
  return params.toString();
}

// sendFor is the fetch openapi-fetch calls for one SessionMode. It sends
// the Request it is handed on as a URL and init rather than through
// SessionClient.fetchFor, which retries by cloning the Request: Chromium
// sends a cloned Request's body fine, but doesn't show it in DevTools (or
// to Playwright's request.postData()), so every write would lose its
// visible payload.
function sendFor(
  session: SessionClient,
  mode: SessionMode,
): (request: Request) => Promise<Response> {
  return async (request) => {
    const headers: Record<string, string> = {};
    request.headers.forEach((value, name) => {
      headers[name] = value;
    });
    const body = await request.text();
    return session.send(
      request.url,
      { method: request.method, headers, body: body || undefined },
      mode,
    );
  };
}

const failures = new ApiFailureBus();

// onApiFailure registers who hears of failed requests (error reporting
// does). Returns an unsubscribe function.
export function onApiFailure(
  listener: (failure: ApiFailure) => void,
): () => void {
  return failures.subscribe(listener);
}

export type { ApiFailure };

// code "role_in_use" is deleteAgentRole's 409 (role_handler.go's
// RoleInUseBody), which carries the tickets still requiring the role.
function refine({ detail, code, fields }: ErrorEnvelope): ApiError | undefined {
  if (code === "role_in_use" && Array.isArray(fields.tickets)) {
    return new RoleInUseError(detail, fields.tickets as RoleInUseTicket[]);
  }
  return undefined;
}

// A fresh W3C traceparent on same-origin API requests, so a failed request's
// error report carries a trace id the server's logs for it share
// (js-components/logging/src/tracing). The telemetry proxy is called by the
// logger's own exporter and never carries one. The failure middleware must
// come after: its onResponse runs in reverse and finds the request this tags.
const propagateTrace = traceparentMiddleware({
  origin: () => window.location.origin,
  includePrefixes: ["/api/"],
  excludePrefixes: ["/api/otel/"],
});

// The cookie session -- the CSRF token on every write, one refresh and
// retry on an expired access token, and announcing a session that is over
// -- is @slop-factory/auth's SessionClient, shared across apps, and its
// clients are one per SessionMode.
export const {
  session,
  client,
  publicClient,
  credentialClient,
  probeClient,
  unwrap,
  errorFrom,
  raise,
  onSignedOut,
  onTenantChanged,
  sessionWriteHeaders,
} = createApiClients<paths>({
  baseUrl: BASE_URL,
  middleware: [propagateTrace, failureMiddleware(failures, errorCode)],
  fetchFor: sendFor,
  tenantScoped: true,
  clientOptions: { querySerializer: serializeQuery },
  refine,
});

// onSignedOut registers what happens when the session has ended -- expired
// past refreshing, or revoked by a password reset elsewhere. sessionWriteHeaders
// is the session's CSRF header, for a sender that isn't this client: the
// telemetry exporter posts to a route that authenticates the session.
// Both come out of createApiClients above.
export { CSRF_HEADER };

// withTenant names the tenant this page is in on a URL for an EventSource,
// which cannot set the header every other request carries.
export function withTenant(url: string): string {
  const tenant = session.tenantId();
  if (!tenant) return url;
  return `${url}${url.includes("?") ? "&" : "?"}${TENANT_QUERY}=${encodeURIComponent(tenant)}`;
}

// The fetch the consent client goes through: the session's, so a consent
// write carries the CSRF token a signed-in person's needs, and one made on
// a refused credential (answered 403 credential_rejected, never recorded) is
// refreshed and sent again once, rather than recorded twice under two
// identities (go-privacy/README.md, section 3).
export const consentFetch: typeof fetch = (input, init) =>
  sendFor(session, "session")(new Request(input, init));

// setupScriptFrom reads a setup script response: a shell script, not JSON,
// under the filename the server chose.
export async function setupScriptFrom(
  pending: Promise<{ data?: string; error?: unknown; response: Response }>,
): Promise<SetupScript> {
  const { data, error, response } = await pending;
  if (!response.ok) raise(errorFrom(response, error));
  return {
    filename:
      filenameFrom(response.headers.get("Content-Disposition")) ??
      "slop-haus-setup.sh",
    script: data ?? "",
  };
}
