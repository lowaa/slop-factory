import { safeRedirectPath } from "./safe_redirect_path";
import { optionalString } from "./search";

// The login page's search param naming where to go once signed in:
// /login?redirect=/tickets/42.
export const RETURN_TO_PARAM = "redirect";

// returnToSearch is the login route's validateSearch fragment for the
// return path. It is only a string here: whoever acts on it must check it
// with safeRedirectPath first, since anyone can put anything in a link.
export function returnToSearch(raw: Record<string, unknown>): { redirect?: string } {
  return { redirect: optionalString(raw, RETURN_TO_PARAM) };
}

// Only used to parse a local path with URL, which wants an origin.
const LOCAL_ORIGIN = "http://login-path.invalid";

// loginPathReturningTo is loginPath carrying returnTo as its return path,
// alongside any search loginPath already has. The site root is left off --
// it is where a sign-in lands anyway, so a plain visit keeps a plain login
// URL -- as is anything safeRedirectPath would refuse.
export function loginPathReturningTo(loginPath: string, returnTo: string | undefined): string {
  const path = safeRedirectPath(returnTo, "/");
  if (path === "/") return loginPath;
  const url = new URL(loginPath, LOCAL_ORIGIN);
  url.searchParams.set(RETURN_TO_PARAM, path);
  return `${url.pathname}${url.search}${url.hash}`;
}
