package githubhook_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/authn/authntest"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/githubhook"
)

// The board's side over the wire: listing a tenant's deliveries, as a
// signed-in member.

func recentlySignedIn(tenant *tenantFixture) *authn.User {
	return &authn.User{ID: tenant.member, TenantID: tenant.id, AuthTime: time.Now()}
}

// boardMux wires the routes as cmd/api does, signed in as who.
func (f *fixture) boardMux(who *authn.User) http.Handler {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.UserMiddleware(api, func(context.Context, string) (*authn.User, error) {
		return who, nil
	}, authntest.Guard(), slog.New(slog.DiscardHandler)))
	githubhook.RegisterRoutes(api, githubhook.NewHandler(f.svc, slog.DiscardHandler))
	return mux
}

func call(t *testing.T, mux http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestListWebhooks_ReportsWhetherTheDoorIsOpen(t *testing.T) {
	f := newFixture(t)
	rec := call(t, f.boardMux(recentlySignedIn(f.a)), http.MethodGet, "/api/github/webhooks")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"enabled":true`)

	f.svc = newService(t, githubhook.NewPostgresStore(f.database), nil, &githubhook.Config{})
	rec = call(t, f.boardMux(recentlySignedIn(f.a)), http.MethodGet, "/api/github/webhooks")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"enabled":false`)
}

// A tenant sees the deliveries recorded for it and no one else's, and the
// installation each came through.
func TestListWebhooks_EachTenantSeesOnlyItsOwnDeliveries(t *testing.T) {
	f := newFixture(t)
	b := f.newTenant()
	aApp, bApp := f.install(f.a), f.install(b)
	f.deliver(delivery{app: aApp, event: "ping", body: `{}`})
	f.deliver(delivery{app: bApp, event: "push", body: `{}`})

	list := func(tenant *tenantFixture) githubhook.WebhooksResponse {
		rec := call(t, f.boardMux(recentlySignedIn(tenant)), http.MethodGet, "/api/github/webhooks")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var listed githubhook.WebhooksResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listed.Body))
		return listed
	}
	a := list(f.a)
	require.Len(t, a.Body.Deliveries, 1)
	assert.Equal(t, "ping", a.Body.Deliveries[0].Event)
	assert.Equal(t, aApp.id, a.Body.Deliveries[0].InstallationID)
	b2 := list(b)
	require.Len(t, b2.Body.Deliveries, 1)
	assert.Equal(t, "push", b2.Body.Deliveries[0].Event)
}

// Deliveries from the repo-endpoint era have no installation and are no
// longer listed.
func TestListWebhooks_LeavesOutDeliveriesWithNoInstallation(t *testing.T) {
	f := newFixture(t)
	_, err := f.database.Exec(f.a.ctx, `INSERT INTO github_deliveries (delivery_id, event, outcome) VALUES ('old', 'ping', 'applied')`)
	require.NoError(t, err)
	assert.Empty(t, f.deliveries(f.a))
}

// Agents may not see webhook deliveries: there is no agent route to them.
func TestWebhooks_NeedASignedInUser(t *testing.T) {
	f := newFixture(t)
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.UserMiddleware(api, func(context.Context, string) (*authn.User, error) {
		return nil, authn.ErrNoUser
	}, authntest.Guard(), slog.New(slog.DiscardHandler)))
	githubhook.RegisterRoutes(api, githubhook.NewHandler(f.svc, slog.DiscardHandler))

	for _, target := range []struct{ method, path string }{
		{http.MethodGet, "/api/github/webhooks"},
	} {
		rec := call(t, mux, target.method, target.path)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s", target.method, target.path)
	}
}

// Repo webhook endpoints are retired: creating or revoking one has no route.
func TestWebhooks_EndpointManagementIsGone(t *testing.T) {
	f := newFixture(t)
	mux := f.boardMux(recentlySignedIn(f.a))

	for _, target := range []struct{ method, path string }{
		{http.MethodPost, "/api/github/webhooks"},
		{http.MethodDelete, "/api/github/webhooks/77777777-7777-7777-7777-777777777777"},
	} {
		rec := call(t, mux, target.method, target.path)
		assert.Contains(t, []int{http.StatusNotFound, http.StatusMethodNotAllowed}, rec.Code, "%s %s", target.method, target.path)
	}
}

func TestNewService_RequiresAHost(t *testing.T) {
	_, err := githubhook.NewService(nil, nil, &githubhook.Config{}, slog.DiscardHandler)
	assert.Error(t, err, "no default host: the wiring chooses")
}
