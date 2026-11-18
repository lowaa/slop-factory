import type { Screen } from "./catalog";

// A route's template (never a URL: no ids) to the screen it is. screens.test.ts
// holds this and UNTRACKED_ROUTES to the route tree, so a new route has to be
// placed in one list or the other.
const SCREEN_OF_ROUTE: Record<string, Screen> = {
  "/": "board",
  "/remote-control": "remote_control",
  "/blocked": "blocked",
  "/approvals": "approvals",
  "/steward": "steward",
  "/unreviewed": "unreviewed",
  "/reports": "reports",
  "/compare": "compare",
  "/workspace": "workspace",
  "/workspace/github-app/created": "github_app_setup",
  "/workspace/github-app/installed": "github_app_setup",
  "/workers": "workers",
  "/workers/add-machine": "add_machine",
  "/members": "members",
  "/settings": "tenant_settings",
  "/git": "git",
  "/intake": "intake",
  "/eval-sets": "eval_sets",
  "/tickets/$ticketId": "ticket",
  "/tickets/$ticketId/attempts/$n": "ticket_attempt",
  "/account": "account",
  "/tenants/new": "new_tenant",
  "/help": "help",
  "/help/$slug": "help_page",
  "/legal/$kind": "legal",
  "/legal/$kind/$version": "legal",
};

// Deliberately not screens: the sign-in and account-recovery pages come
// before there is a signed-in person (usage is reported for signed-in users
// only), the OAuth consent pages are part of an agent's sign-in, and the
// redirects are never shown.
export const UNTRACKED_ROUTES: readonly string[] = [
  "/login",
  "/register",
  "/forgot-password",
  "/activate",
  "/reset-password",
  "/invite",
  "/authorize",
  "/device",
  "/agents",
  "/$",
];

export const TRACKED_ROUTES: readonly string[] = Object.keys(SCREEN_OF_ROUTE);

// The router gives a trailing slash to an index route's full path.
function withoutTrailingSlash(path: string): string {
  return path.length > 1 && path.endsWith("/") ? path.slice(0, -1) : path;
}

export function screenOfRoute(fullPath: string | undefined): Screen | null {
  if (fullPath === undefined) return null;
  return SCREEN_OF_ROUTE[withoutTrailingSlash(fullPath)] ?? null;
}
