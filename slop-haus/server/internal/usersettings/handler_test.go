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

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/authn/authntest"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/usersettings"
)

type rig struct {
	*fixture
	mux    *http.ServeMux
	tokens map[string]uuid.UUID
}

// newRig wires the routes behind the real signed-in-user middleware, so what
// is tested is the wire: status, JSON, and whose settings a token reaches.
func newRig(t *testing.T) *rig {
	t.Helper()
	return newRigWith(t, usersettings.DefaultRegistry())
}

func newRigWith(t *testing.T, registry *usersettings.Registry) *rig {
	t.Helper()
	f := newFixture(t)
	r := &rig{fixture: f, mux: http.NewServeMux(), tokens: map[string]uuid.UUID{}}
	api := humago.New(r.mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.UserMiddleware(api, func(_ context.Context, token string) (*authn.User, error) {
		id, ok := r.tokens[token]
		if !ok {
			return nil, errors.New("unknown token")
		}
		return &authn.User{ID: id, TenantID: uuid.New()}, nil
	}, authntest.Guard(), slog.New(slog.DiscardHandler)))
	svc := usersettings.NewService(f.store, registry, slog.DiscardHandler)
	usersettings.RegisterRoutes(api, usersettings.NewHandler(svc, slog.DiscardHandler))
	return r
}

func (r *rig) signIn() string {
	token := uuid.NewString()
	r.tokens[token] = r.newUser()
	return token
}

func (r *rig) do(method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	return rec
}

func (r *rig) sessionSwitchExplained(t *testing.T, token string) usersettings.UserSettingBody {
	t.Helper()
	return r.setting(t, token, usersettings.RemoteControlSessionSwitchExplained)
}

func (r *rig) setting(t *testing.T, token, name string) usersettings.UserSettingBody {
	t.Helper()
	rec := r.do(http.MethodGet, "/api/me/settings", token, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Settings []usersettings.UserSettingBody `json:"settings"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	for _, s := range body.Settings {
		if s.Name == name {
			return s
		}
	}
	require.Failf(t, "setting missing from the list", "%s", name)
	return usersettings.UserSettingBody{}
}

const sessionSwitchExplainedPath = "/api/me/settings/" + usersettings.RemoteControlSessionSwitchExplained

func TestHandler_ListDefaultsThenReflectsAWrite(t *testing.T) {
	r := newRig(t)
	token := r.signIn()

	s := r.sessionSwitchExplained(t, token)
	require.NotNil(t, s.BoolVal)
	assert.False(t, *s.BoolVal)
	assert.True(t, s.IsDefault)

	rec := r.do(http.MethodPut, sessionSwitchExplainedPath, token, `{"type":"bool","bool_val":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var written usersettings.UserSettingBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &written))
	assert.Equal(t, usersettings.RemoteControlSessionSwitchExplained, written.Name)
	assert.Equal(t, "bool", written.Type)
	require.NotNil(t, written.BoolVal)
	assert.True(t, *written.BoolVal)
	assert.False(t, written.IsDefault)
	assert.NotNil(t, written.UpdatedAt)

	s = r.sessionSwitchExplained(t, token)
	require.NotNil(t, s.BoolVal)
	assert.True(t, *s.BoolVal)
	assert.False(t, s.IsDefault)
}

// Each type has its own field on the wire, so each is written and read back
// with its zero value, where a pointer mishandled as a plain value would show.
func TestHandler_EveryTypeRoundTripsOnTheWire(t *testing.T) {
	reg, err := usersettings.NewRegistry(
		usersettings.Definition{Name: "s", Default: usersettings.StrValue("d")},
		usersettings.Definition{Name: "i", Default: usersettings.IntValue(7)},
		usersettings.Definition{Name: "b", Default: usersettings.BoolValue(true)},
		usersettings.Definition{Name: "f", Default: usersettings.FloatValue(1.5)},
	)
	require.NoError(t, err)
	r := newRigWith(t, reg)
	token := r.signIn()

	for name, body := range map[string]string{
		"s": `{"type":"str","str_val":""}`,
		"i": `{"type":"int","int_val":0}`,
		"b": `{"type":"bool","bool_val":false}`,
		"f": `{"type":"float","float_val":0}`,
	} {
		rec := r.do(http.MethodPut, "/api/me/settings/"+name, token, body)
		require.Equal(t, http.StatusOK, rec.Code, "%s: %s", name, rec.Body.String())
	}

	rec := r.do(http.MethodGet, "/api/me/settings", token, "")
	require.Equal(t, http.StatusOK, rec.Code)
	var list struct {
		Settings []usersettings.UserSettingBody `json:"settings"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	got := map[string]usersettings.UserSettingBody{}
	for _, s := range list.Settings {
		got[s.Name] = s
	}
	require.Len(t, got, 4)
	require.NotNil(t, got["s"].StrVal)
	assert.Equal(t, "", *got["s"].StrVal)
	require.NotNil(t, got["i"].IntVal)
	assert.EqualValues(t, 0, *got["i"].IntVal)
	require.NotNil(t, got["b"].BoolVal)
	assert.False(t, *got["b"].BoolVal)
	require.NotNil(t, got["f"].FloatVal)
	assert.EqualValues(t, 0, *got["f"].FloatVal)
	for name, s := range got {
		assert.False(t, s.IsDefault, name)
	}
}

func TestHandler_AUserOnlyReachesTheirOwnSettings(t *testing.T) {
	r := newRig(t)
	alice, bob := r.signIn(), r.signIn()
	require.Equal(t, http.StatusOK, r.do(http.MethodPut, sessionSwitchExplainedPath, alice, `{"type":"bool","bool_val":true}`).Code)

	require.NotNil(t, r.sessionSwitchExplained(t, bob).BoolVal)
	assert.False(t, *r.sessionSwitchExplained(t, bob).BoolVal, "bob still has the default")
	assert.True(t, *r.sessionSwitchExplained(t, alice).BoolVal)
}

func TestHandler_RefusesWritesThatAreUnknownOrMistyped(t *testing.T) {
	r := newRig(t)
	token := r.signIn()
	cases := []struct {
		name, path, body string
		status           int
	}{
		{"unknown name", "/api/me/settings/nope", `{"type":"bool","bool_val":true}`, http.StatusNotFound},
		{"a retired name", "/api/me/settings/remote_control.enter_sends", `{"type":"bool","bool_val":false}`, http.StatusNotFound},
		{"wrong type", sessionSwitchExplainedPath, `{"type":"str","str_val":"yes"}`, http.StatusUnprocessableEntity},
		{"value field missing", sessionSwitchExplainedPath, `{"type":"bool"}`, http.StatusUnprocessableEntity},
		{"type outside the enum", sessionSwitchExplainedPath, `{"type":"jsonb","bool_val":true}`, http.StatusUnprocessableEntity},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := r.do(http.MethodPut, c.path, token, c.body)
			assert.Equal(t, c.status, rec.Code, rec.Body.String())
		})
	}
	assert.True(t, r.sessionSwitchExplained(t, token).IsDefault, "refused writes store nothing")
}

func TestHandler_RequiresASignedInUser(t *testing.T) {
	r := newRig(t)
	assert.Equal(t, http.StatusUnauthorized, r.do(http.MethodGet, "/api/me/settings", "", "").Code)
	assert.Equal(t, http.StatusUnauthorized, r.do(http.MethodPut, sessionSwitchExplainedPath, "", `{"type":"bool","bool_val":false}`).Code)
	assert.Equal(t, http.StatusUnauthorized, r.do(http.MethodGet, "/api/me/settings", "not-a-user-token", "").Code,
		"a token that is not a signed-in user's")
}
