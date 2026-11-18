package user_test

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/clientip"
	"github.com/lowaa/slop-factory/go-auth/ratelimit"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// newTestMux wires the auth routes the way cmd/api does -- default-deny
// and the user middleware in front -- so these exercise the wire: status
// codes, cookies, and who gets through.
func newTestMux(t *testing.T, tweak ...func(*user.Config)) (*http.ServeMux, *fixture) {
	t.Helper()
	return newTestMuxWith(t, &fixtureOpts{config: tweak})
}

func newTestMuxWith(t *testing.T, opts *fixtureOpts) (*http.ServeMux, *fixture) {
	t.Helper()
	f := newFixtureWith(t, opts)
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	logger := slog.New(slog.DiscardHandler)
	// httptest requests arrive from 192.0.2.1, standing in for the proxy
	// in front of the server, so X-Forwarded-For is believed from it.
	resolver, err := clientip.NewResolver([]string{"192.0.2.0/24"})
	require.NoError(t, err)
	events := audit.New(slog.DiscardHandler, nil)
	guard, err := authn.NewBrowserGuard(baseURL, f.svc.ValidCSRF, events)
	require.NoError(t, err)
	api.UseMiddleware(resolver.Middleware())
	api.UseMiddleware(authn.DefaultDeny(api, logger))
	api.UseMiddleware(guard.PublicWrites(api))
	api.UseMiddleware(authn.UserMiddleware(api, f.svc.VerifyAccess, guard, logger))
	api.UseMiddleware(authn.AdminMiddleware(api, f.svc.IsAdmin, events, logger))
	user.RegisterRoutes(api, user.NewHandler(f.svc, &user.CookieConfig{
		Secure: true, AccessTTL: 15 * time.Minute, RefreshTTL: 24 * time.Hour,
	}, slog.DiscardHandler))
	return mux, f
}

// request is sent as the board's own page sends it -- from the board's
// origin, and with the session's CSRF token on a write -- unless a test of
// those checks says otherwise.
type request struct {
	method, path, body string
	cookies            []*http.Cookie
	// crossSite sends the request as another site's page would: from
	// another origin, and without the session's CSRF token. noOrigin
	// sends neither Origin nor Referer, as no browser does.
	crossSite bool
	noOrigin  bool
	// noCSRF leaves the session's CSRF token off; csrf sends this one
	// instead.
	noCSRF       bool
	csrf         string
	forwardedFor string
}

func do(t *testing.T, mux *http.ServeMux, r request) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
	if r.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, c := range r.cookies {
		req.AddCookie(c)
	}
	switch {
	case r.crossSite:
		req.Header.Set("Origin", "https://evil.example")
	default:
		if !r.noOrigin {
			req.Header.Set("Origin", baseURL)
		}
		token := r.csrf
		if token == "" && !r.noCSRF && r.method != http.MethodGet {
			token = csrfTokenFor(t, mux, r.cookies)
		}
		if token != "" {
			req.Header.Set(authn.CSRFHeader, token)
		}
	}
	if r.forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", r.forwardedFor)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// csrfTokenFor is the CSRF token of the session among cookies, read from
// /api/auth/me as the board reads it; "" without a session that reads.
func csrfTokenFor(t *testing.T, mux *http.ServeMux, cookies []*http.Cookie) string {
	t.Helper()
	var session []*http.Cookie
	for _, c := range cookies {
		if c.Name == authn.SessionCookie {
			session = append(session, c)
		}
	}
	if len(session) == 0 {
		return ""
	}
	rec := do(t, mux, request{method: http.MethodGet, path: "/api/auth/me", cookies: session})
	if rec.Code != http.StatusOK {
		return ""
	}
	var me user.ProfileBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &me))
	return me.CSRFToken
}

func cookieNamed(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	require.FailNow(t, "no cookie", "%s not set; got %v", name, rec.Header().Values("Set-Cookie"))
	return nil
}

func login(t *testing.T, mux *http.ServeMux) *httptest.ResponseRecorder {
	t.Helper()
	rec := do(t, mux, request{method: http.MethodPost, path: "/api/auth/login",
		body: `{"email":"a@example.com","password":"` + password + `"}`})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return rec
}

func TestSignUpToSignedInOverHTTP(t *testing.T) {
	mux, f := newTestMux(t)

	rec := do(t, mux, request{method: http.MethodPost, path: "/api/auth/register",
		body: `{"email":"a@example.com","password":"` + password + `","display_name":"A"}`})
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	rec = do(t, mux, request{method: http.MethodPost, path: "/api/auth/activate",
		body: `{"token":"` + tokenFrom(t, f.mail.last(t, "a@example.com")) + `"}`})
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	rec = login(t, mux)
	access := cookieNamed(t, rec, authn.SessionCookie)
	refresh := cookieNamed(t, rec, user.RefreshCookie)

	assert.True(t, access.HttpOnly && access.Secure)
	assert.Equal(t, "/", access.Path)
	assert.Equal(t, http.SameSiteLaxMode, access.SameSite)
	assert.True(t, refresh.HttpOnly && refresh.Secure)
	assert.Equal(t, user.RefreshPath, refresh.Path, "sent to the refresh route and nowhere else")
	assert.Equal(t, http.SameSiteStrictMode, refresh.SameSite)
	assert.NotContains(t, rec.Body.String(), access.Value, "no token a script could read")
	assert.NotContains(t, rec.Body.String(), refresh.Value)

	rec = do(t, mux, request{method: http.MethodGet, path: "/api/auth/me", cookies: []*http.Cookie{access}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var me user.ProfileBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &me))
	assert.Equal(t, "a@example.com", me.Email)
}

func TestMeNeedsASession(t *testing.T) {
	mux, _ := newTestMux(t)
	rec := do(t, mux, request{method: http.MethodGet, path: "/api/auth/me"})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRefreshOverHTTP(t *testing.T) {
	mux, f := newTestMux(t)
	f.activeUser(t, "a@example.com")
	refresh := cookieNamed(t, login(t, mux), user.RefreshCookie)

	rec := do(t, mux, request{method: http.MethodPost, path: user.RefreshPath, cookies: []*http.Cookie{refresh}, crossSite: true})
	assert.Equal(t, http.StatusForbidden, rec.Code, "a cross-site post could otherwise mint a session cookie")

	rec = do(t, mux, request{method: http.MethodPost, path: user.RefreshPath, cookies: []*http.Cookie{refresh}})
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	access := cookieNamed(t, rec, authn.SessionCookie)

	rec = do(t, mux, request{method: http.MethodGet, path: "/api/auth/me", cookies: []*http.Cookie{access}})
	assert.Equal(t, http.StatusOK, rec.Code)

	rec = do(t, mux, request{method: http.MethodPost, path: user.RefreshPath})
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "no refresh cookie, no session")
}

func TestLogoutClearsBothCookies(t *testing.T) {
	mux, f := newTestMux(t)
	f.activeUser(t, "a@example.com")
	login(t, mux)

	rec := do(t, mux, request{method: http.MethodPost, path: "/api/auth/logout", crossSite: true})
	assert.Equal(t, http.StatusForbidden, rec.Code)

	rec = do(t, mux, request{method: http.MethodPost, path: "/api/auth/logout"})
	require.Equal(t, http.StatusNoContent, rec.Code)
	for _, name := range []string{authn.SessionCookie, user.RefreshCookie} {
		c := cookieNamed(t, rec, name)
		assert.Empty(t, c.Value, name)
		assert.Negative(t, c.MaxAge, name)
	}
	assert.Equal(t, user.RefreshPath, cookieNamed(t, rec, user.RefreshCookie).Path,
		"a cookie is only cleared by one with the same path")
}

// An unknown address and a wrong password must look identical on the
// wire, not only in the service.
func TestFailedSignInsAreIdenticalOnTheWire(t *testing.T) {
	mux, f := newTestMux(t)
	f.activeUser(t, "a@example.com")

	unknown := do(t, mux, request{method: http.MethodPost, path: "/api/auth/login",
		body: `{"email":"nobody@example.com","password":"x"}`})
	wrong := do(t, mux, request{method: http.MethodPost, path: "/api/auth/login",
		body: `{"email":"a@example.com","password":"x"}`})

	assert.Equal(t, http.StatusUnauthorized, unknown.Code)
	assert.Equal(t, unknown.Code, wrong.Code)
	assert.Equal(t, unknown.Body.String(), wrong.Body.String())
	assert.Empty(t, wrong.Result().Cookies())
}

func TestRateLimitedIsA429(t *testing.T) {
	mux, _ := newTestMux(t, func(c *user.Config) {
		c.MailLimit = &ratelimit.Config{Limit: 1, Window: time.Hour, MaxKeys: 10}
	})
	body := `{"email":"a@example.com"}`
	assert.Equal(t, http.StatusAccepted, do(t, mux, request{method: http.MethodPost, path: "/api/auth/password-reset", body: body}).Code)
	assert.Equal(t, http.StatusTooManyRequests, do(t, mux, request{method: http.MethodPost, path: "/api/auth/password-reset", body: body}).Code)
}

func TestAShortPasswordIsRefusedAtTheEdge(t *testing.T) {
	mux, f := newTestMux(t)
	rec := do(t, mux, request{method: http.MethodPost, path: "/api/auth/register",
		body: `{"email":"a@example.com","password":"short","display_name":"A"}`})
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Empty(t, f.mail.to("a@example.com"))
}

func TestMembersOverHTTP(t *testing.T) {
	mux, f := newTestMux(t)
	f.activeUser(t, "a@example.com")
	f.activeUser(t, "b@example.com")
	access := cookieNamed(t, login(t, mux), authn.SessionCookie)

	rec := do(t, mux, request{method: http.MethodGet, path: "/api/members", cookies: []*http.Cookie{access}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Members []user.MemberBody `json:"members"`
		Former  []user.MemberBody `json:"former"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Members, 1, "b has a tenant of their own")
	assert.Equal(t, "Someone", body.Members[0].DisplayName)
	assert.NotNil(t, body.Former, "former is always a list, empty when nobody left")
	assert.Empty(t, body.Former)

	assert.Equal(t, http.StatusUnauthorized, do(t, mux, request{method: http.MethodGet, path: "/api/members"}).Code)
}

// Over the wire, the client is whoever the trusted proxy says: two clients
// behind it get an allowance each.
func TestSignInLimitsCountEachClientBehindTheProxy(t *testing.T) {
	mux, f := newTestMux(t, func(c *user.Config) {
		c.LoginLimit = &ratelimit.Config{Limit: 1, Window: time.Minute, MaxKeys: 10}
	})
	f.activeUser(t, "a@example.com")
	signIn := func(client string) int {
		return do(t, mux, request{method: http.MethodPost, path: "/api/auth/login", forwardedFor: client,
			body: `{"email":"a@example.com","password":"wrong"}`}).Code
	}

	assert.Equal(t, http.StatusUnauthorized, signIn("203.0.113.66"))
	assert.Equal(t, http.StatusTooManyRequests, signIn("203.0.113.66"))
	assert.Equal(t, http.StatusUnauthorized, signIn("198.51.100.7"), "another client has its own allowance")
}

// One client cycling addresses -- password spraying -- never repeats an
// address|client key, so it is held by the per-client limit instead.
func TestOneClientCyclingAddressesIsLimited(t *testing.T) {
	mux, _ := newTestMux(t, func(c *user.Config) {
		c.LoginClientLimit = &ratelimit.Config{Limit: 3, Window: time.Minute, MaxKeys: 10}
	})
	signIn := func(client, address string) int {
		return do(t, mux, request{method: http.MethodPost, path: "/api/auth/login", forwardedFor: client,
			body: `{"email":"` + address + `","password":"wrong"}`}).Code
	}
	for i := range 3 {
		assert.Equal(t, http.StatusUnauthorized, signIn("203.0.113.66", fmt.Sprintf("u%d@example.com", i)))
	}
	assert.Equal(t, http.StatusTooManyRequests, signIn("203.0.113.66", "u9@example.com"))
	assert.Equal(t, http.StatusUnauthorized, signIn("198.51.100.7", "u9@example.com"), "another client has its own allowance")
}

// An IPv6 client can pick any address in its /64, so the /64 is the
// client.
func TestAnIPv6ClientIsLimitedByItsSlash64(t *testing.T) {
	mux, _ := newTestMux(t, func(c *user.Config) {
		c.LoginClientLimit = &ratelimit.Config{Limit: 2, Window: time.Minute, MaxKeys: 10}
	})
	signIn := func(client string) int {
		return do(t, mux, request{method: http.MethodPost, path: "/api/auth/login", forwardedFor: client,
			body: `{"email":"a@example.com","password":"wrong"}`}).Code
	}
	assert.Equal(t, http.StatusUnauthorized, signIn("2001:db8:1:2::1"))
	assert.Equal(t, http.StatusUnauthorized, signIn("2001:db8:1:2::2"))
	assert.Equal(t, http.StatusTooManyRequests, signIn("2001:db8:1:2:ffff::3"), "the same /64")
	assert.Equal(t, http.StatusUnauthorized, signIn("2001:db8:1:3::1"), "the next /64 is another client")
}

// Reset mail to one address after another from one client is held too.
func TestOneClientMailingAddressAfterAddressIsLimited(t *testing.T) {
	mux, _ := newTestMux(t, func(c *user.Config) {
		c.MailClientLimit = &ratelimit.Config{Limit: 2, Window: time.Hour, MaxKeys: 10}
	})
	reset := func(client, address string) int {
		return do(t, mux, request{method: http.MethodPost, path: "/api/auth/password-reset", forwardedFor: client,
			body: `{"email":"` + address + `"}`}).Code
	}
	assert.Equal(t, http.StatusAccepted, reset("203.0.113.66", "u1@example.com"))
	assert.Equal(t, http.StatusAccepted, reset("203.0.113.66", "u2@example.com"))
	assert.Equal(t, http.StatusTooManyRequests, reset("203.0.113.66", "u3@example.com"))
	assert.Equal(t, http.StatusAccepted, reset("198.51.100.7", "u3@example.com"), "another client has its own allowance")
}

func loginAs(t *testing.T, mux *http.ServeMux, address string) *http.Cookie {
	t.Helper()
	rec := do(t, mux, request{method: http.MethodPost, path: "/api/auth/login",
		body: `{"email":"` + address + `","password":"` + password + `"}`})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return cookieNamed(t, rec, authn.SessionCookie)
}

// nginx asks admin-check before serving the admin pages; everyone who is
// not an admin must get something other than 2xx, or the pages -- and
// their code -- would be served to them.
func TestAdminRoutesAreForAdminsOnly(t *testing.T) {
	mux, f := newTestMux(t)
	f.seededAdmin(t, "admin@example.com")
	f.activeUser(t, "a@example.com")
	admin := loginAs(t, mux, "admin@example.com")
	member := loginAs(t, mux, "a@example.com")

	check := func(cookie *http.Cookie) int {
		r := request{method: http.MethodGet, path: "/api/auth/admin-check"}
		if cookie != nil {
			r.cookies = []*http.Cookie{cookie}
		}
		return do(t, mux, r).Code
	}
	assert.Equal(t, http.StatusNoContent, check(admin))
	assert.Equal(t, http.StatusForbidden, check(member))
	assert.Equal(t, http.StatusUnauthorized, check(nil))

	invite := func(cookie *http.Cookie) int {
		return do(t, mux, request{method: http.MethodPost, path: "/api/admin/invites",
			cookies: []*http.Cookie{cookie}, body: `{"email":"new@example.com"}`}).Code
	}
	assert.Equal(t, http.StatusForbidden, invite(member))
	assert.Empty(t, f.mail.to("new@example.com"), "a refused invite sends nothing")
	assert.Equal(t, http.StatusCreated, invite(admin))
}

func TestAcceptingAnInviteSignsIn(t *testing.T) {
	mux, f := newTestMux(t)
	f.activeUser(t, "a@example.com")
	a := loginAs(t, mux, "a@example.com")

	rec := do(t, mux, request{method: http.MethodPost, path: "/api/invites",
		cookies: []*http.Cookie{a}, body: `{"email":"b@example.com"}`})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	token := tokenFrom(t, f.mail.last(t, "b@example.com"))

	rec = do(t, mux, request{method: http.MethodPost, path: "/api/auth/invite/preview", body: `{"token":"` + token + `"}`})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"account_exists":false`)

	rec = do(t, mux, request{method: http.MethodPost, path: "/api/auth/invite/accept",
		body: `{"token":"` + token + `","display_name":"Bee","password":"` + password + `"}`})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	b := cookieNamed(t, rec, authn.SessionCookie)
	cookieNamed(t, rec, user.RefreshCookie)

	rec = do(t, mux, request{method: http.MethodGet, path: "/api/members", cookies: []*http.Cookie{b}})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Bee", "b is signed in, in a's tenant")
}

func TestSwitchingTenantMovesTheSession(t *testing.T) {
	mux, f := newTestMux(t)
	f.activeUser(t, "a@example.com")
	f.activeUser(t, "b@example.com")
	a := loginAs(t, mux, "a@example.com")
	b := loginAs(t, mux, "b@example.com")

	require.Equal(t, http.StatusCreated, do(t, mux, request{method: http.MethodPost, path: "/api/invites",
		cookies: []*http.Cookie{a}, body: `{"email":"b@example.com"}`}).Code)
	rec := do(t, mux, request{method: http.MethodPost, path: "/api/invites/accept",
		cookies: []*http.Cookie{b}, body: `{"token":"` + tokenFrom(t, f.mail.last(t, "b@example.com")) + `"}`})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	inA := cookieNamed(t, rec, authn.SessionCookie)

	rec = do(t, mux, request{method: http.MethodGet, path: "/api/auth/tenants", cookies: []*http.Cookie{inA}})
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Tenants []user.TenantBody `json:"tenants"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Tenants, 2)
	var home user.TenantBody
	for _, tn := range body.Tenants {
		if !tn.Current {
			home = tn
		}
	}
	require.NotEqual(t, uuid.Nil, home.ID, "exactly one tenant is current")

	rec = do(t, mux, request{method: http.MethodPost, path: "/api/auth/switch-tenant",
		cookies: []*http.Cookie{inA}, body: `{"tenant_id":"` + home.ID.String() + `"}`})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), home.ID.String())
	cookieNamed(t, rec, authn.SessionCookie)
}

func TestCreatingATenantOverHTTPMovesTheSessionAndStopsAtTheLimit(t *testing.T) {
	mux, f := newTestMux(t, func(c *user.Config) { c.TenantOwnerLimit = 2 })
	f.activeUser(t, "a@example.com")
	a := loginAs(t, mux, "a@example.com")

	rec := do(t, mux, request{method: http.MethodPost, path: "/api/tenants",
		cookies: []*http.Cookie{a}, body: `{"name":"Second"}`})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	inNew := cookieNamed(t, rec, authn.SessionCookie)

	rec = do(t, mux, request{method: http.MethodGet, path: "/api/auth/tenants", cookies: []*http.Cookie{inNew}})
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Tenants    []user.TenantBody `json:"tenants"`
		OwnerLimit int               `json:"owner_limit"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, 2, body.OwnerLimit)
	require.Len(t, body.Tenants, 2)
	for _, tn := range body.Tenants {
		assert.Equal(t, tn.Name == "Second", tn.Current, tn.Name)
	}

	rec = do(t, mux, request{method: http.MethodPost, path: "/api/tenants",
		cookies: []*http.Cookie{inNew}, body: `{"name":"Third"}`})
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "tenant_limit_reached")
	rec = do(t, mux, request{method: http.MethodPost, path: "/api/tenants",
		cookies: []*http.Cookie{inNew}, body: `{"name":"Second"}`})
	assert.Equal(t, http.StatusForbidden, rec.Code, "the cap is checked before a name clash is reported")
}

func TestSignUpClosedIsA403AndAdvertised(t *testing.T) {
	mux, _ := newTestMux(t, func(c *user.Config) { c.RegistrationOpen = false })

	rec := do(t, mux, request{method: http.MethodGet, path: "/api/auth/config"})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"registration_open":false`)

	rec = do(t, mux, request{method: http.MethodPost, path: "/api/auth/register",
		body: `{"email":"a@example.com","password":"` + password + `","display_name":"A"}`})
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// Signing in is a write with no session to check, so a cross-site page
// could otherwise sign the victim's browser into an attacker's account.
func TestSignInMustComeFromTheBoard(t *testing.T) {
	mux, f := newTestMux(t)
	f.activeUser(t, "a@example.com")
	body := `{"email":"a@example.com","password":"` + password + `"}`
	for name, r := range map[string]request{
		"another origin":             {crossSite: true},
		"neither Origin nor Referer": {noOrigin: true},
	} {
		r.method, r.path, r.body = http.MethodPost, "/api/auth/login", body
		rec := do(t, mux, r)
		assert.Equal(t, http.StatusForbidden, rec.Code, name)
		assert.Equal(t, authn.CodeCrossOrigin, problem(t, rec).Code, name)
		assert.Empty(t, rec.Result().Cookies(), name)
	}
	login(t, mux)
}

// switchHome is a write any signed-in session may make -- moving into the
// tenant it is already in -- so what answers it is the CSRF check alone.
func switchHome(t *testing.T, mux *http.ServeMux, f *fixture, access *http.Cookie, r request) *httptest.ResponseRecorder {
	t.Helper()
	who, err := f.svc.VerifyAccess(f.ctx, access.Value)
	require.NoError(t, err)
	r.method, r.path = http.MethodPost, "/api/auth/switch-tenant"
	r.body = `{"tenant_id":"` + who.TenantID.String() + `"}`
	r.cookies = []*http.Cookie{access}
	return do(t, mux, r)
}

func profileOf(t *testing.T, rec *httptest.ResponseRecorder) user.ProfileBody {
	t.Helper()
	var p user.ProfileBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p), rec.Body.String())
	return p
}

// A session's CSRF token comes with the sign-in and from /api/auth/me, and
// is the same in both: the page may read it from either.
func TestTheSessionsCSRFTokenIsOnItsProfile(t *testing.T) {
	mux, f := newTestMux(t)
	f.activeUser(t, "a@example.com")
	rec := login(t, mux)
	access := cookieNamed(t, rec, authn.SessionCookie)
	token := profileOf(t, rec).CSRFToken
	require.NotEmpty(t, token)

	me := do(t, mux, request{method: http.MethodGet, path: "/api/auth/me", cookies: []*http.Cookie{access}})
	require.Equal(t, http.StatusOK, me.Code)
	assert.Equal(t, token, profileOf(t, me).CSRFToken)
	assert.NotContains(t, access.Value, token, "the token is not in the session cookie")
}

// A write the session cookie authenticates needs that session's own token.
func TestSessionWritesNeedTheSessionsCSRFToken(t *testing.T) {
	mux, f := newTestMux(t)
	f.activeUser(t, "a@example.com")
	first := login(t, mux)
	access := cookieNamed(t, first, authn.SessionCookie)
	// The same person signed in in another browser: another session.
	otherBrowsers := profileOf(t, login(t, mux)).CSRFToken
	require.NotEqual(t, profileOf(t, first).CSRFToken, otherBrowsers, "a new sign-in is a new session, with a new token")

	for name, tc := range map[string]struct {
		r        request
		code     string
		wantCode int
	}{
		"no token":                {request{noCSRF: true}, authn.CodeCSRF, http.StatusForbidden},
		"a made-up token":         {request{csrf: "bm90LWEtdG9rZW4"}, authn.CodeCSRF, http.StatusForbidden},
		"another session's token": {request{csrf: otherBrowsers}, authn.CodeCSRF, http.StatusForbidden},
		"from another site":       {request{crossSite: true}, authn.CodeCrossOrigin, http.StatusForbidden},
		// Even with the right token: no browser omits both.
		"neither Origin nor Referer": {request{noOrigin: true}, authn.CodeCrossOrigin, http.StatusForbidden},
		"its own token":              {request{}, "", http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			rec := switchHome(t, mux, f, access, tc.r)
			require.Equal(t, tc.wantCode, rec.Code, rec.Body.String())
			if tc.code != "" {
				assert.Equal(t, tc.code, problem(t, rec).Code)
				assert.Empty(t, rec.Result().Cookies(), "refused before it re-issued anything")
			}
		})
	}
}

// A session from before session ids has no CSRF token, so it can read but
// not write: its owner signs in again (the board treats the "csrf" refusal
// as its session having ended). Refreshing does not give it one -- only an
// interactive sign-in starts a session that can write.
func TestASessionFromBeforeSessionIDsMustSignInAgainToWrite(t *testing.T) {
	mux, f := newTestMux(t)
	f.activeUser(t, "a@example.com")
	who := f.signIn(t, "a@example.com")
	legacyAccess, err := f.signer.IssueAccessToken(who.ID, who.TenantID)
	require.NoError(t, err)
	legacyRefresh, err := f.signer.IssueRefreshToken(who.ID, who.TenantID)
	require.NoError(t, err)
	access := &http.Cookie{Name: authn.SessionCookie, Value: legacyAccess}

	me := do(t, mux, request{method: http.MethodGet, path: "/api/auth/me", cookies: []*http.Cookie{access}})
	require.Equal(t, http.StatusOK, me.Code, "it still reads")
	assert.Empty(t, profileOf(t, me).CSRFToken)

	rec := switchHome(t, mux, f, access, request{})
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Equal(t, authn.CodeCSRF, problem(t, rec).Code)

	rec = do(t, mux, request{method: http.MethodPost, path: user.RefreshPath,
		cookies: []*http.Cookie{{Name: user.RefreshCookie, Value: legacyRefresh}}})
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	refreshed, err := f.svc.VerifyAccess(f.ctx, cookieNamed(t, rec, authn.SessionCookie).Value)
	require.NoError(t, err)
	assert.Empty(t, refreshed.SessionID, "a refresh mints no session id")

	fresh := cookieNamed(t, login(t, mux), authn.SessionCookie)
	assert.Equal(t, http.StatusOK, switchHome(t, mux, f, fresh, request{}).Code, "signing in again does")
}

// A browser never attaches an Authorization header by itself, so a request
// authenticated by one needs neither the board's origin nor a CSRF token --
// but naming one is all or nothing: a cookie is never consulted beside it.
func TestBearerAuthenticatedWritesNeedNoCSRFToken(t *testing.T) {
	mux, f := newTestMux(t)
	f.activeUser(t, "a@example.com")
	who := f.signIn(t, "a@example.com")
	session, err := f.svc.Login(f.ctx, "a@example.com", password)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/switch-tenant", strings.NewReader(`{"tenant_id":"`+who.TenantID.String()+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+session.Access)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestMembersOverHTTPListsFormerMembersByNameOnly(t *testing.T) {
	mux, f := newTestMux(t)
	f.activeUser(t, "a@example.com")
	f.activeUser(t, "b@example.com")
	a := f.signIn(t, "a@example.com")
	b := f.signIn(t, "b@example.com")
	_, err := f.db.Global().Exec(f.ctx, `UPDATE global_users SET display_name = 'Bee' WHERE id = $1`, b.ID)
	require.NoError(t, err)
	_, err = f.db.Global().Exec(f.ctx, `INSERT INTO global_tenant_memberships (id, tenant, user_id) VALUES ($1, $2, $3)`, uuid.New(), a.TenantID, b.ID)
	require.NoError(t, err)
	_, err = f.db.Global().Exec(f.ctx, `DELETE FROM global_tenant_memberships WHERE tenant = $1 AND user_id = $2`, a.TenantID, b.ID)
	require.NoError(t, err)

	rec := do(t, mux, request{method: http.MethodGet, path: "/api/members", cookies: []*http.Cookie{cookieNamed(t, login(t, mux), authn.SessionCookie)}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var raw struct {
		Former []map[string]any `json:"former"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	require.Len(t, raw.Former, 1)
	assert.Equal(t, map[string]any{"id": b.ID.String(), "display_name": "Bee"}, raw.Former[0], "id and display_name only")
	assert.NotContains(t, rec.Body.String(), "b@example.com")
}
