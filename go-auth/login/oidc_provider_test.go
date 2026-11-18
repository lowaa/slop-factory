package login_test

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/lowaa/slop-factory/go-auth/login"
	"github.com/lowaa/slop-factory/go-auth/login/logintest"
)

const (
	testClientID     = "example-app"
	testClientSecret = "client-secret"
)

func newOIDCProvider(t *testing.T, idp *logintest.Server) *login.OIDCAuthProvider {
	t.Helper()
	return login.NewOIDCAuthProvider(&login.OIDCOptions{
		IssuerURL:    idp.URL,
		ClientID:     testClientID,
		ClientSecret: testClientSecret,
		CallbackURL:  "http://127.0.0.1/auth/callback",
		Name:         "dex",
		DisplayName:  "Dex",
		RetryMin:     5 * time.Millisecond,
		RetryMax:     20 * time.Millisecond,
	}, slog.NewTextHandler(io.Discard, nil))
}

// discovered is a fake IdP and a provider that has already discovered it.
func discovered(t *testing.T) (*logintest.Server, *login.OIDCAuthProvider) {
	t.Helper()
	idp := logintest.New(t, testClientID, testClientSecret)
	p := newOIDCProvider(t, idp)
	require.NoError(t, p.Discover(context.Background()))
	return idp, p
}

func newAttempt() *login.LoginAttempt {
	return &login.LoginAttempt{State: "state-" + oauth2.GenerateVerifier()[:8], Verifier: oauth2.GenerateVerifier(), Nonce: "nonce-" + oauth2.GenerateVerifier()[:8]}
}

// loginAs runs a whole authorization against idp as user and returns what
// Exchange made of it, using attempt for AuthCodeURL and exchangeAttempt
// for Exchange -- the same attempt, unless a test is simulating a mismatch.
func loginAs(t *testing.T, idp *logintest.Server, p *login.OIDCAuthProvider, user map[string]any, attempt, exchangeAttempt *login.LoginAttempt) (*login.Identity, error) {
	t.Helper()
	idp.SetUser(user)
	authURL, err := p.AuthCodeURL(attempt)
	require.NoError(t, err)
	code, state := idp.Authorize(t, authURL)
	require.Equal(t, attempt.State, state, "the IdP must hand the state back unchanged")
	return p.Exchange(context.Background(), code, exchangeAttempt)
}

func TestOIDC_AuthCodeURLAsksForPKCEAndNonce(t *testing.T) {
	idp, p := discovered(t)
	a := newAttempt()
	raw, err := p.AuthCodeURL(a)
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	q := u.Query()
	assert.Equal(t, idp.URL+"/authorize", u.Scheme+"://"+u.Host+u.Path)
	assert.Equal(t, a.State, q.Get("state"))
	assert.Equal(t, a.Nonce, q.Get("nonce"))
	assert.Equal(t, "S256", q.Get("code_challenge_method"))
	assert.Equal(t, oauth2.S256ChallengeFromVerifier(a.Verifier), q.Get("code_challenge"))
	assert.Equal(t, "openid profile email", q.Get("scope"))
	assert.NotContains(t, raw, a.Verifier, "the verifier itself must never leave the server before the exchange")
}

func TestOIDC_HappyPathMapsClaims(t *testing.T) {
	idp, p := discovered(t)
	a := newAttempt()
	ext, err := loginAs(t, idp, p, map[string]any{
		"sub": "CiQwOGE4Njg0Yi1kYjg4", "preferred_username": "alice", "name": "Alice Liddell",
		"email": "Alice@Example.test", "email_verified": true,
	}, a, a)
	require.NoError(t, err)
	assert.Equal(t, &login.Identity{
		Provider: "dex", Subject: "CiQwOGE4Njg0Yi1kYjg4", Login: "alice", DisplayName: "Alice Liddell",
		Email: "Alice@Example.test", EmailVerified: true,
	}, ext)
}

func TestOIDC_ClaimFallbacks(t *testing.T) {
	cases := []struct {
		name            string
		user            map[string]any
		wantLogin       string
		wantDisplayName string
		wantVerified    bool
	}{
		{"login falls back to email", map[string]any{"sub": "s1", "email": "bob@example.test", "email_verified": true}, "bob@example.test", "bob@example.test", true},
		{"login falls back to sub", map[string]any{"sub": "s2"}, "s2", "s2", false},
		{"display name falls back to login", map[string]any{"sub": "s3", "preferred_username": "carol"}, "carol", "carol", false},
		{"email_verified as a string", map[string]any{"sub": "s4", "email": "d@example.test", "email_verified": "true"}, "d@example.test", "d@example.test", true},
		{"email_verified absent is unverified", map[string]any{"sub": "s5", "email": "e@example.test"}, "e@example.test", "e@example.test", false},
		{"email_verified false", map[string]any{"sub": "s6", "email": "f@example.test", "email_verified": false}, "f@example.test", "f@example.test", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idp, p := discovered(t)
			a := newAttempt()
			ext, err := loginAs(t, idp, p, tc.user, a, a)
			require.NoError(t, err)
			assert.Equal(t, tc.user["sub"], ext.Subject)
			assert.Equal(t, tc.wantLogin, ext.Login)
			assert.Equal(t, tc.wantDisplayName, ext.DisplayName)
			assert.Equal(t, tc.wantVerified, ext.EmailVerified)
		})
	}
}

// Every way an ID token can be wrong must be refused. Each of these is a
// token the fake IdP signs and returns through the normal token endpoint,
// so the only thing standing between it and a login is the provider's
// verification.
func TestOIDC_RejectsBadIDTokens(t *testing.T) {
	cases := []struct {
		name    string
		tamper  func(t *testing.T, idp *logintest.Server)
		wantErr string
	}{
		{"wrong audience", func(_ *testing.T, idp *logintest.Server) {
			idp.OverrideIDToken(func(c map[string]any) { c["aud"] = "some-other-client" })
		}, "audience"},
		{"wrong issuer", func(_ *testing.T, idp *logintest.Server) {
			idp.OverrideIDToken(func(c map[string]any) { c["iss"] = "https://evil.example.test" })
		}, "issued by a different provider"},
		{"expired", func(_ *testing.T, idp *logintest.Server) {
			idp.OverrideIDToken(func(c map[string]any) {
				c["iat"] = time.Now().Add(-2 * time.Hour).Unix()
				c["exp"] = time.Now().Add(-time.Hour).Unix()
			})
		}, "expired"},
		{"bad signature", func(t *testing.T, idp *logintest.Server) { idp.SignWithForeignKey(t) }, "signature"},
		{"nonce from another login", func(_ *testing.T, idp *logintest.Server) {
			idp.OverrideIDToken(func(c map[string]any) { c["nonce"] = "someone-elses-nonce" })
		}, "nonce"},
		{"no nonce", func(_ *testing.T, idp *logintest.Server) {
			idp.OverrideIDToken(func(c map[string]any) { delete(c, "nonce") })
		}, "nonce"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idp, p := discovered(t)
			tc.tamper(t, idp)
			a := newAttempt()
			ext, err := loginAs(t, idp, p, map[string]any{"sub": "victim", "email": "v@example.test", "email_verified": true}, a, a)
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantErr)
			assert.Nil(t, ext)
		})
	}
}

// The nonce check is the provider's own (go-oidc leaves it to the caller):
// a genuine, correctly signed token whose nonce belongs to a different
// attempt than the one this callback is finishing must be refused.
func TestOIDC_RejectsNonceOfADifferentAttempt(t *testing.T) {
	idp, p := discovered(t)
	started := newAttempt()
	finishing := *started
	finishing.Nonce = "a-different-nonce"
	_, err := loginAs(t, idp, p, map[string]any{"sub": "s"}, started, &finishing)
	require.Error(t, err)
	assert.ErrorContains(t, err, "nonce")
}

// The fake IdP only issues a token when the exchange's code_verifier hashes
// to the challenge the authorization carried, so a successful login proves
// the verifier was sent, and a wrong one proves it is what's checked.
func TestOIDC_ExchangeSendsThePKCEVerifier(t *testing.T) {
	idp, p := discovered(t)

	a := newAttempt()
	_, err := loginAs(t, idp, p, map[string]any{"sub": "s"}, a, a)
	require.NoError(t, err, "the right verifier must be accepted")

	b := newAttempt()
	wrong := *b
	wrong.Verifier = oauth2.GenerateVerifier()
	_, err = loginAs(t, idp, p, map[string]any{"sub": "s"}, b, &wrong)
	require.Error(t, err)
	assert.ErrorContains(t, err, "invalid_grant")
}

// A token response with no id_token at all is a different failure mode
// from a bad one (see TestOIDC_RejectsBadIDTokens): there is nothing to
// verify, so it must be rejected before ever reaching the verifier.
func TestOIDC_RejectsATokenResponseWithNoIDToken(t *testing.T) {
	idp, p := discovered(t)
	idp.SetOmitIDToken(true)
	a := newAttempt()
	ext, err := loginAs(t, idp, p, map[string]any{"sub": "s"}, a, a)
	require.Error(t, err)
	assert.ErrorContains(t, err, "no id_token")
	assert.Nil(t, ext)
}

// An empty subject is refused even though the token is otherwise
// genuine and correctly signed: services key a user's identities on
// provider+subject, and none of them can treat "" as a real one.
func TestOIDC_RejectsAnIDTokenWithAnEmptySubject(t *testing.T) {
	idp, p := discovered(t)
	idp.OverrideIDToken(func(c map[string]any) { delete(c, "sub") })
	a := newAttempt()
	ext, err := loginAs(t, idp, p, map[string]any{"sub": "s"}, a, a)
	require.Error(t, err)
	assert.ErrorContains(t, err, "no subject")
	assert.Nil(t, ext)
}

func TestOIDC_UnavailableUntilDiscovered(t *testing.T) {
	idp := logintest.New(t, testClientID, testClientSecret)
	idp.SetDiscoveryDown(true)
	p := newOIDCProvider(t, idp)

	require.Error(t, p.Discover(context.Background()))
	_, err := p.AuthCodeURL(newAttempt())
	assert.ErrorIs(t, err, login.ErrProviderUnavailable)
	_, err = p.Exchange(context.Background(), "code", newAttempt())
	assert.ErrorIs(t, err, login.ErrProviderUnavailable)

	idp.SetDiscoveryDown(false)
	require.NoError(t, p.Discover(context.Background()))
	_, err = p.AuthCodeURL(newAttempt())
	assert.NoError(t, err)
}

// Start keeps retrying in the background, so an IdP that comes up after
// the server does becomes usable without a restart.
func TestOIDC_StartRetriesUntilTheIssuerIsUp(t *testing.T) {
	idp := logintest.New(t, testClientID, testClientSecret)
	idp.SetDiscoveryDown(true)
	p := newOIDCProvider(t, idp)
	p.Start()
	t.Cleanup(p.Close)

	time.Sleep(30 * time.Millisecond)
	assert.False(t, p.Ready())

	idp.SetDiscoveryDown(false)
	assert.Eventually(t, p.Ready, 5*time.Second, 5*time.Millisecond)
}

func TestOIDC_CloseStopsRetrying(t *testing.T) {
	idp := logintest.New(t, testClientID, testClientSecret)
	idp.SetDiscoveryDown(true)
	p := newOIDCProvider(t, idp)
	p.Start()

	done := make(chan struct{})
	go func() {
		p.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not stop the discovery loop")
	}
	assert.False(t, p.Ready())
}
