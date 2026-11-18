// Package authntest builds authn's middleware pieces for other packages'
// handler tests, which authenticate by bearer token (or not at all) and so
// never reach the guard's checks; authn's own tests exercise those.
package authntest

import (
	"log/slog"

	"github.com/lowaa/slop-factory/go-auth/audit"
	"github.com/lowaa/slop-factory/go-auth/authn"
)

// AppURL is the app the guard from Guard protects.
const AppURL = "https://app.example"

// Guard is a BrowserGuard for AppURL that accepts no CSRF token, so a
// cookie-authenticated write through it is always refused.
func Guard() *authn.BrowserGuard {
	g, err := authn.NewBrowserGuard(AppURL, func(string, string) bool { return false },
		audit.New(&audit.Config{Logger: slog.DiscardHandler}))
	if err != nil {
		// AppURL is a constant with an origin; this cannot happen.
		panic(err)
	}
	return g
}
