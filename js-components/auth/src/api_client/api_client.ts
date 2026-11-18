import createClient, { type Client, type ClientOptions, type Middleware } from "openapi-fetch";
import { SessionClient, type SessionMode } from "../session/session_client";

// The plumbing every app's API module repeats on top of SessionClient and
// openapi-fetch: one error type, one reading of huma's error envelope, one
// {data, error, response} -> value-or-throw, and one openapi-fetch client
// per SessionMode. An app's own module keeps its endpoint methods.

export class ApiError extends Error {
  status: number;
  // The server's machine-readable reason, when it gave one
  // (invalid_credentials, no_account, ...), for the auth pages to explain
  // in their own words.
  code?: string;

  constructor(status: number, message: string, code?: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

export const BULK_REFUSED = "bulk_refused";

// A bulk action's 404 or 409: nothing changed, and refusals names every
// item that stopped it and why. A subclass of ApiError, so a caller that
// doesn't need the list still catches it as one.
export class BulkRefusedError<Refusal = unknown> extends ApiError {
  refusals: Refusal[];

  constructor(status: number, message: string, refusals: Refusal[]) {
    super(status, message, BULK_REFUSED);
    this.name = "BulkRefusedError";
    this.refusals = refusals;
  }
}

// What an error response said, read from huma's error envelope. fields is
// the whole parsed body, for an app's own error types (see
// ApiClientsOptions.refine).
export interface ErrorEnvelope {
  status: number;
  detail: string;
  code?: string;
  fields: Record<string, unknown>;
}

// errorCode reads the server's machine-readable code out of a parsed error
// body: what the failure and consent middleware are handed.
export function errorCode(error: unknown): string | undefined {
  const code = error && typeof error === "object" ? (error as Record<string, unknown>).code : undefined;
  return typeof code === "string" ? code : undefined;
}

// readEnvelope reads huma's standard error envelope's "detail" (and the
// auth routes' "code" beside it), falling back to the response's own status
// line when the body isn't JSON or has no such field -- e.g. a plain-text
// 502 from a proxy in front of a down backend. openapi-fetch has already
// read the body: error is the parsed JSON, or the raw text when it wasn't.
function readEnvelope(response: Response, error: unknown): ErrorEnvelope {
  const fields = error && typeof error === "object" ? (error as Record<string, unknown>) : {};
  const detail =
    typeof fields.detail === "string"
      ? fields.detail
      : response.statusText || `request failed with status ${response.status}`;
  return { status: response.status, detail, code: errorCode(error), fields };
}

type Result<T> = { data?: T; error?: unknown; response: Response };

export interface ApiClientsOptions {
  // "" for same-origin "/api/..." requests.
  baseUrl: string;
  // Defaults to `${baseUrl}/api/auth/refresh`.
  refreshUrl?: string;
  // Middleware for every client, in order. Their onResponse runs in
  // reverse, so a middleware that tags the request (a trace id) goes before
  // one that reads the tag back (failure reporting).
  middleware?: Middleware[];
  // The fetch each client sends through for a mode. Defaults to the
  // session's own (SessionClient.fetchFor).
  fetchFor?: (session: SessionClient, mode: SessionMode) => (request: Request) => Promise<Response>;
  // Whether the app has tenants; see SessionClientOptions.tenantScoped.
  tenantScoped?: boolean;
  // Passed to every openapi-fetch client (a querySerializer, say).
  clientOptions?: Omit<ClientOptions, "baseUrl" | "fetch">;
  // Lets an app turn an envelope into its own ApiError subclass: return
  // undefined to leave it to the default.
  refine?: (envelope: ErrorEnvelope) => ApiError | undefined;
}

export type ApiErrorListener = (error: { status: number; code?: string }) => void;

// createApiClients builds an app's session and its openapi-fetch clients,
// one per SessionMode: what a 401 means differs by route (see SessionMode),
// and openapi-fetch takes its fetch per client.
export function createApiClients<Paths extends {}>(options: ApiClientsOptions) {
  const { baseUrl } = options;
  const session = new SessionClient({
    refreshUrl: options.refreshUrl ?? `${baseUrl}/api/auth/refresh`,
    tenantScoped: options.tenantScoped,
  });
  const errorListeners = new Set<ApiErrorListener>();

  const clientFor = (mode: SessionMode): Client<Paths> => {
    const fetchFor = options.fetchFor ?? ((s, m) => s.fetchFor(m));
    const created = createClient<Paths>({ ...options.clientOptions, baseUrl, fetch: fetchFor(session, mode) });
    if (options.middleware?.length) created.use(...options.middleware);
    return created;
  };

  // onApiError registers who hears of an ApiError as it is raised -- only
  // its status and code, never its message (analytics listens). Returns an
  // unsubscribe function.
  function onApiError(listener: ApiErrorListener): () => void {
    errorListeners.add(listener);
    return () => errorListeners.delete(listener);
  }

  function raise(error: ApiError): never {
    for (const listener of [...errorListeners]) {
      try {
        listener({ status: error.status, code: error.code });
      } catch {
        // One listener's failure must not change the error the caller gets.
      }
    }
    throw error;
  }

  function errorFrom(response: Response, error: unknown): ApiError {
    const envelope = readEnvelope(response, error);
    const refined = options.refine?.(envelope);
    if (refined) return refined;
    // Recognised by its code rather than threading a per-route error type
    // through every call.
    const refusals = envelope.fields.refusals;
    if (envelope.code === BULK_REFUSED && Array.isArray(refusals)) {
      return new BulkRefusedError(envelope.status, envelope.detail, refusals);
    }
    return new ApiError(envelope.status, envelope.detail, envelope.code);
  }

  // unwrap turns openapi-fetch's {data, error, response} -- never thrown by
  // openapi-fetch itself -- into the value or a raised ApiError. It goes by
  // response.ok rather than by whether error is set: openapi-fetch leaves
  // error undefined for an empty error body.
  function unwrap<T>(result: Result<T>): T;
  function unwrap<T>(result: Promise<Result<T>>): Promise<T>;
  function unwrap<T>(result: Result<T> | Promise<Result<T>>): T | Promise<T> {
    const read = ({ data, error, response }: Result<T>): T => {
      if (!response.ok) raise(errorFrom(response, error));
      return data as T;
    };
    return result instanceof Promise ? result.then(read) : read(result);
  }

  return {
    session,
    // Signed-in requests: an expired access token is refreshed and the
    // request retried once; a session that is over is announced.
    client: clientFor("session"),
    // Routes that come before a session exists: a 401 is the answer (a
    // wrong password), never an expired session to refresh.
    publicClient: clientFor("public"),
    // Signed-in requests that carry a password (a step-up): a 401 saying it
    // was wrong is the answer, not an expired session to refresh or sign
    // out of.
    credentialClient: clientFor("credential"),
    // "Is anyone signed in?" on first load: a refresh is tried, but a final
    // 401 just means no.
    probeClient: clientFor("probe"),
    unwrap,
    // For a caller that needs more of the response than unwrap gives
    // (a header): raise(errorFrom(response, error)) on !response.ok.
    errorFrom,
    raise,
    onApiError,
    // onSignedOut registers what happens when the session has ended --
    // expired past refreshing. Returns an unsubscribe function.
    onSignedOut: (listener: () => void) => session.onSignedOut(listener),
    // onTenantChanged registers what happens when the session has moved to
    // another tenant than this page's, with the tenant this page was in.
    onTenantChanged: (listener: (was: string) => void) => session.onTenantChanged(listener),
    // The headers a write the session authenticates must carry, for a
    // sender that isn't one of these clients.
    sessionWriteHeaders: (): Record<string, string> => session.writeHeaders(),
  };
}
