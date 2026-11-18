package authn_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// archivedMux is tenantMux with the archived guard ahead of the tenant
// guard, as cmd/api orders them. archived answers for every tenant; with
// withGuard false the guard is left out.
func archivedMux(withGuard bool, archived authn.ArchivedChecker) (*http.ServeMux, *int) {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	logger := slog.New(slog.DiscardHandler)
	calls := new(int)
	api.UseMiddleware(authn.DefaultDeny(api, logger))
	api.UseMiddleware(authn.UserMiddleware(api, verify, testGuard(), logger))
	if withGuard {
		api.UseMiddleware(authn.ArchivedTenantGuard(api, archived, []string{"moveTenant"}, logger))
	}
	api.UseMiddleware(authn.TenantGuard(api, []string{"moveTenant"}, logger))
	for _, op := range []struct{ id, method, path string }{
		{"read", http.MethodGet, "/thing"},
		{"write", http.MethodPost, "/thing"},
		{"moveTenant", http.MethodPost, "/move"},
	} {
		huma.Register(api, huma.Operation{Metadata: authn.RequireUser(), OperationID: op.id, Method: op.method, Path: op.path},
			func(ctx context.Context, _ *struct{}) (*whoResponse, error) {
				*calls++
				return who(ctx, nil)
			})
	}
	return mux, calls
}

func archivedWhen(want bool) authn.ArchivedChecker {
	return func(_ context.Context, tenantID uuid.UUID) (bool, error) {
		if tenantID != signedIn.TenantID {
			return false, errors.New("asked about a tenant the session is not in")
		}
		return want, nil
	}
}

func TestARequestFromAnArchivedTenantIsRefusedBeforeTheHandlerRuns(t *testing.T) {
	mux, calls := archivedMux(true, archivedWhen(true))
	for name, r := range map[string]tenantRequest{
		"cookie read":  {method: http.MethodGet, path: "/thing"},
		"cookie write": {method: http.MethodPost, path: "/thing"},
		"bearer":       {method: http.MethodGet, path: "/thing", bearer: true},
	} {
		rec := r.do(mux)
		assert.Equal(t, http.StatusForbidden, rec.Code, name)
		assert.Contains(t, rec.Body.String(), `"code":"tenant_archived"`, name)
	}
	assert.Zero(t, *calls)
}

func TestAnArchivedTenantsStalePageIsToldItWasArchivedNotThatTheTenantChanged(t *testing.T) {
	mux, _ := archivedMux(true, archivedWhen(true))
	rec := tenantRequest{method: http.MethodGet, path: "/thing", header: otherTenant}.do(mux)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), `"code":"tenant_archived"`)
}

func TestARequestFromALiveTenantPassesTheArchivedGuard(t *testing.T) {
	mux, calls := archivedMux(true, archivedWhen(false))
	assert.Equal(t, http.StatusOK, tenantRequest{method: http.MethodGet, path: "/thing"}.do(mux).Code)
	assert.Equal(t, 1, *calls)
}

func TestTheOperationsThatLeaveATenantRunFromAnArchivedOne(t *testing.T) {
	mux, calls := archivedMux(true, archivedWhen(true))
	rec := tenantRequest{method: http.MethodPost, path: "/move"}.do(mux)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 1, *calls)
}

func TestAFailedArchivedLookupRefusesTheRequest(t *testing.T) {
	mux, calls := archivedMux(true, func(context.Context, uuid.UUID) (bool, error) { return false, errors.New("db down") })
	assert.Equal(t, http.StatusInternalServerError, tenantRequest{method: http.MethodGet, path: "/thing"}.do(mux).Code)
	assert.Zero(t, *calls)
}

// Proves the tests above catch the hole: without the guard an archived
// tenant's session runs.
func TestWithoutTheArchivedGuardAnArchivedTenantsRequestRuns(t *testing.T) {
	mux, calls := archivedMux(false, archivedWhen(true))
	assert.Equal(t, http.StatusOK, tenantRequest{method: http.MethodGet, path: "/thing"}.do(mux).Code)
	assert.Equal(t, 1, *calls)
}
