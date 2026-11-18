package authn

import (
	goauthn "github.com/lowaa/slop-factory/go-auth/authn"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
)

// CSRFHeader carries the session's CSRF token (see BrowserGuard) on every
// state-changing request a session cookie authenticates.
const CSRFHeader = goauthn.CSRFHeader

// The codes a BrowserGuard refuses with. CodeCSRF tells the board its
// session cannot write -- sign in again; CodeCrossOrigin that the request
// did not come from the board at all.
const (
	CodeCSRF        = goauthn.CodeCSRF
	CodeCrossOrigin = goauthn.CodeCrossOrigin
)

// CSRFValidator reports whether token is the CSRF token of the browser
// session sessionID names.
type CSRFValidator = goauthn.CSRFValidator

// BrowserGuard tells the board's own writes from ones another site's page
// makes a visitor's browser send -- cross-site request forgery.
type BrowserGuard = goauthn.BrowserGuard

// NewBrowserGuard guards the board served at publicBaseURL (PUBLIC_BASE_URL),
// whose origin is the only one its writes are accepted from. Each refusal
// is recorded with events.
func NewBrowserGuard(publicBaseURL string, validToken CSRFValidator, events *audit.Recorder) (*BrowserGuard, error) {
	if events == nil {
		// Checked here too: a nil *audit.Recorder is not a nil
		// goauthn.Recorder, so go-auth's own check would miss it.
		return goauthn.NewBrowserGuard(publicBaseURL, validToken, nil)
	}
	return goauthn.NewBrowserGuard(publicBaseURL, validToken, events)
}

// Problem is huma's error body plus a machine-readable code.
type Problem = goauthn.Problem

// NewProblem is a refusal with status, code and a human-readable detail.
func NewProblem(status int, code, detail string) *Problem {
	return goauthn.NewProblem(status, code, detail)
}
