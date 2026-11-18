// Anything parsed against this origin that comes out on a different one
// was not a local path.
const PROBE_ORIGIN = "http://safe-redirect.invalid";

// Control characters (tab, newline and the rest), which browsers strip
// from URLs before parsing -- "/\t/evil.example" would become
// "//evil.example".
// eslint-disable-next-line no-control-regex
const CONTROL_CHARS = /[\u0000-\u001f\u007f]/;

// A "%" not followed by two hex digits, anywhere in the path, query or
// fragment. Browsers pass it through, but the server's check (go-auth
// login.LocalRedirect) refuses it and would turn the path into "/" --
// rejected here too, so that a path this accepts reaches a provider
// sign-in's ?next= intact.
const MALFORMED_ESCAPE = /%(?![0-9a-fA-F]{2})/;

// safeRedirectPath returns raw if it is a path on this site, else fallback.
// It is for any path that arrives from outside the app -- a `?redirect=`
// param, say -- before the app navigates to it, so that a crafted link
// cannot send someone to another site after they sign in (an open
// redirect). Only a single leading slash is accepted: "//host" and "/\host"
// are protocol-relative to browsers, a scheme ("javascript:", "https:")
// leaves the site, and backslashes are rejected outright because browsers
// treat them as slashes.
//
// What it returns is the path as the URL parser normalises it ("/a/../b"
// is "/b"), so the checks that matter are made again on that: dot segments
// can collapse into a leading "//" ("/.//evil.example" is
// "//evil.example").
//
// It accepts a subset of what the server's LocalRedirect (go-auth/login)
// accepts, and a path both accept leads to the same place. The cases in
// go-auth/login/testdata/local_redirect_cases.json drive both functions'
// tests, and this one's checks that promise on every case.
export function safeRedirectPath(raw: unknown, fallback = "/"): string {
  if (typeof raw !== "string") return fallback;
  if (!raw.startsWith("/") || raw.startsWith("//")) return fallback;
  if (raw.includes("\\") || CONTROL_CHARS.test(raw)) return fallback;

  let url: URL;
  try {
    url = new URL(raw, PROBE_ORIGIN);
  } catch {
    return fallback;
  }
  if (url.origin !== PROBE_ORIGIN) return fallback;
  const path = `${url.pathname}${url.search}${url.hash}`;
  if (path.startsWith("//") || MALFORMED_ESCAPE.test(path)) return fallback;
  return path;
}
