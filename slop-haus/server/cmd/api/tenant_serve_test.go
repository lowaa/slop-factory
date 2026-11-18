package main

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/auth"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
)

// A tab left open on tenant A, after another tab switched the shared
// session cookie to tenant B: the request it makes carries A's id and B's
// cookie, and must be refused before any handler runs. Without the guard
// the write below reaches B's settings.
func TestServe_AStaleTabCannotReadOrWriteTheTenantTheSessionMovedTo(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{})
	userID, tenantA, tenantB := dbtest.NewUser(t, b.appDB), dbtest.NewTenant(t, b.appDB), dbtest.NewTenant(t, b.appDB)
	signer := auth.NewSigner([]byte(bootJWTSecret), b.cfg.AccessTokenTTL, b.cfg.RefreshTokenTTL)
	csrfTokens, err := auth.NewCSRFTokens([]byte(bootJWTSecret))
	require.NoError(t, err)
	const sessionID = "one-browser-session"
	cookieFor := func(tenant uuid.UUID) string {
		token, err := signer.IssueSessionAccessToken(userID, tenant, &auth.Session{ID: sessionID})
		require.NoError(t, err)
		return authn.SessionCookie + "=" + token
	}
	req := func(method, path, body string, pageTenant uuid.UUID) *request {
		h := map[string]string{"Cookie": cookieFor(tenantB), "Origin": b.base, authn.CSRFHeader: csrfTokens.Token(sessionID)}
		if pageTenant != uuid.Nil {
			h[authn.TenantHeader] = pageTenant.String()
		}
		return &request{method: method, url: b.base + path, body: body, header: h}
	}
	const write = `{"type":"bool","bool_val":true}`

	for name, r := range map[string]*request{
		"read":  req(http.MethodGet, "/api/me/tenant-settings", "", tenantA),
		"write": req(http.MethodPut, "/api/me/tenant-settings/anything", write, tenantA),
	} {
		resp := do(t, r)
		assert.Equal(t, http.StatusConflict, resp.code, name+": "+resp.body)
		assert.Contains(t, resp.body, `"code":"tenant_changed"`, name)
	}

	resp := do(t, req(http.MethodGet, "/api/me/tenant-settings", "", tenantB))
	assert.NotContains(t, resp.body, "tenant_", "the page's tenant is the session's, so the guard lets it through to the handler")
	resp = do(t, req(http.MethodPut, "/api/me/tenant-settings/anything", write, tenantB))
	assert.NotContains(t, resp.body, "tenant_", "the page's tenant is the session's, so the guard lets it through to the handler")
	resp = do(t, req(http.MethodPut, "/api/me/tenant-settings/anything", write, uuid.Nil))
	assert.NotContains(t, resp.body, "tenant_", "a script that names no tenant is left to the CSRF and Origin checks")
}
