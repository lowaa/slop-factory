// Activation and password-reset links carry their token in the URL
// *fragment* (`#token=...`), never the query string: a fragment never
// leaves the browser in a Referer header or a server access log, which
// matters because these tokens are single-use and worth protecting the
// same way a password reset link is.
//
// Reads the token once and immediately scrubs it from the address bar, so
// it doesn't linger in browser history either. Only the fragment is
// scrubbed -- the query string is left alone, since a host is free to put
// its own params there (a post-sign-in return path, say) alongside the
// token.
export function readAndClearToken(): string | null {
  const token = new URLSearchParams(window.location.hash.slice(1)).get("token");
  window.history.replaceState(null, "", window.location.pathname + window.location.search);
  return token;
}
