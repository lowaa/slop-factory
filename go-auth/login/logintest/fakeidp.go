// Package logintest is an in-process OpenID Connect provider for tests:
// discovery, JWKS, an authorize endpoint that approves immediately, and a
// token endpoint that enforces PKCE and signs real RS256 ID tokens. It is
// strict where the real thing is strict, so a client that skips PKCE or
// the nonce fails here too. Test-only: never import it from production code.
package logintest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/require"
)

const keyID = "fakeidp-key"

// Server is a running fake IdP. Its URL is the issuer.
type Server struct {
	*httptest.Server
	clientID, clientSecret string
	key                    *rsa.PrivateKey

	mu            sync.Mutex
	user          map[string]any
	override      func(claims map[string]any)
	signKey       *rsa.PrivateKey
	discoveryDown bool
	omitIDToken   bool
	grants        map[string]*grant
}

type grant struct {
	challenge, nonce, redirectURI string
	user                          map[string]any
}

// New starts a fake IdP accepting one client, closed when t ends.
func New(t testing.TB, clientID, clientSecret string) *Server {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	s := &Server{clientID: clientID, clientSecret: clientSecret, key: key, signKey: key, grants: map[string]*grant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", s.discovery)
	mux.HandleFunc("GET /keys", s.jwks)
	mux.HandleFunc("GET /authorize", s.authorize)
	mux.HandleFunc("POST /token", s.token)
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// SetUser sets the claims (sub, email, ...) the next authorization vouches
// for.
func (s *Server) SetUser(claims map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.user = claims
}

// OverrideIDToken lets a test tamper with an ID token's claims (iss, aud,
// exp, nonce, ...) just before it is signed.
func (s *Server) OverrideIDToken(fn func(claims map[string]any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.override = fn
}

// SignWithForeignKey signs ID tokens with a key not in the JWKS, under the
// published key's ID -- a forged token.
func (s *Server) SignWithForeignKey(t testing.TB) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.signKey = key
}

// SetDiscoveryDown makes discovery answer 503, as an IdP still starting
// would.
func (s *Server) SetDiscoveryDown(down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.discoveryDown = down
}

// SetOmitIDToken makes the token endpoint answer without an id_token field
// at all -- a real IdP misconfigured to skip the OIDC scope, or one that
// exchanged but failed to mint one, would look like this.
func (s *Server) SetOmitIDToken(omit bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.omitIDToken = omit
}

// Authorize plays the browser's part at the IdP: it follows authURL (from
// AuthCodeURL) and returns the code and state the IdP redirected back with.
func (s *Server) Authorize(t testing.TB, authURL string) (code, state string) {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(authURL)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode, "fake IdP refused the authorization request")
	loc, err := resp.Location()
	require.NoError(t, err)
	return loc.Query().Get("code"), loc.Query().Get("state")
}

func (s *Server) discovery(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	down := s.discoveryDown
	s.mu.Unlock()
	if down {
		http.Error(w, "starting up", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                s.URL,
		"authorization_endpoint":                s.URL + "/authorize",
		"token_endpoint":                        s.URL + "/token",
		"jwks_uri":                              s.URL + "/keys",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (s *Server) jwks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: &s.key.PublicKey, KeyID: keyID, Algorithm: string(jose.RS256), Use: "sig"},
	}})
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != s.clientID || q.Get("response_type") != "code" {
		http.Error(w, "bad client or response type", http.StatusBadRequest)
		return
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		http.Error(w, "PKCE with S256 is required", http.StatusBadRequest)
		return
	}
	if q.Get("nonce") == "" {
		http.Error(w, "nonce is required", http.StatusBadRequest)
		return
	}
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || redirect.Host == "" {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	code := randomString()
	s.mu.Lock()
	s.grants[code] = &grant{challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), redirectURI: redirect.String(), user: s.user}
	s.mu.Unlock()

	back := redirect.Query()
	back.Set("code", code)
	back.Set("state", q.Get("state"))
	redirect.RawQuery = back.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		tokenError(w, "invalid_request")
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != s.clientID || subtle.ConstantTimeCompare([]byte(secret), []byte(s.clientSecret)) != 1 {
		w.Header().Set("WWW-Authenticate", `Basic realm="fakeidp"`)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}
	if r.PostForm.Get("grant_type") != "authorization_code" {
		tokenError(w, "unsupported_grant_type")
		return
	}

	s.mu.Lock()
	g, ok := s.grants[r.PostForm.Get("code")]
	delete(s.grants, r.PostForm.Get("code"))
	override, signKey, omitIDToken := s.override, s.signKey, s.omitIDToken
	s.mu.Unlock()
	if !ok || r.PostForm.Get("redirect_uri") != g.redirectURI {
		tokenError(w, "invalid_grant")
		return
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
		tokenError(w, "invalid_grant")
		return
	}

	if omitIDToken {
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": randomString(), "token_type": "Bearer", "expires_in": 300,
		})
		return
	}

	now := time.Now()
	claims := map[string]any{
		"iss": s.URL, "aud": s.clientID, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "nonce": g.nonce,
	}
	for k, v := range g.user {
		claims[k] = v
	}
	if override != nil {
		override(claims)
	}
	idToken, err := sign(signKey, claims)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": randomString(), "token_type": "Bearer", "expires_in": 300, "id_token": idToken,
	})
}

func sign(key *rsa.PrivateKey, claims map[string]any) (string, error) {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", keyID))
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	jws, err := signer.Sign(payload)
	if err != nil {
		return "", err
	}
	return jws.CompactSerialize()
}

func tokenError(w http.ResponseWriter, code string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func randomString() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
