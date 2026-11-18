package oauth_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
)

func (f *fixture) mux() *http.ServeMux {
	mux := http.NewServeMux()
	f.svc.Mount(mux, func(r *http.Request) netip.Addr { return netip.MustParseAddrPort(r.RemoteAddr).Addr() })
	return mux
}

func serve(mux *http.ServeMux, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// Discovery is where Claude Code starts: from the MCP endpoint's resource
// metadata to this server's authorization metadata.
func TestDiscoveryDocuments(t *testing.T) {
	f := newFixture(t)
	mux := f.mux()

	rec := serve(mux, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource/mcp", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var resource map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resource))
	assert.Equal(t, issuer+"/mcp", resource["resource"])
	assert.Equal(t, []any{issuer}, resource["authorization_servers"])

	rec = serve(mux, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var meta map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &meta))
	assert.Equal(t, issuer, meta["issuer"])
	assert.Equal(t, issuer+"/oauth/token", meta["token_endpoint"])
	assert.Equal(t, []any{"S256"}, meta["code_challenge_methods_supported"])
}

func TestRegistrationOverHTTP(t *testing.T) {
	f := newFixture(t)
	body := `{"client_name":"Claude Code","redirect_uris":["http://localhost:33418/callback"]}`
	rec := serve(f.mux(), httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(body)))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.NotEmpty(t, got["client_id"])
	assert.Equal(t, "none", got["token_endpoint_auth_method"])

	rec = serve(f.mux(), httptest.NewRequest(http.MethodPost, "/oauth/register",
		strings.NewReader(`{"redirect_uris":["https://evil.example/cb"]}`)))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), `"error":"invalid_redirect_uri"`)
}

func authorizeURL(clientID, redirectURI, challenge string) string {
	q := url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirectURI},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"xyz"},
	}
	return "/oauth/authorize?" + q.Encode()
}

func TestAuthorizeOverHTTP(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	_, challenge := pkce(t)
	mux := f.mux()

	rec := serve(mux, httptest.NewRequest(http.MethodGet, authorizeURL(c.ID, redirect, challenge), nil))
	require.Equal(t, http.StatusFound, rec.Code)
	assert.True(t, strings.HasPrefix(rec.Header().Get("Location"), issuer+"/authorize?request="), rec.Header().Get("Location"))

	// A trusted redirect gets the error; an untrusted one gets nothing.
	rec = serve(mux, httptest.NewRequest(http.MethodGet, authorizeURL(c.ID, redirect, ""), nil))
	require.Equal(t, http.StatusFound, rec.Code)
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "localhost:33418", loc.Host)
	assert.Equal(t, "invalid_request", loc.Query().Get("error"))
	assert.Equal(t, "xyz", loc.Query().Get("state"))

	rec = serve(mux, httptest.NewRequest(http.MethodGet, authorizeURL(c.ID, "http://localhost:1/evil", challenge), nil))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, rec.Header().Get("Location"), "never redirected to an unregistered URI")
}

func postForm(mux *http.ServeMux, path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return serve(mux, req)
}

func TestTheTokenEndpointOverHTTP(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	verifier, challenge := pkce(t)
	code := f.authorize(t, c, challenge, newAgent("b"))
	mux := f.mux()

	rec := postForm(mux, "/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "client_id": {c.ID}, "code": {code},
		"redirect_uri": {redirect}, "code_verifier": {verifier},
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"), "tokens must not be cached")
	var tokens oauth.TokenResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &tokens))
	assert.NotEmpty(t, tokens.AccessToken)
	assert.Equal(t, 900, tokens.ExpiresIn)

	rec = postForm(mux, "/oauth/token", url.Values{"grant_type": {"password"}, "client_id": {c.ID}})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), `"error":"unsupported_grant_type"`)

	rec = postForm(mux, "/oauth/token", url.Values{"grant_type": {"refresh_token"}, "client_id": {"nope"}})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), `"error":"invalid_client"`)
}

func TestTheDeviceEndpointOverHTTP(t *testing.T) {
	f := newFixture(t)
	rec := postForm(f.mux(), "/oauth/device_authorization", url.Values{"client_id": {oauth.WorkspaceClientID}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var d oauth.DeviceAuthorization
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &d))
	assert.Regexp(t, `^[B-Z]{4}-[B-Z]{4}$`, d.UserCode)
	assert.Equal(t, issuer+"/device?code="+d.UserCode, d.VerificationURIComplete)
}

// The endpoints on the plain mux record who called them, as the huma
// routes do: the client address is on the context before the handler runs.
func TestOAuthEndpointEventsSayWhoCalled(t *testing.T) {
	f := newFixture(t)
	var logs bytes.Buffer
	f.svc = oauth.NewService(oauth.NewPostgresStore(f.db), f.agents, testConfig(),
		audit.New(slog.NewJSONHandler(&logs, nil), nil), slog.DiscardHandler)
	req := httptest.NewRequest(http.MethodPost, "/oauth/register",
		strings.NewReader(`{"client_name":"Claude Code","redirect_uris":["http://localhost:33418/callback"]}`))
	req.RemoteAddr = "198.51.100.7:4242"
	rec := serve(f.mux(), req)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var line map[string]any
	require.NoError(t, json.Unmarshal(logs.Bytes(), &line), logs.String())
	assert.Equal(t, "oauth_client_registered", line["action"])
	assert.Equal(t, "198.51.100.7", line["client_ip"])
}
