package user_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/lowaa/slop-factory/go-auth/auth"
	gologin "github.com/lowaa/slop-factory/go-auth/login"
	"github.com/lowaa/slop-factory/go-auth/login/logintest"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// testProvider is a login provider for a fixture, and the issuer its name
// is bound to.
type testProvider struct {
	provider gologin.AuthProvider
	issuer   string
}

const (
	oidcName     = "dex"
	oidcClientID = "slop-haus"
	oidcSecret   = "slop-haus-secret"
)

// newFakeOIDC is go-auth's in-process OIDC provider, and a real
// OIDCAuthProvider pointed at it, discovered and ready.
func newFakeOIDC(t *testing.T) (*logintest.Server, testProvider) {
	t.Helper()
	callback := baseURL + user.CallbackPath(oidcName)
	idp := logintest.New(t, oidcClientID, oidcSecret, callback)
	p := gologin.NewOIDCAuthProvider(&gologin.OIDCOptions{
		IssuerURL: idp.URL, ClientID: oidcClientID, ClientSecret: oidcSecret,
		CallbackURL: callback,
		Name:        oidcName, DisplayName: "Dex",
	}, slog.DiscardHandler)
	require.NoError(t, p.Discover(t.Context()))
	return idp, testProvider{provider: p, issuer: gologin.NormalizeIssuer(idp.URL)}
}

// fakeGitHub stands in for GitHub's token endpoint and /user API, strict
// about PKCE as the real one is. The browser's visit to GitHub's authorize
// page is played by authorize, which approves as whichever user it is
// told to.
type fakeGitHub struct {
	*httptest.Server
	mu     sync.Mutex
	grants map[string]fakeGitHubGrant
}

type fakeGitHubGrant struct {
	challenge string
	user      githubUser
}

type githubUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name,omitempty"`
}

func newFakeGitHub(t *testing.T) (*fakeGitHub, testProvider) {
	t.Helper()
	f := &fakeGitHub{grants: map[string]fakeGitHubGrant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", f.token)
	mux.HandleFunc("GET /user", f.user)
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	p := gologin.NewGitHubAuthProvider("gh-client", "gh-secret", baseURL+user.CallbackPath("github"), gologin.GitHubEndpoints{
		OAuth: oauth2.Endpoint{AuthURL: f.URL + "/login/oauth/authorize", TokenURL: f.URL + "/login/oauth/access_token"},
		API:   f.URL,
	})
	return f, testProvider{provider: p, issuer: gologin.GitHubIssuer}
}

// authorize approves authURL as u, returning what GitHub would redirect
// back with.
func (f *fakeGitHub) authorize(t *testing.T, authURL string, u githubUser) (code, state string) {
	t.Helper()
	parsed, err := url.Parse(authURL)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(authURL, f.URL), "sent to %s, not the fake GitHub", authURL)
	q := parsed.Query()
	code = "code-" + base64.RawURLEncoding.EncodeToString([]byte(u.Login+time.Now().String()))
	f.mu.Lock()
	f.grants[code] = fakeGitHubGrant{challenge: q.Get("code_challenge"), user: u}
	f.mu.Unlock()
	return code, q.Get("state")
}

func (f *fakeGitHub) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	g, ok := f.grants[r.PostForm.Get("code")]
	delete(f.grants, r.PostForm.Get("code"))
	f.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	w.Header().Set("Content-Type", "application/json")
	if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad_verification_code"})
		return
	}
	f.mu.Lock()
	f.grants["token:"+r.PostForm.Get("code")] = g
	f.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]string{"access_token": r.PostForm.Get("code"), "token_type": "bearer"})
}

func (f *fakeGitHub) user(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	g, ok := f.grants["token:"+strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	f.mu.Unlock()
	if !ok {
		http.Error(w, "bad credentials", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(g.user)
}

// browser is one browser against the test mux: a cookie jar that keeps
// what the server sets, and sends back what a browser would -- by path,
// expiry and all -- so a cookie scoped to one route really is missing from
// the others.
type browser struct {
	t   *testing.T
	mux *http.ServeMux
	jar *cookiejar.Jar
}

var siteURL, _ = url.Parse(baseURL)

func newBrowser(t *testing.T, mux *http.ServeMux) *browser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	return &browser{t: t, mux: mux, jar: jar}
}

// do sends a request as the board's own page does: from the board's
// origin, and with the session's CSRF token -- read from /api/auth/me, as
// the page reads it -- on a write.
func (b *browser) do(method, path, body string) *httptest.ResponseRecorder {
	b.t.Helper()
	req := httptest.NewRequest(method, baseURL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", baseURL)
	if method != http.MethodGet {
		if token := b.csrfToken(); token != "" {
			req.Header.Set(authn.CSRFHeader, token)
		}
	}
	return b.send(req)
}

// csrfToken is the browser's session's CSRF token, or "" signed out.
func (b *browser) csrfToken() string {
	b.t.Helper()
	if b.cookie(authn.SessionCookie, "/") == "" {
		return ""
	}
	rec := b.send(httptest.NewRequest(http.MethodGet, baseURL+"/api/auth/me", nil))
	if rec.Code != http.StatusOK {
		return ""
	}
	var me user.ProfileBody
	require.NoError(b.t, json.Unmarshal(rec.Body.Bytes(), &me))
	return me.CSRFToken
}

// navigate follows a link, as the browser does to the start of a sign-in
// or back from the provider: a GET, with no CSRF header.
func (b *browser) navigate(target string) *httptest.ResponseRecorder {
	b.t.Helper()
	if !strings.HasPrefix(target, "http") {
		target = baseURL + target
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	return b.send(req)
}

func (b *browser) send(req *http.Request) *httptest.ResponseRecorder {
	for _, c := range b.jar.Cookies(req.URL) {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	b.mux.ServeHTTP(rec, req)
	b.jar.SetCookies(siteURL, rec.Result().Cookies())
	return rec
}

func (b *browser) cookie(name, path string) string {
	u := *siteURL
	u.Path = path
	for _, c := range b.jar.Cookies(&u) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

// session is the browser's access token, as the server reads it.
func (b *browser) session(f *fixture) *authn.User {
	b.t.Helper()
	raw := b.cookie(authn.SessionCookie, "/")
	require.NotEmpty(b.t, raw, "the browser is not signed in")
	who, err := f.svc.VerifyAccess(f.ctx, raw)
	require.NoError(b.t, err)
	return who
}

func (b *browser) signInWithPassword(address string) {
	b.t.Helper()
	rec := b.do(http.MethodPost, "/api/auth/login", `{"email":"`+address+`","password":"`+password+`"}`)
	require.Equal(b.t, http.StatusOK, rec.Code, rec.Body.String())
}

// setSession puts sess in the browser as if it had signed in at its auth
// time: how a test gets a session too old for a sensitive change without
// waiting ten minutes.
func (b *browser) setSession(f *fixture, userID, tenantID uuid.UUID, sess *auth.Session) {
	b.t.Helper()
	access, err := f.signer.IssueSessionAccessToken(userID, tenantID, sess)
	require.NoError(b.t, err)
	refresh, err := f.signer.IssueSessionRefreshToken(userID, tenantID, sess)
	require.NoError(b.t, err)
	b.jar.SetCookies(siteURL, []*http.Cookie{
		{Name: authn.SessionCookie, Value: access, Path: "/"},
		{Name: user.RefreshCookie, Value: refresh, Path: user.RefreshPath},
	})
}

// staleSession replaces the browser's session with the same one signed in
// eleven minutes ago.
func (b *browser) staleSession(f *fixture) {
	b.t.Helper()
	who := b.session(f)
	b.setSession(f, who.ID, who.TenantID, &auth.Session{AuthTime: time.Now().Add(-11 * time.Minute), ID: who.SessionID})
}

// problem decodes an error answer.
// requireRefusal asserts rec is a callback refused with code: a redirect
// back to the board's page for the round trip, with the code for that page
// to explain, that clears the state cookie and sets no session.
func requireRefusal(t *testing.T, rec *httptest.ResponseRecorder, page, code string, msgAndArgs ...any) {
	t.Helper()
	require.Equal(t, http.StatusFound, rec.Code, append([]any{rec.Body.String()}, msgAndArgs...)...)
	assert.Equal(t, page+"?error="+code, rec.Header().Get("Location"), msgAndArgs...)
	for _, c := range rec.Result().Cookies() {
		assert.NotEqual(t, authn.SessionCookie, c.Name, "a refusal signs nobody in")
		if c.Name == user.LoginStateCookie {
			assert.Negative(t, c.MaxAge, "a round trip is good for one callback, refused or not")
		}
	}
}

func problem(t *testing.T, rec *httptest.ResponseRecorder) *authn.Problem {
	t.Helper()
	var p authn.Problem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p), rec.Body.String())
	return &p
}

// approve plays the person at the provider, approving the sign-in at
// authURL and returning what the provider redirects back with.
type approve func(t *testing.T, authURL string) (code, state string)

// start navigates to the start of a round trip, which must send the
// browser on to the provider; it returns where.
func (b *browser) start(path string) string {
	b.t.Helper()
	rec := b.navigate(path)
	require.Equal(b.t, http.StatusFound, rec.Code, rec.Body.String())
	return rec.Header().Get("Location")
}

// post starts a round trip with a POST, as the board's scripts do for
// anything that acts on an account, and returns where it sends the browser.
func (b *browser) post(path, body string) string {
	b.t.Helper()
	rec := b.do(http.MethodPost, path, body)
	require.Equal(b.t, http.StatusOK, rec.Code, rec.Body.String())
	var started struct {
		RedirectURL string `json:"redirect_url"`
	}
	require.NoError(b.t, json.Unmarshal(rec.Body.Bytes(), &started))
	require.NotEmpty(b.t, started.RedirectURL)
	return started.RedirectURL
}

// startInvite starts accepting the invite token with the OIDC provider.
func (b *browser) startInvite(token, next string) string {
	b.t.Helper()
	return b.startInviteNaming(token, next, "")
}

// startInviteNaming is startInvite for a new_tenant invite, with the name
// the person gave the tenant.
func (b *browser) startInviteNaming(token, next, tenantName string) string {
	b.t.Helper()
	body, err := json.Marshal(map[string]string{"token": token, "next": next, "tenant_name": tenantName})
	require.NoError(b.t, err)
	return b.post("/api/auth/invite/accept/"+oidcName, string(body))
}

// callback is the part of a round trip away from the board: approve at
// the provider, and come back to the callback with what it sends.
func (b *browser) callback(provider, authURL string, approveAs approve) *httptest.ResponseRecorder {
	b.t.Helper()
	code, state := approveAs(b.t, authURL)
	return b.navigate(user.CallbackPath(provider) + "?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state))
}

func asOIDC(idp *logintest.Server, sub, username string) approve {
	return func(t *testing.T, authURL string) (string, string) {
		idp.SetUser(map[string]any{"sub": sub, "preferred_username": username, "name": "Person " + username,
			"email": username + "@idp.example", "email_verified": true})
		return idp.Authorize(t, authURL)
	}
}

func asGitHub(gh *fakeGitHub, u githubUser) approve {
	return func(t *testing.T, authURL string) (string, string) {
		return gh.authorize(t, authURL, u)
	}
}

// authTimeOf reads the auth_time claim straight out of an access token.
func authTimeOf(t *testing.T, signer *auth.Signer, access string) time.Time {
	t.Helper()
	claims, err := signer.ParseAccessToken(access)
	require.NoError(t, err)
	require.NotNil(t, claims.AuthTime, "no auth_time claim")
	return claims.AuthTime.Time
}
