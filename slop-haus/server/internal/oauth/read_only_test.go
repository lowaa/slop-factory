package oauth_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/auth"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
)

// readOnlyClient makes a read-only client for a new agent in the fixture
// user's tenant, as the smoke_client command does.
func (f *fixture) readOnlyClient(t *testing.T, agentName string) (string, string, *oauth.Access) {
	t.Helper()
	a, err := f.agents.Create(f.ctx, agentName)
	require.NoError(t, err)
	id, secret, err := f.admin.CreateReadOnlyClient(context.Background(), f.user.TenantID, a.ID, "smoke")
	require.NoError(t, err)
	return id, secret, &oauth.Access{TenantID: f.user.TenantID, AgentID: a.ID, ReadOnly: true}
}

func (f *fixture) clientCredentials(clientID, secret string) (*oauth.TokenResponse, error) {
	return f.svc.Token(context.Background(), &oauth.TokenRequest{
		GrantType: oauth.GrantClientCredentials, ClientID: clientID, ClientSecret: secret,
	})
}

func oauthError(t *testing.T, err error) string {
	t.Helper()
	var oe *oauth.Error
	require.True(t, errors.As(err, &oe), "want an OAuth error, got %v", err)
	return oe.Code
}

func TestAReadOnlyClientGetsAReadOnlyAccessTokenForItsAgent(t *testing.T) {
	f := newFixture(t)
	id, secret, want := f.readOnlyClient(t, "smoke")
	assert.Equal(t, 1, f.events.Count(audit.ReadOnlyClientCreated))

	tokens, err := f.clientCredentials(id, secret)
	require.NoError(t, err)
	assert.NotEmpty(t, tokens.AccessToken)
	assert.Equal(t, "Bearer", tokens.TokenType)
	assert.Empty(t, tokens.RefreshToken, "the secret is the long-lived credential; there is nothing to refresh")

	got, err := f.svc.Authenticate(context.Background(), tokens.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// Every other grant's tokens are the full agent's, as they were before
// scopes existed.
func TestAnAgentsOwnSignInIsNotReadOnly(t *testing.T) {
	f := newFixture(t)
	tokens := f.codeFlowTokens(t, f.client(t), "b")
	got, err := f.svc.Authenticate(context.Background(), tokens.AccessToken)
	require.NoError(t, err)
	assert.False(t, got.ReadOnly)
}

func TestAReadOnlyClientMustPresentItsOwnSecret(t *testing.T) {
	f := newFixture(t)
	id, secret, _ := f.readOnlyClient(t, "smoke")
	otherID, otherSecret, _ := f.readOnlyClient(t, "smoke-2")
	require.NotEqual(t, secret, otherSecret)

	for name, try := range map[string]string{
		"no secret":           "",
		"a wrong secret":      secret + "x",
		"another's secret":    otherSecret,
		"the secret's prefix": secret[:len(secret)-1],
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.clientCredentials(id, try)
			assert.Equal(t, "invalid_client", oauthError(t, err))
		})
	}
	assert.Equal(t, 4, f.events.Count(audit.ClientSecretRefused))

	_, err := f.clientCredentials(otherID, otherSecret)
	assert.NoError(t, err)
}

// A public client -- the kind anyone can register -- can't use the grant,
// secret or no secret: the only confidential clients are the ones an admin
// made.
func TestAPublicClientCannotUseClientCredentials(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	_, err := f.clientCredentials(c.ID, "anything")
	assert.Equal(t, "unauthorized_client", oauthError(t, err))

	_, err = f.svc.Register(context.Background(), netip.MustParseAddr("192.0.2.1"), &oauth.RegistrationRequest{
		ClientName: "sneaky", RedirectURIs: []string{redirect},
		GrantTypes: []string{oauth.GrantClientCredentials},
	})
	assert.Error(t, err, "registration can't make a client credentials client")
}

// And a read-only client can't reach a full agent's tokens by another
// grant.
func TestAReadOnlyClientCanUseNoOtherGrant(t *testing.T) {
	f := newFixture(t)
	id, _, _ := f.readOnlyClient(t, "smoke")
	for _, grant := range []string{oauth.GrantAuthorizationCode, oauth.GrantRefreshToken, oauth.GrantDeviceCode} {
		_, err := f.svc.Token(context.Background(), &oauth.TokenRequest{GrantType: grant, ClientID: id})
		assert.Equal(t, "unauthorized_client", oauthError(t, err), grant)
	}
}

func TestRevokingAReadOnlyClientEndsItsTokensAndItsSecret(t *testing.T) {
	f := newFixture(t)
	id, secret, _ := f.readOnlyClient(t, "smoke")
	tokens, err := f.clientCredentials(id, secret)
	require.NoError(t, err)
	require.True(t, f.works(t, tokens.AccessToken))

	require.NoError(t, f.admin.RevokeClient(context.Background(), id))
	assert.Equal(t, 1, f.events.Count(audit.ReadOnlyClientRevoked))

	assert.False(t, f.works(t, tokens.AccessToken))
	_, err = f.clientCredentials(id, secret)
	assert.Equal(t, "invalid_grant", oauthError(t, err))

	assert.ErrorIs(t, f.admin.RevokeClient(context.Background(), "ro-nope"), oauth.ErrNotFound)
}

func TestADisabledAgentsReadOnlyClientGetsNoToken(t *testing.T) {
	f := newFixture(t)
	id, secret, access := f.readOnlyClient(t, "smoke")
	_, err := f.agents.SetDisabled(f.ctx, access.AgentID, true)
	require.NoError(t, err)

	_, err = f.clientCredentials(id, secret)
	assert.Equal(t, "invalid_grant", oauthError(t, err))
}

func basicAuth(id, secret string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(url.QueryEscape(id)+":"+url.QueryEscape(secret)))
}

func TestClientCredentialsOverHTTP(t *testing.T) {
	f := newFixture(t)
	id, secret, _ := f.readOnlyClient(t, "smoke")
	mux := f.mux()

	post := func(form url.Values, authorization string) (int, string) {
		req := httpRequest(form)
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		rec := serve(mux, req)
		return rec.Code, rec.Body.String()
	}

	code, body := post(url.Values{"grant_type": {"client_credentials"}}, basicAuth(id, secret))
	require.Equal(t, http.StatusOK, code, body)
	var tokens map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &tokens))
	assert.NotEmpty(t, tokens["access_token"])
	assert.NotContains(t, tokens, "refresh_token")

	code, body = post(url.Values{"grant_type": {"client_credentials"}, "client_id": {id}, "client_secret": {secret}}, "")
	assert.Equal(t, http.StatusOK, code, body)

	code, body = post(url.Values{"grant_type": {"client_credentials"}}, basicAuth(id, "wrong"))
	assert.Equal(t, http.StatusUnauthorized, code, body)
	assert.Contains(t, body, `"error":"invalid_client"`)

	code, body = post(url.Values{"grant_type": {"client_credentials"}, "client_secret": {secret}}, basicAuth(id, secret))
	assert.Equal(t, http.StatusBadRequest, code, "two authentication methods at once")
	assert.Contains(t, body, `"error":"invalid_request"`)

	code, body = post(url.Values{"grant_type": {"client_credentials"}, "client_id": {"ro-other"}}, basicAuth(id, secret))
	assert.Equal(t, http.StatusBadRequest, code, "a form client_id that contradicts the header")
	assert.Contains(t, body, `"error":"invalid_request"`)
}

func httpRequest(form url.Values) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

// However a confidential client came by its grant, its secret only ever
// buys read-only tokens.
func TestClientCredentialsNeverIssueAFullAgentsToken(t *testing.T) {
	f := newFixture(t)
	a, err := f.agents.Create(f.ctx, "smoke")
	require.NoError(t, err)
	secret := "a-secret"
	c := &oauth.Client{ID: "ro-full", Name: "mis-made", GrantTypes: []string{oauth.GrantClientCredentials}, SecretHash: auth.HashOpaqueToken(secret)}
	g := &oauth.Grant{ID: uuid.New(), TenantID: f.user.TenantID, AgentID: a.ID, ClientID: c.ID, Resource: issuer + "/mcp", Scope: oauth.ScopeAgent}
	require.NoError(t, oauth.NewPostgresStore(f.db).CreateReadOnlyClient(context.Background(), c, g))

	_, err = f.clientCredentials(c.ID, secret)
	assert.Equal(t, "invalid_grant", oauthError(t, err))
}

func TestNoTokenIsIssuedUnderARevokedGrant(t *testing.T) {
	f := newFixture(t)
	id, _, _ := f.readOnlyClient(t, "smoke")
	store := oauth.NewPostgresStore(f.db)
	g, err := store.ClientGrant(context.Background(), id)
	require.NoError(t, err)
	require.NoError(t, f.admin.RevokeClient(context.Background(), id))

	err = store.IssueAccess(context.Background(), g.ID, &oauth.IssuedToken{Hash: "h", ExpiresAt: time.Now().Add(time.Minute)})
	assert.ErrorIs(t, err, oauth.ErrNotFound)
}

// A revocation that commits while a token is being issued under the same
// grant: issuing waits for it, then finds the grant revoked, rather than
// answering with a token that was dead on arrival.
func TestIssuingWaitsOutARevocationInFlight(t *testing.T) {
	f := newFixture(t)
	id, _, _ := f.readOnlyClient(t, "smoke")
	store := oauth.NewPostgresStore(f.db)
	g, err := store.ClientGrant(context.Background(), id)
	require.NoError(t, err)

	issued := make(chan error, 1)
	err = f.db.InGlobalTx(context.Background(), func(q db.Querier) error {
		if _, err := q.Exec(context.Background(), `UPDATE global_oauth_grants SET revoked_at = now() WHERE id = $1`, g.ID); err != nil {
			return err
		}
		go func() {
			issued <- store.IssueAccess(context.Background(), g.ID, &oauth.IssuedToken{Hash: "h", ExpiresAt: time.Now().Add(time.Minute)})
		}()
		// Until the issue is blocked on this transaction's row lock (any
		// lock wait in this test's database is it: nothing else runs) --
		// or has finished without waiting for it, which is the bug.
		for {
			select {
			case err := <-issued:
				return fmt.Errorf("issued without waiting for the revocation in flight: %v", err)
			default:
			}
			var waiting bool
			if err := f.db.Global().QueryRow(context.Background(), `
				SELECT EXISTS (SELECT 1 FROM pg_stat_activity
				WHERE datname = current_database() AND wait_event_type = 'Lock')`).Scan(&waiting); err != nil {
				return err
			}
			if waiting {
				return nil
			}
		}
	})
	require.NoError(t, err)
	assert.ErrorIs(t, <-issued, oauth.ErrNotFound)
}
