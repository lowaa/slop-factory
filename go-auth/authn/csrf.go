package authn

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/go-auth/audit"
)

// CSRFHeader carries the session's CSRF token (see BrowserGuard) on every
// state-changing request a session cookie authenticates.
const CSRFHeader = "X-CSRF-Token"

// The codes a BrowserGuard refuses with. CodeCSRF tells the app's page its
// session cannot write -- sign in again; CodeCrossOrigin that the request
// did not come from the app's page at all.
const (
	CodeCSRF        = "csrf"
	CodeCrossOrigin = "cross_origin"
)

// CSRFValidator reports whether token is the CSRF token of the browser
// session sessionID names. It is false for a session with no id.
type CSRFValidator func(sessionID, token string) bool

// Recorder records a guard's refusals; *audit.Recorder is one.
type Recorder interface {
	Record(ctx context.Context, action audit.Action, attrs ...any)
}

// BrowserGuard tells the app's own pages' writes from ones another site's
// page makes a visitor's browser send -- cross-site request forgery.
//
// Two checks:
//
//   - Origin. A browser writes where it came from into Origin (or, failing
//     that, Referer) and no page's script can change either, so a write from
//     any other origin is refused. This is what guards the Public writes:
//     sign-in, sign-up and the emailed-link exchanges have no session to
//     check, yet a cross-site form could still sign a victim's browser into
//     an attacker's account (login CSRF) and watch what they do there.
//   - A synchronizer token, for writes a session cookie authenticates: an
//     HMAC of the session's id (go-auth/auth's CSRFTokens), which the page
//     reads from a same-origin response and sends back in CSRFHeader.
//     Another site cannot read that response, so it cannot send the token.
//     This does not rest on the Origin check, nor on SameSite -- a lax
//     cookie still rides a top-level cross-site POST -- nor on the server
//     never granting a CORS preflight.
//
// Neither applies to a request authenticated by an Authorization bearer
// token (an agent's OAuth token, a script's): a browser never attaches one
// by itself, so a forged request cannot carry it.
type BrowserGuard struct {
	origin     string
	validToken CSRFValidator
	events     Recorder
}

// NewBrowserGuard guards the app served at publicBaseURL, whose origin is
// the only one its writes are accepted from. Each refusal is recorded with
// events.
func NewBrowserGuard(publicBaseURL string, validToken CSRFValidator, events Recorder) (*BrowserGuard, error) {
	origin, ok := originOf(publicBaseURL)
	if !ok {
		return nil, fmt.Errorf("authn: public base URL %q has no origin", publicBaseURL)
	}
	if validToken == nil {
		return nil, fmt.Errorf("authn: a browser guard needs a CSRF token validator")
	}
	if events == nil {
		return nil, fmt.Errorf("authn: a browser guard needs an audit recorder")
	}
	return &BrowserGuard{origin: origin, validToken: validToken, events: events}, nil
}

// PublicWrites refuses a state-changing request to a Public operation that
// did not come from the app's origin.
func (g *BrowserGuard) PublicWrites(api huma.API) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		if publicMarker.On(ctx.Operation()) && !isSafeMethod(ctx.Method()) {
			if p := g.checkOrigin(ctx); p != nil {
				writeProblem(api, ctx, p)
				return
			}
		}
		next(ctx)
	}
}

// checkSessionWrite refuses a state-changing request authenticated by who's
// session cookie unless it came from the app's origin and carries the
// session's CSRF token. A session with no id -- issued before ids were --
// has no token, so it cannot write: its owner signs in again.
func (g *BrowserGuard) checkSessionWrite(ctx huma.Context, who *User) *Problem {
	if p := g.checkOrigin(ctx); p != nil {
		return p
	}
	if !g.validToken(who.SessionID, ctx.Header(CSRFHeader)) {
		g.events.Record(ctx.Context(), audit.CSRFRefused,
			"path", ctx.Operation().Path, "http_method", ctx.Method(), "user_id", who.ID, "legacy_session", who.SessionID == "")
		return NewProblem(http.StatusForbidden, CodeCSRF, "this session's CSRF token is missing or wrong; sign in again")
	}
	return nil
}

// checkOrigin refuses a request whose Origin -- or, only when there is no
// Origin, Referer -- is not the app's.
//
// With neither, it refuses too. Every browser
// in use sends Origin on a POST, PUT, PATCH or DELETE, the app's own
// fetches included (a no-referrer policy does not reach a fetch's Origin),
// so a write with neither is not from a browser page at all: a script, which
// can name the origin as easily as it sets any header. Accepting it would
// only matter to a browser old enough to omit both -- exactly the one this
// check must not wave through. An "Origin: null" (a sandboxed frame, a page
// under no-referrer submitting a form) is present and not the app's.
func (g *BrowserGuard) checkOrigin(ctx huma.Context) *Problem {
	raw := ctx.Header("Origin")
	if raw == "" {
		raw = ctx.Header("Referer")
	}
	got, ok := originOf(raw)
	if ok && got == g.origin {
		return nil
	}
	// The origin alone is logged, never raw: a Referer is a whole URL, and
	// its query can carry what the page it came from was showing.
	g.events.Record(ctx.Context(), audit.CrossOriginRefused,
		"path", ctx.Operation().Path, "http_method", ctx.Method(), "origin", got, "had_origin", raw != "")
	return NewProblem(http.StatusForbidden, CodeCrossOrigin, "this request did not come from this site")
}

// originOf is raw's origin as a browser serializes it -- lower-case scheme
// and host, the port only when it is not the scheme's default -- whether raw
// is an Origin header or a whole URL (a Referer, the public base URL).
func originOf(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	switch {
	case scheme != "http" && scheme != "https":
		return "", false
	case (scheme == "http" && port == "80") || (scheme == "https" && port == "443"):
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host, true
}
