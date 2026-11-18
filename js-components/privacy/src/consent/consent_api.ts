import {
  ConsentApiError,
  type ConsentApi,
  type ConsentErrorCode,
  type ConsentState,
  type Decision,
  type DecisionSource,
  type PurposeKind,
  type PurposeState,
} from "./types";

export const DEFAULT_CONSENT_PATH = "/api/privacy/consent";

// Set by go-auth on a request whose session credential was refused but which
// the route carried on with as anonymous. Mirrors @slop-factory/auth's
// session client: this package does not depend on it.
const CREDENTIAL_REJECTED_HEADER = "X-Auth-Credential";

const KNOWN_CODES: ReadonlySet<string> = new Set<ConsentErrorCode>([
  "credential_rejected",
  "consent_changed",
  "document_changed",
  "gpc_active",
  "sign_in_required",
  "invalid_purpose",
  "necessary_purpose",
  "duplicate_purpose",
]);

export interface FetchConsentApiOptions {
  basePath?: string;
  // The fetch every request goes through. In an app that signs people in
  // with go-auth cookie sessions this is the session client's
  // (`session.fetchFor("session")` adapted to a URL, or its `send`), which
  // adds the CSRF token to the write -- the consent PUT is refused without
  // it for a signed-in person -- and already refreshes and retries.
  fetch?: typeof fetch;
  // Trades the refresh cookie for a new access cookie; true when it worked.
  // Only for an app whose fetch does not refresh by itself. A consent write
  // on a refused credential is answered 403 credential_rejected and never
  // recorded (go-privacy/README.md §3), so the way through is to refresh and
  // send it again, exactly once.
  refreshSession?: () => Promise<boolean>;
}

type Json = Record<string, unknown>;

function isRecord(v: unknown): v is Json {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function parsePurpose(v: unknown): PurposeState | null {
  if (!isRecord(v) || typeof v.id !== "string") return null;
  const kind: PurposeKind | null =
    v.kind === "cookie_category" || v.kind === "processing" ? v.kind : null;
  if (kind === null) return null;
  return {
    id: v.id,
    kind,
    necessary: v.necessary === true,
    granted: v.granted === true,
    needsDecision: v.needs_decision === true,
    gpcLocked: v.gpc_locked === true,
    documentKind: typeof v.document_kind === "string" ? v.document_kind : null,
    documentVersion: typeof v.document_version === "string" ? v.document_version : null,
    basedOn: typeof v.based_on === "number" ? v.based_on : 0,
  };
}

// A malformed purpose is dropped rather than guessed at: the client shows
// only what the server reported, so a purpose it can't read is not offered
// (and so is never granted).
export function parseConsentState(body: unknown): ConsentState {
  if (!isRecord(body) || !Array.isArray(body.purposes)) {
    throw new ConsentApiError("unknown", 200);
  }
  return {
    purposes: body.purposes.map(parsePurpose).filter((p): p is PurposeState => p !== null),
    signedIn: body.signed_in === true,
    gpcHonoured: body.gpc === true,
  };
}

async function errorCodeOf(res: Response): Promise<ConsentErrorCode> {
  try {
    const body: unknown = await res.clone().json();
    if (isRecord(body) && typeof body.code === "string" && KNOWN_CODES.has(body.code)) {
      return body.code as ConsentErrorCode;
    }
  } catch {
    // Not a problem body; the header may still say what happened.
  }
  return res.headers.get(CREDENTIAL_REJECTED_HEADER) === "rejected" && res.status === 403
    ? "credential_rejected"
    : "unknown";
}

function credentialRejected(res: Response): boolean {
  return res.headers.get(CREDENTIAL_REJECTED_HEADER) === "rejected";
}

export function createFetchConsentApi({
  basePath = DEFAULT_CONSENT_PATH,
  fetch: fetchFn,
  refreshSession,
}: FetchConsentApiOptions = {}): ConsentApi {
  async function send(init: RequestInit): Promise<Response> {
    try {
      return await (fetchFn ?? globalThis.fetch)(basePath, {
        ...init,
        credentials: "same-origin",
        cache: "no-store",
      });
    } catch {
      throw new ConsentApiError("network", 0);
    }
  }

  // At most one refresh and one resend. A second refusal is the answer:
  // looping would hammer the refresh route for a session that is gone.
  async function sendWithRefresh(init: RequestInit): Promise<Response> {
    const first = await send(init);
    if (!credentialRejected(first) || !refreshSession) return first;
    let refreshed = false;
    try {
      refreshed = await refreshSession();
    } catch {
      refreshed = false;
    }
    return refreshed ? send(init) : first;
  }

  async function stateOf(res: Response): Promise<ConsentState> {
    if (!res.ok) throw new ConsentApiError(await errorCodeOf(res), res.status);
    return parseConsentState(await res.json());
  }

  return {
    get: async () => stateOf(await sendWithRefresh({ method: "GET", headers: { Accept: "application/json" } })),
    put: async (source: DecisionSource, decisions: readonly Decision[], current: ConsentState) => {
      const byId = new Map(current.purposes.map((p) => [p.id, p]));
      const body = {
        source,
        decisions: decisions.map((d) => {
          const p = byId.get(d.purpose);
          return {
            purpose: d.purpose,
            granted: d.granted,
            document_version: p?.documentVersion ?? "",
            based_on: p?.basedOn ?? 0,
          };
        }),
      };
      return stateOf(
        await sendWithRefresh({
          method: "PUT",
          headers: { Accept: "application/json", "Content-Type": "application/json" },
          body: JSON.stringify(body),
        }),
      );
    },
  };
}
