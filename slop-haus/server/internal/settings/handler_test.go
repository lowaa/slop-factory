package settings_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/authn/authntest"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
)

const testUserToken = "user-token"

var (
	testUserID   = uuid.New()
	testTenantID = uuid.New()
)

// newTestMux wires /api/settings exactly as cmd/api/main.go does, the
// signed-in-user middleware included, so these tests exercise the real wire
// behaviour (status, JSON shape, who is allowed) rather than the handler's
// Go return values.
//
// Every settings route is authn.RequireUser() now, reads included: a
// setting is one tenant's data, so serving it needs to know which tenant is
// asking, and that answer only exists once somebody is signed in. There is
// no separate admin credential any more -- that concept (and the token) is
// gone with single-tenancy.
func newTestMux(t *testing.T) (*http.ServeMux, *fakeStore) {
	t.Helper()
	store := newFakeStore()
	svc := newService(store)

	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.UserMiddleware(api, func(_ context.Context, token string) (*authn.User, error) {
		if token != testUserToken {
			return nil, errors.New("unknown token")
		}
		return &authn.User{ID: testUserID, TenantID: testTenantID}, nil
	}, authntest.Guard(), slog.New(slog.DiscardHandler)))
	settings.RegisterRoutes(api, settings.NewHandler(svc, slog.DiscardHandler))
	return mux, store
}

func patch(t *testing.T, mux *http.ServeMux, name, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/settings/"+name, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// get reads as the fixed signed-in user every read route now requires --
// see newTestMux's doc comment for why there is no open-read case anymore.
func get(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+testUserToken)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// getUnauthenticated is get without the credential, for the one test that
// checks a read is refused without one.
func getUnauthenticated(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// The list is what the top bar renders. It used to need no credential at
// all -- a single-tenant server had nothing a read could leak -- but a
// setting is one tenant's data now, so serving the list needs to know which
// tenant is asking, and that answer only exists once somebody is signed
// in. See newTestMux's doc comment.
func TestListSettings_ReturnsEveryDeclaredSetting(t *testing.T) {
	mux, _ := newTestMux(t)

	rec := get(t, mux, "/api/settings")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var body struct {
		Settings []settings.SettingBody `json:"settings"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Settings, len(settings.Names()))

	idx := slices.IndexFunc(body.Settings, func(b settings.SettingBody) bool { return b.Name == settings.TicketProcessingEnabled })
	require.GreaterOrEqual(t, idx, 0)
	found := body.Settings[idx]
	assert.Equal(t, "bool", found.Type)
	require.NotNil(t, found.BoolVal)
	assert.True(t, *found.BoolVal, "enabled by default")
	assert.True(t, found.IsDefault)
	assert.Nil(t, found.UpdatedAt, "a setting nobody has written has no change time")
	assert.Nil(t, found.IntVal, "the storage detail must not leak into the wire body")
}

// The tenant-scoping counterpart of the test above: with no signed-in user
// there is no tenant to read, so the read is refused rather than answered
// with nobody's settings.
func TestListSettings_NoCredential_Returns401(t *testing.T) {
	mux, _ := newTestMux(t)

	rec := getUnauthenticated(t, mux, "/api/settings")
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "body: %s", rec.Body.String())
}

// Reading a single setting is one tenant's data exactly as much as the
// list is, and gated the same way.
func TestGetSetting_NoCredential_Returns401(t *testing.T) {
	mux, _ := newTestMux(t)

	rec := getUnauthenticated(t, mux, "/api/settings/"+settings.TicketProcessingEnabled)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "body: %s", rec.Body.String())
}

// A write changes what the whole fleet does, so it is gated the same way
// every other mutating route is: a signed-in user.
func TestUpdateSetting_RequiresASignedInUser(t *testing.T) {
	mux, store := newTestMux(t)

	rec := patch(t, mux, settings.TicketProcessingEnabled, `{"type":"bool","bool_val":false}`, "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "body: %s", rec.Body.String())

	rec = patch(t, mux, settings.TicketProcessingEnabled, `{"type":"bool","bool_val":false}`, "wrong-token")
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "body: %s", rec.Body.String())

	assert.Zero(t, store.upserts, "a rejected request must not reach the store")
}

func TestUpdateSetting_WritesAndReportsTheNewValue(t *testing.T) {
	mux, store := newTestMux(t)

	rec := patch(t, mux, settings.TicketProcessingEnabled, `{"type":"bool","bool_val":false}`, testUserToken)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var body settings.SettingBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotNil(t, body.BoolVal)
	assert.False(t, *body.BoolVal)
	assert.False(t, body.IsDefault, "a written setting is no longer a default")
	assert.NotNil(t, body.UpdatedAt)

	assert.Equal(t, 1, store.upserts)
	assert.Equal(t, settings.BoolValue(false), store.upsertVal)
	assert.Equal(t, testUserID, store.upsertBy, "the change is recorded against the signed-in user")

	// And the change is visible to the next reader, which is the whole
	// point -- the toggle has to survive a page reload.
	readRec := get(t, mux, "/api/settings/"+settings.TicketProcessingEnabled)
	require.Equal(t, http.StatusOK, readRec.Code)
	var read settings.SettingBody
	require.NoError(t, json.Unmarshal(readRec.Body.Bytes(), &read))
	require.NotNil(t, read.BoolVal)
	assert.False(t, *read.BoolVal)
}

func TestGetSetting_UnknownNameIs404(t *testing.T) {
	mux, _ := newTestMux(t)

	rec := get(t, mux, "/api/settings/no_such_setting")
	assert.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())
}

// Client input, so a 422 rather than a 500 -- and nothing written. The
// mismatch cases are the ones that would otherwise store a row no reader
// can interpret.
func TestUpdateSetting_RejectsABodyThatDoesNotMatchItsType(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"wrong kind for this setting", `{"type":"string","string_val":"false"}`},
		{"type names a field the body omits", `{"type":"bool"}`},
		{"unrecognised type", `{"type":"colour","string_val":"red"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux, store := newTestMux(t)

			rec := patch(t, mux, settings.TicketProcessingEnabled, tc.body, testUserToken)
			assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
			assert.Zero(t, store.upserts, "a rejected write must not reach the store")
		})
	}
}

func TestUpdateSetting_UnknownNameIs404(t *testing.T) {
	mux, store := newTestMux(t)

	rec := patch(t, mux, "no_such_setting", `{"type":"bool","bool_val":true}`, testUserToken)
	assert.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())
	assert.Zero(t, store.upserts)
}

// A dependency failure is a 500 with nothing useful in it -- and, more to
// the point, not a 200 carrying a made-up value. A settings response the
// browser trusts has to come from the database or not at all.
func TestListSettings_StoreFailureIs500(t *testing.T) {
	mux, store := newTestMux(t)
	store.listErr = errors.New("database is down")

	rec := get(t, mux, "/api/settings")
	require.Equal(t, http.StatusInternalServerError, rec.Code, "body: %s", rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "ticket_processing_enabled",
		"a failed read must not answer with a settings list")
	// The underlying error is logged, never echoed -- it can name tables,
	// columns and connection strings.
	assert.NotContains(t, rec.Body.String(), "database is down")
}

func TestUpdateSetting_StoreFailureIs500(t *testing.T) {
	mux, store := newTestMux(t)
	store.upsertErr = errors.New("database is down")

	rec := patch(t, mux, settings.TicketProcessingEnabled, `{"type":"bool","bool_val":false}`, testUserToken)
	require.Equal(t, http.StatusInternalServerError, rec.Code, "body: %s", rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "database is down")
}

func TestGetSetting_StoreFailureIs500(t *testing.T) {
	mux, store := newTestMux(t)
	store.getErr = errors.New("database is down")

	rec := get(t, mux, "/api/settings/"+settings.TicketProcessingEnabled)
	require.Equal(t, http.StatusInternalServerError, rec.Code, "body: %s", rec.Body.String())
}

// The wire shape is the API's contract with the generated client and the
// board: these bodies are what the server returned before settings moved to
// tenant_settings, byte for byte. The bool rides on bool_val, never on
// int_val.
func TestWireShape_IsUnchanged(t *testing.T) {
	mux, _ := newTestMux(t)
	const schema = "https://example.com/schemas/"
	const defaulted = `"name":"ticket_processing_enabled","type":"bool","string_val":null,"int_val":null,"float_val":null,"bool_val":true,"is_default":true,"updated_at":null}`
	const written = `"name":"ticket_processing_enabled","type":"bool","string_val":null,"int_val":null,"float_val":null,"bool_val":false,"is_default":false,"updated_at":"2026-01-02T03:04:05Z"}`

	list := get(t, mux, "/api/settings").Body.String()
	assert.Contains(t, list, `{"$schema":"`+schema+`ListSettingsResponseBody.json","settings":[`)
	assert.Contains(t, list, `{`+defaulted)
	assert.Equal(t, `{"$schema":"`+schema+`SettingBody.json",`+defaulted+"\n",
		get(t, mux, "/api/settings/ticket_processing_enabled").Body.String())
	assert.Equal(t, `{"$schema":"`+schema+`SettingBody.json",`+written+"\n",
		patch(t, mux, "ticket_processing_enabled", `{"type":"bool","bool_val":false}`, testUserToken).Body.String())
	assert.Equal(t, `{"$schema":"`+schema+`SettingBody.json",`+written+"\n",
		get(t, mux, "/api/settings/ticket_processing_enabled").Body.String())
}
