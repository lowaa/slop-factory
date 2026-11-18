// Package authn is the edge of a huma API that decides who a request is
// from and whether it may proceed: every operation declares who may call
// it with exactly one marker (DefaultDeny), signed-in users are resolved
// from a session cookie or a bearer token (UserMiddleware; OptionalUser
// routes carry on anonymous without one), and browser
// writes are checked for cross-site request forgery (BrowserGuard).
//
// An app adds its own markers -- an agent's, an admin's -- to a Policy,
// and its own notion of what a user's request is scoped to (a tenant) to
// UserMiddlewareConfig.
package authn

import "strings"

// BearerToken pulls the credential out of an Authorization header.
func BearerToken(header string) string {
	const prefix = "Bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}
