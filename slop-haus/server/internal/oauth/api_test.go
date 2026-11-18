package oauth_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/authn/authntest"
	"github.com/lowaa/slop-factory/go-auth/clientip"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
)

// consentMux mounts the consent API behind the real user middleware, with
// a verifier that maps a bearer string to one of the fixture's users.
func (f *fixture) consentMux(t *testing.T, users map[string]*authn.User) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	logger := slog.New(slog.DiscardHandler)
	// httptest requests come from 192.0.2.1, standing in for the proxy.
	resolver, err := clientip.NewResolver([]string{"192.0.2.0/24"})
	require.NoError(t, err)
	api.UseMiddleware(resolver.Middleware())
	api.UseMiddleware(authn.DefaultDeny(api, logger))
	api.UseMiddleware(authn.UserMiddleware(api, func(_ context.Context, token string) (*authn.User, error) {
		if u, ok := users[token]; ok {
			return u, nil
		}
		return nil, errors.New("unknown")
	}, authntest.Guard(), logger))
	oauth.RegisterRoutes(api, oauth.NewHandler(f.svc, slog.DiscardHandler))
	return mux
}

func call(mux *http.ServeMux, method, path, bearer, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestConsentOverHTTP(t *testing.T) {
	f := newFixture(t)
	mux := f.consentMux(t, map[string]*authn.User{"me": f.user})
	c := f.client(t)
	_, challenge := pkce(t)
	consent, err := f.svc.Authorize(context.Background(), &oauth.AuthorizeParams{
		ResponseType: "code", ClientID: c.ID, RedirectURI: redirect,
		CodeChallenge: challenge, CodeChallengeMethod: "S256", State: "s",
	})
	require.NoError(t, err)
	u, _ := url.Parse(consent)
	id := u.Query().Get("request")

	assert.Equal(t, http.StatusUnauthorized, call(mux, http.MethodGet, "/api/oauth/requests/"+id, "", "").Code)

	rec := call(mux, http.MethodGet, "/api/oauth/requests/"+id, "me", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"client_name":"Claude Code"`)
	assert.Contains(t, rec.Body.String(), `"redirect_host":"localhost:33418"`)

	rec = call(mux, http.MethodPost, "/api/oauth/requests/"+id+"/decision", "me", `{"approve":true}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "approving names an agent")
	rec = call(mux, http.MethodPost, "/api/oauth/requests/"+id+"/decision", "me",
		`{"approve":true,"agent_name":"x","agent_id":"`+uuid.NewString()+`"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "an existing agent or a new one, not both")

	rec = call(mux, http.MethodPost, "/api/oauth/requests/"+id+"/decision", "me", `{"approve":true,"agent_name":"builder-1"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		RedirectTo string `json:"redirect_to"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	back, err := url.Parse(body.RedirectTo)
	require.NoError(t, err)
	assert.Equal(t, "localhost:33418", back.Host)
	assert.NotEmpty(t, back.Query().Get("code"))

	rec = call(mux, http.MethodPost, "/api/oauth/requests/"+id+"/decision", "me", `{"approve":true,"agent_name":"again"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code, "decided once")
}

func TestTheDeviceApprovalOverHTTP(t *testing.T) {
	f := newFixture(t)
	mux := f.consentMux(t, map[string]*authn.User{"me": f.user})
	d := f.startDevice(t)
	existing, err := f.agents.Create(f.ctx, "builder-1")
	require.NoError(t, err)

	rec := call(mux, http.MethodPost, "/api/oauth/device/lookup", "me", `{"user_code":"`+strings.ToLower(d.UserCode)+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), d.UserCode)

	rec = call(mux, http.MethodPost, "/api/oauth/device/decision", "me",
		`{"user_code":"`+d.UserCode+`","approve":true,"agent_id":"`+existing.ID.String()+`"}`)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	tokens, err := f.poll(d)
	require.NoError(t, err)
	access, err := f.svc.Authenticate(context.Background(), tokens.AccessToken)
	require.NoError(t, err)
	agentID := access.AgentID
	assert.Equal(t, existing.ID, agentID)

	rec = call(mux, http.MethodPost, "/api/oauth/device/lookup", "me", `{"user_code":"BCDF-GHJK"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// Grants are a tenant's: listed and revoked there, invisible elsewhere.
func TestGrantsOverHTTPAreTheTenants(t *testing.T) {
	f := newFixture(t)
	outsider := f.newUser(t)
	mux := f.consentMux(t, map[string]*authn.User{"me": f.user, "outsider": outsider})
	c := f.client(t)
	tokens := f.codeFlowTokens(t, c, "b")

	rec := call(mux, http.MethodGet, "/api/oauth/grants", "outsider", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"grants":[]`)

	rec = call(mux, http.MethodGet, "/api/oauth/grants", "me", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Grants []struct {
			ID        uuid.UUID `json:"id"`
			AgentName string    `json:"agent_name"`
			AgentKind string    `json:"agent_kind"`
		} `json:"grants"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Grants, 1)
	assert.Equal(t, "b", body.Grants[0].AgentName)
	assert.Equal(t, "worker", body.Grants[0].AgentKind)
	grantID := body.Grants[0].ID.String()

	assert.Equal(t, http.StatusNotFound, call(mux, http.MethodDelete, "/api/oauth/grants/"+grantID, "outsider", "").Code)
	assert.True(t, f.works(t, tokens.AccessToken))
	assert.Equal(t, http.StatusNoContent, call(mux, http.MethodDelete, "/api/oauth/grants/"+grantID, "me", "").Code)
	assert.False(t, f.works(t, tokens.AccessToken))
}

// The device page is told where and when a code was started, and whether
// that was the approver's own network -- the phishing tell for a code
// someone else started and sent them.
func TestDeviceLookupSaysWhereItWasStarted(t *testing.T) {
	f := newFixture(t)
	mux := f.consentMux(t, map[string]*authn.User{"me": f.user})
	before := time.Now().Add(-time.Second)
	d, err := f.svc.StartDevice(context.Background(), netip.MustParseAddr("2001:db8:1:2::9"), oauth.WorkspaceClientID, "")
	require.NoError(t, err)

	lookup := func(from string) map[string]any {
		req := httptest.NewRequest(http.MethodPost, "/api/oauth/device/lookup", strings.NewReader(`{"user_code":"`+d.UserCode+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer me")
		req.Header.Set("X-Forwarded-For", from)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		return body
	}

	same := lookup("2001:db8:1:2::77")
	assert.Equal(t, "2001:db8:1:2::9", same["started_from"])
	assert.Equal(t, true, same["from_this_network"], "the same /64")
	startedAt, err := time.Parse(time.RFC3339Nano, same["started_at"].(string))
	require.NoError(t, err)
	assert.WithinRange(t, startedAt, before, time.Now().Add(time.Second))

	other := lookup("203.0.113.5")
	assert.Equal(t, "2001:db8:1:2::9", other["started_from"])
	assert.Equal(t, false, other["from_this_network"])
}
