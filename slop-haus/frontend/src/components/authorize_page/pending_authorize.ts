import { registerPersonalState } from "@/lib/personal_state";

// Split from authorize_page.tsx the same way invite_page's pending token
// lives in pending_invite.ts (see that file's doc comment for the full
// reasoning): module-scope state, not React state or localStorage, because
// it only has to survive one round trip through /login -- which replaces
// the URL (app.tsx's usePathname's redirect) and loses the "?request=..."
// query string that got the visitor to /authorize in the first place.

let pendingRequestId: string | null = null;

// hasPendingAuthorizeRequest lets app.tsx's screenFor send a freshly
// authenticated user back to /authorize even though the current URL by
// then is "/login" and no longer names the request at all.
export function hasPendingAuthorizeRequest(): boolean {
  return pendingRequestId !== null;
}

export function clearPendingAuthorizeRequest(): void {
  pendingRequestId = null;
}

// captureAuthorizeRequestId reads "request" from the URL's query string on
// the first visit, stashing it for the return trip -- same shape as
// pending_invite.ts's readToken, but from the query string (this is a
// public, non-sensitive request id, and it's what oauth.go's Authorize
// itself builds the URL with: `/authorize?request=<uuid>`) rather than the
// fragment an emailed link uses. On a later call with no "request" in the
// query (the return leg, now on /login, or a re-render of /authorize
// itself) it falls back to whatever was stashed.
export function captureAuthorizeRequestId(): string | null {
  const fromQuery = new URLSearchParams(window.location.search).get("request");
  if (fromQuery) pendingRequestId = fromQuery;
  return pendingRequestId;
}

registerPersonalState("pending authorize request", { forget: clearPendingAuthorizeRequest });
