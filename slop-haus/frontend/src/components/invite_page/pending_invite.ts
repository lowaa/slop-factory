import { registerPersonalState } from "@/lib/personal_state";

// Split from invite_page.tsx the same way agent_name's provider/hook live
// in separate files (agent_name.tsx / agent_names.ts): a file that exports
// only a component keeps react/only-export-components happy for fast
// refresh, and this one holds everything that isn't a component.

// pendingToken lives in module scope, not React state or localStorage. It
// has to survive an unrelated top-level round trip -- invite_page.tsx
// sending an already-signed-up invitee to /login and app.tsx bringing them
// straight back here once they're signed in (see hasPendingInviteToken
// below) -- but the invite is single-use and short-lived, so it does not
// belong anywhere durable: localStorage would outlive the tab, a failed
// accept, or the invite's own expiry, and there is nothing to clean up
// there once it has served this one page load. A module-level variable is
// exactly as long-lived as the page needs and no longer.
let pendingToken: string | null = null;

// hasPendingInviteToken lets app.tsx's routing send an authenticated user
// back to /invite after a detour through /login, even though the current
// URL by then is "/login" and no longer names the invite at all.
export function hasPendingInviteToken(): boolean {
  return pendingToken !== null;
}

export function clearPendingInvite(): void {
  pendingToken = null;
}

// readToken reads the token from the emailed link's URL fragment (the same
// pattern as @slop-factory/auth's readAndClearToken, for the same reason
// -- a fragment never leaves the browser in a Referer header) on the
// first visit, and scrubs it from the address bar. On a *second* mount with
// no fragment -- the round trip back from /login above -- it falls back to
// the token stashed on the first visit instead of reporting "missing
// token".
export function readToken(): string | null {
  const fromHash = new URLSearchParams(window.location.hash.slice(1)).get(
    "token",
  );
  if (fromHash) {
    window.history.replaceState(null, "", window.location.pathname);
    pendingToken = fromHash;
    return fromHash;
  }
  return pendingToken;
}

registerPersonalState("pending invite", { forget: clearPendingInvite });
