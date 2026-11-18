package authn_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// tenantMux is userMux with the tenant guard (when withGuard), and handlers
// that count how often one ran.
func tenantMux(withGuard bool) (*http.ServeMux, *int) {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	logger := slog.New(slog.DiscardHandler)
	guard := testGuard()
	calls := new(int)
	api.UseMiddleware(authn.DefaultDeny(api, logger))
	api.UseMiddleware(authn.UserMiddleware(api, verify, guard, logger))
	if withGuard {
		api.UseMiddleware(authn.TenantGuard(api, []string{"moveTenant"}, logger))
	}
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

type tenantRequest struct {
	method, path string
	header       string // the tenant header; empty for none
	query        string
	bearer       bool
}

func (r tenantRequest) do(mux *http.ServeMux) *httptest.ResponseRecorder {
	target := r.path
	if r.query != "" {
		target += "?" + authn.TenantQuery + "=" + r.query
	}
	req := httptest.NewRequest(r.method, target, nil)
	if r.bearer {
		req.Header.Set("Authorization", "Bearer "+goodToken)
	} else {
		withSession(req)
	}
	if r.method == http.MethodPost {
		req.Header.Set("Origin", board)
		req.Header.Set(authn.CSRFHeader, csrfOf(signedIn.SessionID))
	}
	if r.header != "" {
		req.Header.Set(authn.TenantHeader, r.header)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

var (
	thisTenant  = signedIn.TenantID.String()
	otherTenant = "11111111-1111-1111-1111-111111111111"
)

// The bug: a session another tab moved to tenant B still authenticates a
// page that loaded in tenant A, and its write lands in B.
func TestAWriteFromAPageInAnotherTenantIsRefusedBeforeTheHandlerRuns(t *testing.T) {
	mux, calls := tenantMux(true)
	rec := tenantRequest{method: http.MethodPost, path: "/thing", header: otherTenant}.do(mux)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), `"code":"tenant_changed"`)
	assert.NotContains(t, rec.Body.String(), thisTenant, "names neither tenant")
	assert.NotContains(t, rec.Body.String(), otherTenant, "names neither tenant")
	assert.Zero(t, *calls, "the write must not have run")
}

func TestAReadFromAPageInAnotherTenantIsRefused(t *testing.T) {
	mux, calls := tenantMux(true)
	for name, r := range map[string]tenantRequest{
		"header": {method: http.MethodGet, path: "/thing", header: otherTenant},
		"query":  {method: http.MethodGet, path: "/thing", query: otherTenant},
	} {
		rec := r.do(mux)
		assert.Equal(t, http.StatusConflict, rec.Code, name)
		assert.NotContains(t, rec.Body.String(), thisTenant, name)
	}
	assert.Zero(t, *calls, "no read handler ran")
}

func TestARequestFromAPageInTheSessionsTenantPasses(t *testing.T) {
	mux, calls := tenantMux(true)
	assert.Equal(t, http.StatusOK, tenantRequest{method: http.MethodGet, path: "/thing", header: thisTenant}.do(mux).Code)
	assert.Equal(t, http.StatusOK, tenantRequest{method: http.MethodGet, path: "/thing", query: thisTenant}.do(mux).Code)
	assert.Equal(t, http.StatusOK, tenantRequest{method: http.MethodPost, path: "/thing", header: thisTenant}.do(mux).Code)
	assert.Equal(t, 3, *calls)
}

// Scripts and EventSources have no page tenant to be wrong about; the CSRF
// token and Origin checks, not this guard, stop a forged request.
func TestACookieRequestThatNamesNoTenantIsLeftToTheOtherChecks(t *testing.T) {
	mux, calls := tenantMux(true)
	assert.Equal(t, http.StatusOK, tenantRequest{method: http.MethodPost, path: "/thing"}.do(mux).Code)
	assert.Equal(t, http.StatusOK, tenantRequest{method: http.MethodGet, path: "/thing"}.do(mux).Code)
	assert.Equal(t, 2, *calls)
}

func TestABearerCredentialIsNotAskedWhichTenant(t *testing.T) {
	mux, _ := tenantMux(true)
	assert.Equal(t, http.StatusOK, tenantRequest{method: http.MethodGet, path: "/thing", bearer: true}.do(mux).Code)
	assert.Equal(t, http.StatusOK, tenantRequest{method: http.MethodGet, path: "/thing", header: otherTenant, bearer: true}.do(mux).Code)
}

func TestTheOperationsThatMoveBetweenTenantsRunFromAStalePage(t *testing.T) {
	mux, calls := tenantMux(true)
	rec := tenantRequest{method: http.MethodPost, path: "/move", header: otherTenant}.do(mux)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 1, *calls)
}

// Proves the tests above catch the bug: with no guard, the stale page's
// write runs.
func TestWithoutTheGuardAStalePagesWriteRuns(t *testing.T) {
	mux, calls := tenantMux(false)
	rec := tenantRequest{method: http.MethodPost, path: "/thing", header: otherTenant}.do(mux)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, *calls)
}
