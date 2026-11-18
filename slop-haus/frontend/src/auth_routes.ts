import type { AuthRoutes } from "@slop-factory/auth";
import { RETURN_TO_PARAM, safeRedirectPath } from "@slop-factory/router";

// Where the auth pages live -- routes.tsx declares its routes at these paths.
export const AUTH_ROUTES = {
  login: "/login",
  register: "/register",
  forgotPassword: "/forgot-password",
} as const satisfies AuthRoutes;

// The pages a sign-in can't usefully return to: signed in, they redirect
// to the board anyway.
const SIGN_IN_PAGES = new Set<string>([AUTH_ROUTES.login, AUTH_ROUTES.register, AUTH_ROUTES.forgotPassword]);

// The server's limit on a provider sign-in's ?next= (StartSignInRequest).
// A longer one fails the sign-in outright (a 422) rather than falling back.
// It is not the whole story: the server also drops a next that doesn't fit
// its sealed state cookie (go-auth login.LoginStateCodec.Encode) for "/",
// so a provider sign-in with a path of a kilobyte or more can land on the
// board where a password one would not. That fallback is graceful, and
// mirroring the cookie's sealing here to predict it isn't worth it.
const MAX_RETURN_PATH_LENGTH = 2048;

// signInReturnPath is where a sign-in from /login?redirect=raw lands: raw,
// if it is a local path (safeRedirectPath), not a sign-in page, and short
// enough for the server to take as a provider sign-in's next; else the
// board. Password and provider sign-ins both go through it.
export function signInReturnPath(raw: unknown): string {
  const path = safeRedirectPath(raw);
  const pathname = path.split(/[?#]/, 1)[0];
  if (path.length > MAX_RETURN_PATH_LENGTH || SIGN_IN_PAGES.has(pathname)) return "/";
  return path;
}

// providerReturnPath is where a provider sign-in started here comes back
// to: the login page's own return path, or else this page (the admin
// bundle's /admin/, say) unless it is a sign-in page. The server re-checks
// it is local. The provider round trip is a full page load, so the
// server's next is the only way a return path can come back from it.
export function providerReturnPath(): string | undefined {
  const { pathname, search } = window.location;
  if (pathname === AUTH_ROUTES.login) {
    const path = signInReturnPath(new URLSearchParams(search).get(RETURN_TO_PARAM));
    return path === "/" ? undefined : path;
  }
  return SIGN_IN_PAGES.has(pathname) ? undefined : pathname;
}
