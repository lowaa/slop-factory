package login_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/lowaa/slop-factory/go-auth/login"
)

// fakeGitHub is an in-process stand-in for GitHub's OAuth token endpoint
// and /user API, strict about the one thing that matters here -- PKCE --
// so a client that skipped it would fail here too.
type fakeGitHub struct {
	*httptest.Server

	mu           sync.Mutex
	grants       map[string]string // code -> the code_challenge AuthCodeURL sent for it
	failToken    bool
	userStatus   int
	userBody     []byte
	lastTokenReq url.Values
	lastUserAuth string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{grants: map[string]string{}, userStatus: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", f.token)
	mux.HandleFunc("GET /user", f.user)
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// endpoints points a GitHubAuthProvider at this fake instead of the real
// GitHub.
func (f *fakeGitHub) endpoints() login.GitHubEndpoints {
	return login.GitHubEndpoints{
		OAuth: oauth2.Endpoint{AuthURL: f.URL + "/login/oauth/authorize", TokenURL: f.URL + "/login/oauth/access_token"},
		API:   f.URL,
	}
}

// registerGrant records that code was issued for an authorization carrying
// challenge (the code_challenge query parameter AuthCodeURL produced), the
// way GitHub's real /authorize would after approving one. Tests read
// challenge back out of AuthCodeURL's own output rather than recomputing
// it, so a real mismatch between what AuthCodeURL sends and what Exchange
// sends would show up as the token endpoint rejecting the grant below.
func (f *fakeGitHub) registerGrant(code, challenge string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.grants[code] = challenge
}

func (f *fakeGitHub) setFailToken(fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failToken = fail
}

type githubUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name,omitempty"`
}

func (f *fakeGitHub) setUser(u githubUser) {
	body, err := json.Marshal(u)
	if err != nil {
		panic(err)
	}
	f.setUserResponse(http.StatusOK, body)
}

func (f *fakeGitHub) setUserResponse(status int, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.userStatus, f.userBody = status, body
}

func (f *fakeGitHub) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	fail := f.failToken
	challenge, known := f.grants[r.PostForm.Get("code")]
	f.lastTokenReq = r.PostForm
	f.mu.Unlock()

	if fail {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_verification_code"})
		return
	}
	if !known {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "unknown code"})
		return
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "code_verifier mismatch"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": "token-for-" + r.PostForm.Get("code"), "token_type": "Bearer",
	})
}

func (f *fakeGitHub) user(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.lastUserAuth = r.Header.Get("Authorization")
	status, body := f.userStatus, f.userBody
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func newGitHubProvider(fake *fakeGitHub) *login.GitHubAuthProvider {
	return login.NewGitHubAuthProvider("client-id", "client-secret", "http://127.0.0.1/auth/callback", fake.endpoints())
}

func newGitHubAttempt() *login.LoginAttempt {
	return &login.LoginAttempt{State: "state-" + oauth2.GenerateVerifier()[:8], Verifier: oauth2.GenerateVerifier()}
}

func TestGitHub_AuthCodeURLHasStatePKCEAndNoSignupOrScopes(t *testing.T) {
	fake := newFakeGitHub(t)
	p := newGitHubProvider(fake)
	a := newGitHubAttempt()

	raw, err := p.AuthCodeURL(a)
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	q := u.Query()

	assert.Equal(t, fake.URL+"/login/oauth/authorize", u.Scheme+"://"+u.Host+u.Path)
	assert.Equal(t, a.State, q.Get("state"))
	assert.Equal(t, "S256", q.Get("code_challenge_method"))
	assert.Equal(t, oauth2.S256ChallengeFromVerifier(a.Verifier), q.Get("code_challenge"))
	assert.Equal(t, "false", q.Get("allow_signup"))
	assert.Empty(t, q.Get("scope"), "no scope is requested -- the default grant (public profile) is everything used here")
	assert.NotContains(t, raw, a.Verifier, "the verifier itself must never leave the server before the exchange")
}

// grantFor runs AuthCodeURL to get the code_challenge it sends, registers a
// grant for code carrying that challenge (standing in for GitHub's
// /authorize approving it), and returns code -- so Exchange(code, a) below
// only succeeds if it sends back the verifier that hashes to the same
// challenge AuthCodeURL committed to.
func grantFor(t *testing.T, fake *fakeGitHub, p *login.GitHubAuthProvider, a *login.LoginAttempt, code string) {
	t.Helper()
	raw, err := p.AuthCodeURL(a)
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	fake.registerGrant(code, u.Query().Get("code_challenge"))
}

func TestGitHub_ExchangeSendsTheMatchingPKCEVerifierAndCode(t *testing.T) {
	fake := newFakeGitHub(t)
	p := newGitHubProvider(fake)
	a := newGitHubAttempt()
	fake.setUser(githubUser{ID: 1, Login: "someone"})

	grantFor(t, fake, p, a, "the-code")
	_, err := p.Exchange(context.Background(), "the-code", a)
	require.NoError(t, err, "the fake only accepts a code whose verifier hashes to the challenge AuthCodeURL sent")

	assert.Equal(t, "the-code", fake.lastTokenReq.Get("code"))
	assert.Equal(t, a.Verifier, fake.lastTokenReq.Get("code_verifier"))

	// A verifier from a different attempt must be refused, proving the
	// check above isn't vacuous.
	wrong := newGitHubAttempt()
	grantFor(t, fake, p, a, "another-code")
	_, err = p.Exchange(context.Background(), "another-code", wrong)
	require.Error(t, err)
}

func TestGitHub_UserIsFetchedWithTheAccessToken(t *testing.T) {
	fake := newFakeGitHub(t)
	p := newGitHubProvider(fake)
	a := newGitHubAttempt()
	fake.setUser(githubUser{ID: 42, Login: "octocat"})
	grantFor(t, fake, p, a, "code-1")

	_, err := p.Exchange(context.Background(), "code-1", a)
	require.NoError(t, err)
	assert.Equal(t, "Bearer token-for-code-1", fake.lastUserAuth)
}

func TestGitHub_MapsUserFields(t *testing.T) {
	cases := []struct {
		name string
		user githubUser
		want *login.Identity
	}{
		{
			"name present",
			githubUser{ID: 918273645, Login: "octocat", Name: "The Octocat"},
			&login.Identity{Provider: "github", Subject: "918273645", Login: "octocat", DisplayName: "The Octocat"},
		},
		{
			"no name falls back to login",
			githubUser{ID: 42, Login: "no-name-here"},
			&login.Identity{Provider: "github", Subject: "42", Login: "no-name-here", DisplayName: "no-name-here"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeGitHub(t)
			p := newGitHubProvider(fake)
			a := newGitHubAttempt()
			fake.setUser(tc.user)
			grantFor(t, fake, p, a, "code")

			ext, err := p.Exchange(context.Background(), "code", a)
			require.NoError(t, err)
			assert.Equal(t, tc.want, ext)
		})
	}
}

func TestGitHub_NonOKUserResponseIsAnError(t *testing.T) {
	fake := newFakeGitHub(t)
	p := newGitHubProvider(fake)
	a := newGitHubAttempt()
	fake.setUserResponse(http.StatusForbidden, []byte(`{"message":"rate limited"}`))
	grantFor(t, fake, p, a, "code")

	ext, err := p.Exchange(context.Background(), "code", a)
	require.Error(t, err)
	assert.Nil(t, ext)
	assert.ErrorContains(t, err, "403")
}

func TestGitHub_UserResponseMissingIDOrLoginIsAnError(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"no id", `{"login":"someone"}`},
		{"no login", `{"id":123}`},
		{"id zero", `{"id":0,"login":"someone"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeGitHub(t)
			p := newGitHubProvider(fake)
			a := newGitHubAttempt()
			fake.setUserResponse(http.StatusOK, []byte(tc.body))
			grantFor(t, fake, p, a, "code")

			ext, err := p.Exchange(context.Background(), "code", a)
			require.Error(t, err)
			assert.Nil(t, ext)
			assert.ErrorContains(t, err, "no id or login")
		})
	}
}

func TestGitHub_TokenEndpointErrorIsAnError(t *testing.T) {
	fake := newFakeGitHub(t)
	p := newGitHubProvider(fake)
	a := newGitHubAttempt()
	grantFor(t, fake, p, a, "code")
	fake.setFailToken(true)

	ext, err := p.Exchange(context.Background(), "code", a)
	require.Error(t, err)
	assert.Nil(t, ext)
	assert.True(t, strings.Contains(err.Error(), "exchange code"), err.Error())
}
