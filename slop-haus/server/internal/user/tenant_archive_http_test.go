package user_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

type archiveEnv struct {
	f      *fixture
	b      *browser
	second uuid.UUID
}

// archiveEnvOf is an owner of two tenants, signed in over HTTP and in the
// first.
func archiveEnvOf(t *testing.T, tweak ...func(*user.Config)) *archiveEnv {
	t.Helper()
	mux, f := newTestMux(t, tweak...)
	_, second := ownerWithTwoTenants(t, f)
	b := newBrowser(t, mux)
	b.signInWithPassword("a@example.com")
	return &archiveEnv{f: f, b: b, second: second}
}

func (e *archiveEnv) archive(b *browser, id uuid.UUID, name string) *httptest.ResponseRecorder {
	return b.do(http.MethodPost, "/api/tenants/"+id.String()+"/archive", `{"confirm_name":"`+name+`"}`)
}

func TestArchiveOverHTTPArchivesAndHidesFromTheListAndShowsInTheArchivedList(t *testing.T) {
	e := archiveEnvOf(t)
	rec := e.archive(e.b, e.second, "Second")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	at, _ := archivedAt(t, e.f, e.second)
	assert.NotNil(t, at)
	assert.Equal(t, 1, e.f.events.Count(audit.TenantArchived))

	assert.NotContains(t, tenantIDs(t, e.f, e.b.session(e.f)), e.second)
	rec = e.b.do(http.MethodGet, "/api/auth/tenants", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), e.second.String())

	rec = e.b.do(http.MethodGet, "/api/tenants/archived", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), e.second.String())
	assert.Contains(t, rec.Body.String(), `"count":1`)
	var archived struct {
		Tenants []struct {
			ID         uuid.UUID `json:"id"`
			ArchivedAt *string   `json:"archived_at"`
		} `json:"tenants"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &archived))
	require.Len(t, archived.Tenants, 1)
	assert.NotNil(t, archived.Tenants[0].ArchivedAt, "the list says when")
}

func TestArchiveOverHTTPWithNoWayToSwitchAgentsOffIs503AndArchivesNothing(t *testing.T) {
	mux, f := newTestMuxWith(t, &fixtureOpts{noAgents: true})
	_, second := ownerWithTwoTenants(t, f)
	b := newBrowser(t, mux)
	b.signInWithPassword("a@example.com")
	rec := b.do(http.MethodPost, "/api/tenants/"+second.String()+"/archive", `{"confirm_name":"Second"}`)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	assert.Equal(t, "archive_unavailable", problem(t, rec).Code)
	at, _ := archivedAt(t, f, second)
	assert.Nil(t, at)
}

func TestArchiveOverHTTPMovesASessionInTheArchivedTenant(t *testing.T) {
	e := archiveEnvOf(t)
	first := e.b.session(e.f).TenantID
	rec := e.b.do(http.MethodPost, "/api/auth/switch-tenant", `{"tenant_id":"`+e.second.String()+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, e.second, e.b.session(e.f).TenantID)

	rec = e.archive(e.b, e.second, "Second")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, first, e.b.session(e.f).TenantID, "the session moved to the other active tenant")
	assert.Equal(t, http.StatusOK, e.b.do(http.MethodGet, "/api/auth/me", "").Code)
}

func TestArchiveOverHTTPRefusals(t *testing.T) {
	e := archiveEnvOf(t)
	first := e.b.session(e.f).TenantID

	rec := e.archive(e.b, e.second, "second")
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "the name must match exactly")
	assert.Equal(t, "confirm_name_mismatch", problem(t, rec).Code)
	rec = e.archive(e.b, e.second, "")
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	at, _ := archivedAt(t, e.f, e.second)
	assert.Nil(t, at)

	// Archive the second, then the first is the last active.
	require.Equal(t, http.StatusOK, e.archive(e.b, e.second, "Second").Code)
	var firstName string
	require.NoError(t, e.f.db.Global().QueryRow(e.f.ctx, `SELECT name FROM global_tenants WHERE id = $1`, first).Scan(&firstName))
	rec = e.archive(e.b, first, firstName)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, "last_active_tenant", problem(t, rec).Code)

	e.b.staleSession(e.f)
	rec = e.b.do(http.MethodPost, "/api/tenants/"+e.second.String()+"/unarchive", "")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "reauth_required", problem(t, rec).Code)
}

func TestArchiveOverHTTPNeedsARecentSignIn(t *testing.T) {
	e := archiveEnvOf(t)
	e.b.staleSession(e.f)
	rec := e.archive(e.b, e.second, "Second")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "reauth_required", problem(t, rec).Code)
	at, _ := archivedAt(t, e.f, e.second)
	assert.Nil(t, at)
}

func TestArchiveOverHTTPIsForTheOwnerOnly(t *testing.T) {
	e := archiveEnvOf(t)
	// A member of the second tenant, via an invite.
	e.f.activeUser(t, "m@example.com")
	m := e.f.signIn(t, "m@example.com")
	_, err := e.f.db.Global().Exec(e.f.ctx, `INSERT INTO global_tenant_memberships (id, tenant, user_id) VALUES ($1, $2, $3)`, uuid.New(), e.second, m.ID)
	require.NoError(t, err)
	member := newBrowser(t, e.b.mux)
	member.signInWithPassword("m@example.com")

	rec := e.archive(member, e.second, "Second")
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Equal(t, "not_owner", problem(t, rec).Code)
	rec = member.do(http.MethodPost, "/api/tenants/"+e.second.String()+"/unarchive", "")
	assert.Equal(t, http.StatusForbidden, rec.Code)

	require.Equal(t, http.StatusOK, e.archive(e.b, e.second, "Second").Code)
	rec = member.do(http.MethodGet, "/api/tenants/archived", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"count":0`, "a member is shown nothing")
	assert.Equal(t, http.StatusForbidden, member.do(http.MethodPost, "/api/tenants/"+e.second.String()+"/unarchive", "").Code)
}

func TestUnarchiveOverHTTPRestoresAccessAndRefusesOverTheCap(t *testing.T) {
	e := archiveEnvOf(t, func(c *user.Config) { c.TenantOwnerLimit = 2 })
	require.Equal(t, http.StatusOK, e.archive(e.b, e.second, "Second").Code)

	// Back at the cap with a third active tenant.
	rec := e.b.do(http.MethodPost, "/api/tenants", `{"name":"Third"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = e.b.do(http.MethodPost, "/api/tenants/"+e.second.String()+"/unarchive", "")
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Equal(t, "tenant_limit_reached", problem(t, rec).Code)

	// Archive the third; unarchiving the second now fits.
	third := e.b.session(e.f).TenantID
	require.Equal(t, http.StatusOK, e.archive(e.b, third, "Third").Code)
	rec = e.b.do(http.MethodPost, "/api/tenants/"+e.second.String()+"/unarchive", "")
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Contains(t, tenantIDs(t, e.f, e.b.session(e.f)), e.second)
	rec = e.b.do(http.MethodPost, "/api/auth/switch-tenant", `{"tenant_id":"`+e.second.String()+`"}`)
	assert.Equal(t, http.StatusOK, rec.Code, "access is back")
}
