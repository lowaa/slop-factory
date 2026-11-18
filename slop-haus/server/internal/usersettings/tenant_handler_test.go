package usersettings_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/authn/authntest"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/usersettings"
)

type tenantRig struct {
	*tenantFixture
	mux    *http.ServeMux
	tokens map[string]*authn.User
}

func newTenantRig(t *testing.T) *tenantRig {
	t.Helper()
	f := newTenantFixture(t)
	r := &tenantRig{tenantFixture: f, mux: http.NewServeMux(), tokens: map[string]*authn.User{}}
	api := humago.New(r.mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.UserMiddleware(api, func(_ context.Context, token string) (*authn.User, error) {
		u, ok := r.tokens[token]
		if !ok {
			return nil, errors.New("unknown token")
		}
		return u, nil
	}, authntest.Guard(), slog.New(slog.DiscardHandler)))
	reg := newTenantRegistry(t)
	usersettings.RegisterRoutes(api, usersettings.NewHandler(
		usersettings.NewService(usersettings.NewPostgresStore(f.db), reg, slog.DiscardHandler), slog.DiscardHandler))
	usersettings.RegisterTenantRoutes(api, usersettings.NewTenantHandler(f.svc, slog.DiscardHandler))
	return r
}

func (r *tenantRig) signIn() string {
	token := uuid.NewString()
	r.tokens[token] = &authn.User{ID: r.member(), TenantID: r.tenant}
	return token
}

func (r *tenantRig) do(method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	return rec
}

func TestTenantRoutes_RequireSignIn(t *testing.T) {
	r := newTenantRig(t)
	assert.Equal(t, http.StatusUnauthorized, r.do(http.MethodGet, "/api/me/tenant-settings", "", "").Code)
}

func TestTenantRoutes_SetGetListAndHistory(t *testing.T) {
	r := newTenantRig(t)
	token := r.signIn()
	other := r.signIn()

	rec := r.do(http.MethodGet, "/api/me/tenant-settings", token, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list usersettings.ListResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list.Body))
	require.Len(t, list.Body.Settings, 2)
	assert.True(t, list.Body.Settings[0].IsDefault)

	for _, v := range []string{"true", "false", "true"} {
		rec = r.do(http.MethodPut, "/api/me/tenant-settings/"+tenantSetting, token, `{"type":"bool","bool_val":`+v+`}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
	var one usersettings.UserSettingBody
	rec = r.do(http.MethodGet, "/api/me/tenant-settings/"+tenantSetting, token, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &one))
	assert.False(t, one.IsDefault)
	require.NotNil(t, one.BoolVal)
	assert.True(t, *one.BoolVal)

	rec = r.do(http.MethodGet, "/api/me/tenant-settings/"+tenantSetting, other, "")
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &one))
	assert.True(t, one.IsDefault, "the other user is untouched")

	var hist usersettings.SettingHistoryResponse
	rec = r.do(http.MethodGet, "/api/me/tenant-settings/"+tenantSetting+"/history?limit=2", token, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &hist.Body))
	require.Len(t, hist.Body.Changes, 2)
	require.NotNil(t, hist.Body.NextBefore)
	assert.False(t, hist.Body.Changes[0].EarlierMembership)
	assert.WithinDuration(t, time.Now(), hist.Body.Changes[0].SetAt, time.Minute)

	rec = r.do(http.MethodGet, "/api/me/tenant-settings/"+tenantSetting+"/history", other, "")
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &hist.Body))
	assert.Empty(t, hist.Body.Changes)
}

func TestTenantRoutes_NamesOfTheOtherScopeAreNotFound(t *testing.T) {
	r := newTenantRig(t)
	token := r.signIn()
	body := `{"type":"bool","bool_val":true}`

	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/me/tenant-settings/test.global"},
		{http.MethodPut, "/api/me/tenant-settings/test.global"},
		{http.MethodGet, "/api/me/tenant-settings/test.global/history"},
		{http.MethodPut, "/api/me/settings/" + tenantSetting},
	} {
		assert.Equal(t, http.StatusNotFound, r.do(c.method, c.path, token, body).Code, c.method+" "+c.path)
	}
	rec := r.do(http.MethodGet, "/api/me/settings", token, "")
	assert.NotContains(t, rec.Body.String(), tenantSetting, "the global list omits tenant names")
	assert.Equal(t, http.StatusUnprocessableEntity,
		r.do(http.MethodPut, "/api/me/tenant-settings/"+tenantSetting, token, `{"type":"str","str_val":"x"}`).Code)
}

func TestTenantRoutes_NonMemberIsForbidden(t *testing.T) {
	r := newTenantRig(t)
	token := uuid.NewString()
	r.tokens[token] = &authn.User{ID: dbtest.NewUser(t, r.db), TenantID: r.tenant}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/me/tenant-settings"},
		{http.MethodGet, "/api/me/tenant-settings/" + tenantSetting},
		{http.MethodPut, "/api/me/tenant-settings/" + tenantSetting},
		{http.MethodGet, "/api/me/tenant-settings/" + tenantSetting + "/history"},
	} {
		assert.Equal(t, http.StatusForbidden, r.do(c.method, c.path, token, `{"type":"bool","bool_val":true}`).Code, c.method+" "+c.path)
	}
}

func TestTenantRoutes_ValuesOfEveryTypeRoundTripIncludingZeroValues(t *testing.T) {
	r := newTenantRig(t)
	token := r.signIn()
	rec := r.do(http.MethodPut, "/api/me/tenant-settings/test.label", token, `{"type":"str","str_val":""}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var one usersettings.UserSettingBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &one))
	require.NotNil(t, one.StrVal)
	assert.Equal(t, "", *one.StrVal)
	assert.Nil(t, one.BoolVal)

	rec = r.do(http.MethodGet, "/api/me/tenant-settings/test.label/history", token, "")
	var hist usersettings.SettingHistoryResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &hist.Body))
	require.Len(t, hist.Body.Changes, 1)
	require.NotNil(t, hist.Body.Changes[0].StrVal)
	assert.Equal(t, "str", hist.Body.Changes[0].Type)
	assert.Nil(t, hist.Body.Changes[0].BoolVal)
}
