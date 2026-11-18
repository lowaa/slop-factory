// The browser half of a cookie session, shared by every app whose server
// signs people in with httpOnly session cookies and go-auth's BrowserGuard
// (see go-auth/authn/csrf.go): the session's CSRF token on every write, one
// refresh-and-retry when the access token has expired, and telling the app
// when the session is over. No token of the session itself ever passes
// through here -- the cookies are httpOnly -- only the CSRF token, which
// the server hands the page in a same-origin response.

// CSRF_HEADER carries the session's CSRF token on every write. The session
// is a cookie, which a browser attaches to a cross-site request by itself;
// the token is what such a request cannot carry, since only this origin can
// read the response it comes in.
export const CSRF_HEADER = "X-CSRF-Token";

// TENANT_HEADER carries the tenant the page loaded in, on every request. The
// session cookies are shared by every tab of the browser, so a tenant switch
// in one tab moves them all: without this a tab left open on the old tenant
// would go on acting on the new one. The server refuses a mismatch
// (TENANT_CHANGED), which is the guarantee; the broadcast below is only the
// quick path to telling the other tabs.
export const TENANT_HEADER = "X-Tenant-ID";

// TENANT_QUERY is TENANT_HEADER for a request that cannot set headers, an
// EventSource.
export const TENANT_QUERY = "tenant";

// TENANT_CHANGED is the code of that refusal.
export const TENANT_CHANGED = "tenant_changed";

// CREDENTIAL_REJECTED_HEADER is how a route that never answers 401 (go-auth's
// OptionalUser: with a refused credential it carries on as anonymous) says
// the request's session credential was refused. Without this the page would
// take an expired session for a visitor.
//
// The retry replays the request, so it is safe only because go-auth tells a
// handler to do nothing for such a request (authn.CredentialRejected) --
// a write would otherwise run once as a visitor and again as the person.
export const CREDENTIAL_REJECTED_HEADER = "X-Auth-Credential";
const CREDENTIAL_REJECTED_VALUE = "rejected";

// SessionMode is what a 401 (or a rejected-credential header) means for a request:
//   "session"    -- the access token expired; refresh, retry once, and if
//                   that fails too the session is over and the app is told.
//   "probe"      -- the same refresh and retry, but a final 401 is just the
//                   answer: on first load there was nothing to sign out of.
//   "public"     -- no retry at all: a 401 from sign-in is a wrong password,
//                   not an expired session.
//   "credential" -- a signed-in request that carries a password (a step-up):
//                   a 401 saying the password was wrong is the answer, and
//                   anything else is "session" -- so a mistyped password
//                   neither signs the person out nor is sent twice.
export type SessionMode = "session" | "probe" | "public" | "credential";

// Only "session" and "probe" read the rejected-credential header: they are
// the modes for requests that a refresh can put right, and a "credential" or
// "public" request has its own meaning for a refusal.

// SessionBody is a sign-in's (or a session probe's) answer: the profile,
// with the session's CSRF token beside it.
export interface SessionBody {
  csrf_token: string;
  // The tenant the session is in, for an app that has tenants.
  tenant_id?: string;
}

export interface SessionClientOptions {
  // The URL that trades the refresh cookie for a new access cookie. The
  // refresh cookie is scoped to its path, so this is the only request that
  // ever carries it.
  refreshUrl: string;
  // Defaults to the global fetch, looked up at call time so a test can
  // stub it.
  fetch?: typeof fetch;
  // How a tab tells the others it switched tenant. Defaults to the
  // browser's BroadcastChannel on "session_tenant" where there is one.
  tenantChannel?: { name: string; create: (name: string) => BroadcastChannel } | null;
  // Whether the app has tenants: the page's tenant is then sent on every
  // request, and a switch is told to the other tabs. Off, tenant_id on a
  // session body is ignored.
  tenantScoped?: boolean;
}

// isWrite is whether a request changes something -- the methods the server
// wants the CSRF token on. Reads never carry it, so the token only ever
// goes where it is needed.
function isWrite(method: string | undefined): boolean {
  const m = (method ?? "GET").toUpperCase();
  return m !== "GET" && m !== "HEAD" && m !== "OPTIONS";
}

// hasCode is whether response is a refusal with the machine-readable code.
export async function hasCode(response: Response, code: string): Promise<boolean> {
  try {
    const body: unknown = await response.clone().json();
    return !!body && typeof body === "object" && (body as Record<string, unknown>).code === code;
  } catch {
    return false;
  }
}

// isRefused is whether response says the session's credential was refused:
// a 401, or in the two modes that retry on an OptionalUser route, the
// rejected-credential header on an otherwise ordinary answer.
function isRefused(response: Response, mode: SessionMode): boolean {
  if (mode === "public") return false;
  if (response.status === 401) return true;
  return (
    (mode === "session" || mode === "probe") &&
    response.headers.get(CREDENTIAL_REJECTED_HEADER) === CREDENTIAL_REJECTED_VALUE
  );
}

export class SessionClient {
  // The signed-in session's CSRF token, from the profile every sign-in and
  // session probe answers with. It stays the same through a refresh -- the
  // session is the same one -- and changes only with a new sign-in.
  private csrfToken: string | null = null;
  private signedOutListener: (() => void) | null = null;
  // The tenant this page loaded in (the session's, as of the last sign-in,
  // probe or switch made from this page), and who hears that it has changed.
  private tenant: string | null = null;
  private tenantChangedListener: ((was: string) => void) | null = null;
  private tenantChangedAnnounced = false;
  private readonly tenantChannel: BroadcastChannel | null = null;
  private readonly tenantScoped: boolean;
  // Counts sign-ins. A request from the old session can still be failing
  // after the person has signed in again -- it started before -- and its
  // "session over" must not sign the new one out.
  private signIns = 0;
  // Shared, so a burst of requests failing together on an expired access
  // token makes one refresh between them rather than one each.
  // Tagged with the sign-in count it began under: a request from a newer
  // session must not ride a refresh begun by an older one, whose answer is
  // about the old session's cookies.
  private refreshing: { generation: number; result: Promise<boolean> } | null = null;
  private readonly refreshUrl: string;
  private readonly fetchImpl?: typeof fetch;

  constructor(options: SessionClientOptions) {
    this.refreshUrl = options.refreshUrl;
    this.fetchImpl = options.fetch;
    this.tenantScoped = options.tenantScoped ?? false;
    const channel =
      !this.tenantScoped
        ? null
        : options.tenantChannel !== undefined
        ? options.tenantChannel
        : typeof BroadcastChannel !== "undefined"
          ? { name: "session_tenant", create: (n: string) => new BroadcastChannel(n) }
          : null;
    if (channel) {
      this.tenantChannel = channel.create(channel.name);
      this.tenantChannel.onmessage = (event: MessageEvent) => {
        const data: unknown = event.data;
        const message = data && typeof data === "object" ? (data as Record<string, unknown>) : {};
        // Only a switch away from the tenant this page is in: a message
        // delayed past this page's own sign-in or switch is about a session
        // that is no longer the one it holds.
        if (typeof message.from === "string" && message.from === this.tenant) this.announceTenantChanged();
      };
    }
  }

  private doFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
    return (this.fetchImpl ?? globalThis.fetch)(input, init);
  }

  // remember takes the CSRF token off a session body, so it never reaches
  // component state, and keeps it for the writes that follow.
  remember<T extends SessionBody>(body: T): Omit<T, "csrf_token"> {
    const { csrf_token, ...rest } = body;
    this.csrfToken = csrf_token || null;
    this.tenant = this.tenantScoped ? (body.tenant_id ?? null) : null;
    return rest;
  }

  // tenantSwitched is remember for a session this page itself moved to
  // another tenant, which it tells the other tabs about: theirs is a session
  // that has just changed under them.
  tenantSwitched<T extends SessionBody>(body: T): Omit<T, "csrf_token"> {
    const from = this.tenant;
    const rest = this.remember(body);
    if (from && body.tenant_id && body.tenant_id !== from) this.tenantChannel?.postMessage({ from, tenant_id: body.tenant_id });
    return rest;
  }

  // tenantId is the tenant this page loaded in, null before a sign-in or
  // probe has said.
  tenantId(): string | null {
    return this.tenant;
  }

  // onTenantChanged registers what happens when the session has moved to
  // another tenant than this page's, with the tenant this page was in. Told
  // once: the page is replaced by a notice and reloads from there.
  onTenantChanged(listener: (was: string) => void): () => void {
    this.tenantChangedListener = listener;
    return () => {
      if (this.tenantChangedListener === listener) this.tenantChangedListener = null;
    };
  }

  private announceTenantChanged(): void {
    if (this.tenantChangedAnnounced || this.tenant === null) return;
    this.tenantChangedAnnounced = true;
    this.tenantChangedListener?.(this.tenant);
  }

  // signedIn is remember for a new session -- a sign-in, an accepted invite
  // -- after which a late failure from the previous session is ignored.
  signedIn<T extends SessionBody>(body: T): Omit<T, "csrf_token"> {
    this.signIns++;
    return this.remember(body);
  }

  // writeHeaders are the headers a write the session authenticates must
  // carry -- for a sender that isn't send() (a telemetry exporter posting to
  // an OptionalUser route, whose cookie-authenticated writes are checked
  // like any other). Empty with no session.
  writeHeaders(): Record<string, string> {
    return { ...(this.csrfToken ? { [CSRF_HEADER]: this.csrfToken } : {}), ...this.tenantHeaders() };
  }

  private tenantHeaders(): Record<string, string> {
    return this.tenant ? { [TENANT_HEADER]: this.tenant } : {};
  }

  // forget drops the CSRF token -- on sign-out, or a session that can't
  // write.
  forget(): void {
    this.csrfToken = null;
    this.tenant = null;
    this.tenantChangedAnnounced = false;
  }

  // onSignedOut registers what happens when the session has ended --
  // expired past refreshing. Announced rather than handled here: this client
  // can't show a sign-in page, and the app can. Returns an unsubscribe.
  onSignedOut(listener: () => void): () => void {
    this.signedOutListener = listener;
    return () => {
      if (this.signedOutListener === listener) this.signedOutListener = null;
    };
  }

  // generation is the sign-in count now, for announceSignedOut: something
  // long-running (a stream) takes it when it starts.
  generation(): number {
    return this.signIns;
  }

  // announceSignedOut tells the app the session that was current at
  // generation is over -- unless there has been a sign-in since.
  announceSignedOut(generation: number): void {
    if (generation !== this.signIns) return;
    this.csrfToken = null;
    this.tenant = null;
    this.tenantChangedAnnounced = false;
    this.signedOutListener?.();
  }

  // refresh trades the refresh cookie for a new access cookie. No CSRF
  // token: the refresh route is public (the access token it replaces may be
  // gone), so the server holds it to this origin instead.
  refresh(): Promise<boolean> {
    if (this.refreshing?.generation === this.signIns) return this.refreshing.result;
    const entry = {
      generation: this.signIns,
      result: this.doFetch(this.refreshUrl, { method: "POST" })
        .then((response) => response.ok)
        .catch(() => false)
        .finally(() => {
          if (this.refreshing === entry) this.refreshing = null;
        }),
    };
    this.refreshing = entry;
    return entry.result;
  }

  // send is fetch with the session's CSRF token on a write and the 401
  // handling SessionMode describes.
  send(url: string, init: RequestInit = {}, mode: SessionMode = "session"): Promise<Response> {
    return this.withSession(
      (csrf) =>
        this.doFetch(url, {
          ...init,
          headers: {
            ...(isWrite(init.method) && csrf ? { [CSRF_HEADER]: csrf } : {}),
            ...this.tenantHeaders(),
            ...init.headers,
          },
        }),
      mode,
    );
  }

  // sendRequest is send for a ready-made Request -- what a generated
  // client's custom fetch receives. Each attempt sends a clone, so the body
  // is still there for the retry.
  sendRequest(request: Request, mode: SessionMode = "session"): Promise<Response> {
    return this.withSession((csrf) => {
      const attempt = request.clone();
      if (isWrite(attempt.method) && csrf) attempt.headers.set(CSRF_HEADER, csrf);
      for (const [name, value] of Object.entries(this.tenantHeaders())) attempt.headers.set(name, value);
      return this.doFetch(attempt);
    }, mode);
  }

  // fetchFor is a fetch function whose every request goes through the
  // session in mode, for a client library that takes its own fetch.
  fetchFor(mode: SessionMode = "session"): (request: Request) => Promise<Response> {
    return (request) => this.sendRequest(request, mode);
  }

  // A 403 "csrf" on a signed-in request means this session cannot write:
  // another tab has since signed in and this page's token is for a session
  // the browser no longer holds, or the session predates CSRF tokens.
  // Either way it is the session being over -- the same sign-in-again path
  // as a 401 no refresh fixes -- and never retried, so a write meant for one
  // account is never replayed as another.
  private async withSession(attempt: (csrf: string | null) => Promise<Response>, mode: SessionMode): Promise<Response> {
    const generation = this.signIns;
    const startedIn = this.tenant;
    const sessionOver = async (response: Response): Promise<boolean> =>
      mode !== "public" && response.status === 403 && (await hasCode(response, "csrf"));

    // The page is in a tenant the session has left: nothing ran, and
    // replaying it would run it in the tenant the person is in now.
    const tenantChanged = async (response: Response): Promise<boolean> => {
      if (mode === "public" || response.status !== 409 || !(await hasCode(response, TENANT_CHANGED))) return false;
      // Refused for the tenant it was sent in; if this page has moved since
      // (its own switch), the refusal is old news and not about this page.
      if (startedIn === this.tenant) this.announceTenantChanged();
      return true;
    };

    let response = await attempt(this.csrfToken);
    if (await tenantChanged(response)) return response;
    if (await sessionOver(response)) {
      this.announceSignedOut(generation);
      return response;
    }
    if (!isRefused(response, mode)) return response;
    if (mode === "credential" && (await hasCode(response, "invalid_credentials"))) return response;

    if (await this.refresh()) {
      // Someone signed in while the refresh was in flight. The request was
      // issued by the old session; replaying it now would run it under the new
      // account's cookies and CSRF token. Hand back the refusal instead -- the
      // caller sees a failed request, and the old session's verdict is not
      // announced over the new one.
      if (generation !== this.signIns || startedIn !== this.tenant) return response;
      response = await attempt(this.csrfToken);
      if (await tenantChanged(response)) return response;
      // The token is only checked once the access token is good, so the
      // retry is where an expired session's write first meets it.
      if (await sessionOver(response)) {
        this.announceSignedOut(generation);
        return response;
      }
      if (!isRefused(response, mode)) return response;
      // The retry can also be a wrong credential, not just an expired
      // session -- an access token that expired between the two attempts.
      if (mode === "credential" && (await hasCode(response, "invalid_credentials"))) return response;
    }
    if (mode === "session" || mode === "credential") this.announceSignedOut(generation);
    return response;
  }
}
