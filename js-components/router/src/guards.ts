import { redirect } from "@tanstack/react-router";
import { loginPathReturningTo } from "./return_to";

// For beforeLoad: stop loading this route and go to path instead. It
// replaces the current history entry rather than pushing, because a guard
// is correcting the URL, not following a click -- Back should not return to
// a page the guard would only redirect away from again. (TanStack replaces
// on every beforeLoad redirect today; this says so rather than relying on
// it.)
//
// path is a plain local path rather than a typed route, so a guard can
// compute where to go (a pending flow's page, say) at runtime. A guard must
// not redirect to the page it is guarding: that loops.
export function redirectTo(path: string): never {
  throw redirect({ href: path, replace: true });
}

export interface RequireAuthenticatedOptions {
  authenticated: boolean;
  loginPath: string;
  // Where to come back to after signing in -- the guarded location's href,
  // typically -- carried on the login page as ?redirect= (see return_to.ts).
  // Left out, the login page is a plain one.
  returnTo?: string;
}

// requireAuthenticated is the beforeLoad guard for a route only a signed-in
// user may see: anyone else goes to the login page.
export function requireAuthenticated({ authenticated, loginPath, returnTo }: RequireAuthenticatedOptions): void {
  if (!authenticated) redirectTo(loginPathReturningTo(loginPath, returnTo));
}
