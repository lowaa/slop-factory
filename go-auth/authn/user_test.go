package authn_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/audit"
	"github.com/lowaa/slop-factory/go-auth/audit/audittest"
	"github.com/lowaa/slop-factory/go-auth/authn"
)

const (
	// goodToken and otherSessionToken are two sessions of the same person
	// -- two browsers -- and legacyToken one issued before session ids.
	goodToken         = "good-token"
	otherSessionToken = "other-session-token"
	legacyToken       = "legacy-token"
	// otherPersonToken is a different person's session.
	otherPersonToken = "other-person-token"

	board = "https://app.example"

	sessionCookie = "app_session"
)

var (
	signedIn      = &authn.User{ID: uuid.New(), TenantID: uuid.New(), SessionID: "session-a"}
	otherSession  = &authn.User{ID: signedIn.ID, TenantID: signedIn.TenantID, SessionID: "session-b"}
	legacy        = &authn.User{ID: signedIn.ID, TenantID: signedIn.TenantID}
	anotherPerson = &authn.User{ID: uuid.New(), TenantID: uuid.New(), SessionID: "session-c"}
)

func verify(_ context.Context, token string) (*authn.User, error) {
	switch token {
	case goodToken:
		return signedIn, nil
	case otherSessionToken:
		return otherSession, nil
	case legacyToken:
		return legacy, nil
	case otherPersonToken:
		return anotherPerson, nil
	}
	return nil, errors.New("bad token")
}

// csrfOf stands in for go-auth's CSRFTokens: a token per session id, and
// none for a session without one.
func csrfOf(sessionID string) string { return "csrf-of-" + sessionID }

func validCSRF(sessionID, token string) bool {
	return sessionID != "" && token == csrfOf(sessionID)
}

// testGuard guards the board at board, with the default port spelled out
// the way PUBLIC_BASE_URL may spell it and a browser never does.
func testGuard() *authn.BrowserGuard {
	return testGuardRecording(audit.New(&audit.Config{Logger: slog.DiscardHandler}))
}

func testGuardRecording(events authn.Recorder) *authn.BrowserGuard {
	g, err := authn.NewBrowserGuard(board+":443/", validCSRF, events)
	if err != nil {
		panic(err)
	}
	return g
}

type tenantKey struct{}

// scopeToTenant stands in for an app's tenancy: what UserMiddlewareConfig's
// Scope sets, the handler can read back.
func scopeToTenant(ctx context.Context, u *authn.User) context.Context {
	return context.WithValue(ctx, tenantKey{}, u.TenantID)
}

type whoResponse struct {
	Body struct {
		UserID   uuid.UUID `json:"user_id"`
		TenantID uuid.UUID `json:"tenant_id"`
		Rejected bool      `json:"rejected"`
	}
}

// newMux mounts one route per marker and method, each reporting who it
// saw, behind the middleware chain cmd/api installs.
func newMux(t *testing.T) *http.ServeMux {
	t.Helper()
	mux, _ := newCountingMux(t)
	return mux
}

// newCountingMux is newMux, counting the guard's refusals.
func newCountingMux(t *testing.T) (*http.ServeMux, *audittest.Counter) {
	t.Helper()
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	logger := slog.DiscardHandler
	events, counter := audittest.NewRecorder()
	guard := testGuardRecording(events)
	api.UseMiddleware(authn.NewPolicy().DefaultDeny(api, logger))
	api.UseMiddleware(guard.PublicWrites(api))
	api.UseMiddleware(authn.UserMiddleware(api, &authn.UserMiddlewareConfig{
		Verify: verify, Guard: guard, SessionCookie: sessionCookie, Scope: scopeToTenant, Logger: logger,
	}))

	who := func(ctx context.Context, _ *struct{}) (*whoResponse, error) {
		resp := &whoResponse{}
		if u, err := authn.UserFromContext(ctx); err == nil {
			resp.Body.UserID = u.ID
		}
		resp.Body.Rejected = authn.CredentialRejected(ctx)
		if tenant, ok := ctx.Value(tenantKey{}).(uuid.UUID); ok {
			resp.Body.TenantID = tenant
		}
		return resp, nil
	}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		huma.Register(api, huma.Operation{Metadata: authn.RequireUser(), OperationID: "user" + method, Method: method, Path: "/user"}, who)
		huma.Register(api, huma.Operation{Metadata: authn.OptionalUser(), OperationID: "optional" + method, Method: method, Path: "/optional"}, who)
		huma.Register(api, huma.Operation{
			Metadata: authn.OptionalUser(), OperationID: "guarded" + method, Method: method, Path: "/guarded",
			Middlewares: huma.Middlewares{authn.RefuseRejectedCredential(api)},
		}, who)
		huma.Register(api, huma.Operation{Metadata: authn.Public(), OperationID: "public" + method, Method: method, Path: "/public"}, who)
	}
	huma.Register(api, huma.Operation{OperationID: "unmarked", Method: http.MethodGet, Path: "/unmarked"}, who)
	both := authn.Public()
	for k, v := range authn.RequireUser() {
		both[k] = v
	}
	huma.Register(api, huma.Operation{Metadata: both, OperationID: "both", Method: http.MethodGet, Path: "/both"}, who)
	optionalAndUser := authn.OptionalUser()
	for k, v := range authn.RequireUser() {
		optionalAndUser[k] = v
	}
	huma.Register(api, huma.Operation{Metadata: optionalAndUser, OperationID: "optionalAndUser", Method: http.MethodGet, Path: "/optional-and-user"}, who)
	return mux, counter
}

type call struct {
	method, path string
	// authorization is the raw Authorization header; bearer a shorthand
	// for "Bearer <bearer>".
	authorization string
	bearer        string
	cookie        string
	csrf          string
	origin        string
	referer       string
}

func serve(mux *http.ServeMux, c call) *httptest.ResponseRecorder {
	return serveWith(mux, c, nil)
}

// serveWith is serve, setting extra headers last.
func serveWith(mux *http.ServeMux, c call, extra map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(c.method, c.path, nil)
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	if c.authorization != "" {
		req.Header.Set("Authorization", c.authorization)
	}
	if c.cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: c.cookie})
	}
	if c.csrf != "" {
		req.Header.Set(authn.CSRFHeader, c.csrf)
	}
	if c.origin != "" {
		req.Header.Set("Origin", c.origin)
	}
	if c.referer != "" {
		req.Header.Set("Referer", c.referer)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// codeOf is a refusal's machine-readable code.
func codeOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var p authn.Problem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p), rec.Body.String())
	return p.Code
}

func TestUserMiddlewareAuthenticates(t *testing.T) {
	mux := newMux(t)
	cases := []struct {
		name string
		call call
		want int
	}{
		{"no credential", call{method: http.MethodGet, path: "/user"}, http.StatusUnauthorized},
		{"bad bearer", call{method: http.MethodGet, path: "/user", bearer: "forged"}, http.StatusUnauthorized},
		{"bad cookie", call{method: http.MethodGet, path: "/user", cookie: "forged"}, http.StatusUnauthorized},
		{"bearer read", call{method: http.MethodGet, path: "/user", bearer: goodToken}, http.StatusOK},
		// A read needs neither an Origin nor a token: it changes nothing,
		// and another site cannot read the answer.
		{"cookie read", call{method: http.MethodGet, path: "/user", cookie: goodToken}, http.StatusOK},
		{"cookie read of a legacy session", call{method: http.MethodGet, path: "/user", cookie: legacyToken}, http.StatusOK},
		// A bad bearer is not rescued by a good cookie.
		{"bad bearer, good cookie", call{method: http.MethodGet, path: "/user", bearer: "forged", cookie: goodToken}, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(mux, tc.call)
			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}
}

// A write a session cookie authenticates must come from the board's origin
// and carry that very session's CSRF token.
func TestSessionWritesNeedTheOriginAndTheSessionsToken(t *testing.T) {
	mux, events := newCountingMux(t)
	good := csrfOf(signedIn.SessionID)
	cases := []struct {
		name     string
		call     call
		want     int
		wantCode string
	}{
		{"origin and token", call{cookie: goodToken, origin: board, csrf: good}, http.StatusOK, ""},
		{"no token", call{cookie: goodToken, origin: board}, http.StatusForbidden, authn.CodeCSRF},
		{"a wrong token", call{cookie: goodToken, origin: board, csrf: "csrf-of-nothing"}, http.StatusForbidden, authn.CodeCSRF},
		// The same person's other browser: its token is no good here.
		{"another session's token", call{cookie: goodToken, origin: board, csrf: csrfOf(otherSession.SessionID)}, http.StatusForbidden, authn.CodeCSRF},
		{"the other session with its own token", call{cookie: otherSessionToken, origin: board, csrf: csrfOf(otherSession.SessionID)}, http.StatusOK, ""},
		// A session from before session ids has no token to send, and an
		// empty one must not match its empty id.
		{"a legacy session", call{cookie: legacyToken, origin: board}, http.StatusForbidden, authn.CodeCSRF},
		{"a legacy session with an empty-id token", call{cookie: legacyToken, origin: board, csrf: csrfOf("")}, http.StatusForbidden, authn.CodeCSRF},
		{"another origin", call{cookie: goodToken, origin: "https://evil.example", csrf: good}, http.StatusForbidden, authn.CodeCrossOrigin},
		{"another port", call{cookie: goodToken, origin: board + ":8443", csrf: good}, http.StatusForbidden, authn.CodeCrossOrigin},
		{"plain http", call{cookie: goodToken, origin: "http://app.example", csrf: good}, http.StatusForbidden, authn.CodeCrossOrigin},
		// A sandboxed frame, or a form under no-referrer: present, and
		// not the board.
		{"Origin: null", call{cookie: goodToken, origin: "null", csrf: good}, http.StatusForbidden, authn.CodeCrossOrigin},
		{"neither Origin nor Referer", call{cookie: goodToken, csrf: good}, http.StatusForbidden, authn.CodeCrossOrigin},
		{"a Referer from the board", call{cookie: goodToken, referer: board + "/tickets/1?x=y", csrf: good}, http.StatusOK, ""},
		{"a Referer from elsewhere", call{cookie: goodToken, referer: "https://evil.example/board", csrf: good}, http.StatusForbidden, authn.CodeCrossOrigin},
		// Referer is only the fallback: an Origin that is present decides.
		{"a bad Origin with a good Referer", call{cookie: goodToken, origin: "https://evil.example", referer: board + "/", csrf: good}, http.StatusForbidden, authn.CodeCrossOrigin},
		{"the origin in another case", call{cookie: goodToken, origin: "HTTPS://App.Example", csrf: good}, http.StatusOK, ""},
		// An Authorization header that names no bearer token leaves the
		// request cookie-authenticated, and so checked.
		{"a Basic Authorization header", call{authorization: "Basic dXNlcjpwYXNz", cookie: goodToken}, http.StatusForbidden, authn.CodeCrossOrigin},
		{"an empty bearer", call{authorization: "Bearer   ", cookie: goodToken, origin: board}, http.StatusForbidden, authn.CodeCSRF},
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		for _, tc := range cases {
			t.Run(method+" "+tc.name, func(t *testing.T) {
				tc.call.method, tc.call.path = method, "/user"
				csrfBefore, originBefore := events.Count(audit.CSRFRefused), events.Count(audit.CrossOriginRefused)
				rec := serve(mux, tc.call)
				require.Equal(t, tc.want, rec.Code, rec.Body.String())
				if tc.wantCode != "" {
					assert.Equal(t, tc.wantCode, codeOf(t, rec))
				} else {
					assert.Contains(t, rec.Body.String(), signedIn.ID.String())
				}
				// Each refusal is recorded once, as the refusal it was.
				wantCSRF, wantOrigin := 0, 0
				switch tc.wantCode {
				case authn.CodeCSRF:
					wantCSRF = 1
				case authn.CodeCrossOrigin:
					wantOrigin = 1
				}
				assert.Equal(t, wantCSRF, events.Count(audit.CSRFRefused)-csrfBefore, "CSRF refusals recorded")
				assert.Equal(t, wantOrigin, events.Count(audit.CrossOriginRefused)-originBefore, "cross-origin refusals recorded")
			})
		}
	}
}

// A browser never attaches an Authorization header by itself, so a request
// that is authenticated by one was not forged by another site's page: it
// needs neither an Origin nor a CSRF token.
func TestBearerWritesAreExempt(t *testing.T) {
	mux := newMux(t)
	for name, c := range map[string]call{
		"nothing else":          {bearer: goodToken},
		"another origin":        {bearer: goodToken, origin: "https://evil.example"},
		"a cookie of its own":   {bearer: goodToken, cookie: otherSessionToken},
		"a legacy bearer token": {bearer: legacyToken},
	} {
		t.Run(name, func(t *testing.T) {
			c.method, c.path = http.MethodPost, "/user"
			rec := serve(mux, c)
			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		})
	}
}

// Sign-in and the other public writes have no session to check, but a
// cross-site form could still use them against a visitor -- signing them
// into an attacker's account -- so they must come from the board.
func TestPublicWritesNeedTheOrigin(t *testing.T) {
	mux := newMux(t)
	cases := []struct {
		name string
		call call
		want int
	}{
		{"the board's origin", call{method: http.MethodPost, origin: board}, http.StatusOK},
		{"a Referer from the board", call{method: http.MethodPost, referer: board + "/login"}, http.StatusOK},
		{"another origin", call{method: http.MethodPost, origin: "https://evil.example"}, http.StatusForbidden},
		{"Origin: null", call{method: http.MethodPost, origin: "null"}, http.StatusForbidden},
		{"neither Origin nor Referer", call{method: http.MethodPost}, http.StatusForbidden},
		{"a Referer from elsewhere", call{method: http.MethodDelete, referer: "https://evil.example/"}, http.StatusForbidden},
		// A bearer token is no credential on a public route, so it does
		// not buy its way past the check either.
		{"a bearer token from elsewhere", call{method: http.MethodPost, bearer: goodToken, origin: "https://evil.example"}, http.StatusForbidden},
		{"a read from anywhere", call{method: http.MethodGet, origin: "https://evil.example"}, http.StatusOK},
		{"a read with neither", call{method: http.MethodGet}, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.call.path = "/public"
			rec := serve(mux, tc.call)
			require.Equal(t, tc.want, rec.Code, rec.Body.String())
			if tc.want == http.StatusForbidden {
				assert.Equal(t, authn.CodeCrossOrigin, codeOf(t, rec))
			}
		})
	}
}

func TestOriginOfSerializesAsABrowserDoes(t *testing.T) {
	for raw, want := range map[string]string{
		"https://app.example":             "https://app.example",
		"https://app.example:443/board/":  "https://app.example",
		"http://frontend:80":              "http://frontend",
		"http://localhost:5273/x?y#z":     "http://localhost:5273",
		"HTTPS://App.EXAMPLE":             "https://app.example",
		"https://[::1]:8443/":             "https://[::1]:8443",
		"http://app.example:443":          "http://app.example:443",
		"https://app.example:80/whatever": "https://app.example:80",
	} {
		got, ok := authn.OriginOf(raw)
		assert.True(t, ok, raw)
		assert.Equal(t, want, got, raw)
	}
	for _, raw := range []string{"", "null", "app.example", "/relative", "ftp://app.example", "https://user@app.example", "://"} {
		_, ok := authn.OriginOf(raw)
		assert.False(t, ok, raw)
	}
}

func TestNewBrowserGuardRefusesABaseURLWithoutAnOrigin(t *testing.T) {
	events := audit.New(&audit.Config{Logger: slog.DiscardHandler})
	_, err := authn.NewBrowserGuard("app.example", validCSRF, events)
	assert.Error(t, err)
	_, err = authn.NewBrowserGuard(board, nil, events)
	assert.Error(t, err)
	_, err = authn.NewBrowserGuard(board, validCSRF, nil)
	assert.Error(t, err)
}

func TestASignedInRequestIsScopedToTheUsersTenant(t *testing.T) {
	rec := serve(newMux(t), call{method: http.MethodGet, path: "/user", cookie: goodToken})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), signedIn.ID.String())
	assert.Contains(t, rec.Body.String(), signedIn.TenantID.String(), "the scope is set with the user")
}

// A public route gets no user even when the request carries a session:
// nothing on it may act as anyone.
func TestAPublicRouteSeesNoUser(t *testing.T) {
	rec := serve(newMux(t), call{method: http.MethodGet, path: "/public", cookie: goodToken})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), signedIn.ID.String())
}

func TestDefaultDenyRefusesRoutesWithoutExactlyOneMarker(t *testing.T) {
	mux := newMux(t)
	for _, path := range []string{"/unmarked", "/both", "/optional-and-user"} {
		t.Run(path, func(t *testing.T) {
			rec := serve(mux, call{method: http.MethodGet, path: path, bearer: goodToken})
			assert.Equal(t, http.StatusInternalServerError, rec.Code)
		})
	}
}

// An app's own marker that needs a user -- an admin's, say -- is
// authenticated like RequireUser, so the app's middleware after this one
// finds the user on the context.
func TestAnAppsUserMarkerIsAuthenticated(t *testing.T) {
	admin := authn.NewMarker("test:requires-admin", "admin")
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.NewPolicy(admin).DefaultDeny(api, slog.DiscardHandler))
	api.UseMiddleware(authn.UserMiddleware(api, &authn.UserMiddlewareConfig{
		Verify: verify, Guard: testGuard(), SessionCookie: sessionCookie, UserMarkers: []*authn.Marker{admin},
	}))
	huma.Register(api, huma.Operation{Metadata: admin.Metadata(), OperationID: "admin", Method: http.MethodGet, Path: "/admin"},
		func(ctx context.Context, _ *struct{}) (*whoResponse, error) {
			resp := &whoResponse{}
			if u, err := authn.UserFromContext(ctx); err == nil {
				resp.Body.UserID = u.ID
			}
			return resp, nil
		})

	assert.Equal(t, http.StatusUnauthorized, serve(mux, call{method: http.MethodGet, path: "/admin"}).Code)
	rec := serve(mux, call{method: http.MethodGet, path: "/admin", bearer: goodToken})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), signedIn.ID.String())
}

func TestUserMiddlewarePanicsOnAnIncompleteConfig(t *testing.T) {
	api := humago.New(http.NewServeMux(), huma.DefaultConfig("test", "0.0.0"))
	for name, cfg := range map[string]*authn.UserMiddlewareConfig{
		"no verifier": {Guard: testGuard(), SessionCookie: sessionCookie},
		"no guard":    {Verify: verify, SessionCookie: sessionCookie},
		"no cookie":   {Verify: verify, Guard: testGuard()},
	} {
		assert.Panics(t, func() { authn.UserMiddleware(api, cfg) }, name)
	}
}
