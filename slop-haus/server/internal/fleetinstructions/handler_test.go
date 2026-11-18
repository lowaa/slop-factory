package fleetinstructions_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/fleetinstructions"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

// server mounts the real routes with the signed-in user the test picks, in
// place of the session middleware: what this checks is what the handler does
// with a user, not how one is found.
func (f *fixture) server(who *authn.User) *http.ServeMux {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		if who != nil {
			ctx = huma.WithContext(ctx, authn.NewUserContext(ctx.Context(), who))
		}
		next(ctx)
	})
	fleetinstructions.RegisterRoutes(api, fleetinstructions.NewHandler(f.svc, f.codeHost, slog.DiscardHandler))
	return mux
}

func call(mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestHandler_OwnerSavesAndChoosesAndMemberOnlyReads(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	owner, member := f.server(f.owner), f.server(f.member)

	rec := call(owner, http.MethodPost, "/api/fleet_instructions/versions", `{"name":"house","body":"comment a plan first","make_default":true}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var saved fleetinstructions.VersionBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &saved))
	assert.Equal(t, 1, saved.Version)

	var got fleetinstructions.FleetInstructionsBody
	for who, mux := range map[string]*http.ServeMux{"owner": owner, "member": member} {
		rec = call(mux, http.MethodGet, "/api/fleet_instructions", "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.Len(t, got.Versions, 1)
		assert.Equal(t, saved.ID, *got.DefaultID)
		assert.Equal(t, who == "owner", got.CanEdit, who)
		assert.Equal(t, fleetinstructions.MaxBodyBytes, got.MaxBytes)
	}

	rec = call(owner, http.MethodPut, "/api/fleet_instructions/default", `{"version_id":null}`)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	rec = call(owner, http.MethodGet, "/api/fleet_instructions", "")
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Nil(t, got.DefaultID)
}

func TestHandler_RefusalsAreMappedToStatuses(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	v := f.save("n", "text", false)

	for name, c := range map[string]struct {
		mux          *http.ServeMux
		method, path string
		body         string
		want         int
	}{
		"member saves":       {f.server(f.member), http.MethodPost, "/api/fleet_instructions/versions", `{"name":"n","body":"x","make_default":false}`, http.StatusForbidden},
		"member chooses":     {f.server(f.member), http.MethodPut, "/api/fleet_instructions/default", `{"version_id":"` + v.ID.String() + `"}`, http.StatusForbidden},
		"nobody signed in":   {f.server(nil), http.MethodGet, "/api/fleet_instructions", ``, http.StatusUnauthorized},
		"oversized text":     {f.server(f.owner), http.MethodPost, "/api/fleet_instructions/versions", `{"name":"n","body":"` + strings.Repeat("x", fleetinstructions.MaxBodyBytes+1) + `","make_default":false}`, http.StatusUnprocessableEntity},
		"unknown version":    {f.server(f.owner), http.MethodPut, "/api/fleet_instructions/default", `{"version_id":"7f2c1c1e-0000-4000-8000-000000000000"}`, http.StatusNotFound},
		"missing version_id": {f.server(f.owner), http.MethodPut, "/api/fleet_instructions/default", `{}`, http.StatusUnprocessableEntity},
	} {
		t.Run(name, func(t *testing.T) {
			rec := call(c.mux, c.method, c.path, c.body)
			assert.Equal(t, c.want, rec.Code, rec.Body.String())
		})
	}
	all, err := f.svc.Load(f.ctx, fleetinstructions.KindFleet, true)
	require.NoError(t, err)
	assert.Len(t, all.Versions, 1, "none of the refused saves stored anything")
}

type codeHostFake struct{ connected bool }

func (c *codeHostFake) Connected(context.Context) (bool, error) { return c.connected, nil }

// filesFor is what workspace.Files writes for the tenant, which is what the
// briefings endpoint must show.
func filesFor(t *testing.T, fleet *workspace.FleetInstructions, changeTools bool) map[string]string {
	t.Helper()
	files, err := workspace.Files(workspace.Params{AgentName: fleetinstructions.AgentNamePlaceholder, Fleet: fleet, ChangeTools: changeTools})
	require.NoError(t, err)
	out := map[string]string{}
	for _, f := range files {
		out[f.Path] = string(f.Body)
	}
	return out
}

func briefings(t *testing.T, mux *http.ServeMux, query string) map[string]fleetinstructions.BriefingFile {
	t.Helper()
	rec := call(mux, http.MethodGet, "/api/fleet_instructions/briefings"+query, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body fleetinstructions.BriefingsBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	out := map[string]fleetinstructions.BriefingFile{}
	for _, f := range body.Files {
		out[f.Path] = f
	}
	return out
}

func TestHandler_BriefingsAreWhatWorkspaceFilesWrite(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	v1 := f.save("house", "Comment a plan first.\n\n## Not a heading", false)
	v2 := f.save("house", "Be brief.", true)
	member := f.server(f.member)

	for name, c := range map[string]struct {
		query       string
		fleet       *workspace.FleetInstructions
		changeTools bool
	}{
		"the latest version": {"?version_id=" + v2.ID.String(), &workspace.FleetInstructions{Name: "house", Version: 2, Body: v2.Body}, false},
		"a chosen version":   {"?version_id=" + v1.ID.String(), &workspace.FleetInstructions{Name: "house", Version: 1, Body: v1.Body}, false},
		"with a code host":   {"?version_id=" + v2.ID.String(), &workspace.FleetInstructions{Name: "house", Version: 2, Body: v2.Body}, true},
	} {
		t.Run(name, func(t *testing.T) {
			f.codeHost.connected = c.changeTools
			want := filesFor(t, c.fleet, c.changeTools)
			got := briefings(t, member, c.query)
			require.Len(t, got, 1)
			require.Contains(t, got, workspace.HeadlessBriefing)
			assert.Equal(t, "headless", got[workspace.HeadlessBriefing].Name)
			for path, file := range got {
				assert.Equal(t, want[path], file.Protocol+file.Fleet, path)
				assert.Equal(t, 1, strings.Count(file.Protocol+file.Fleet, "## Fleet instructions"), path)
				assert.Contains(t, file.Protocol, fleetinstructions.AgentNamePlaceholder, path)
			}
		})
	}
}

func TestHandler_BriefingsCarryNoFleetSectionWithoutAVersion(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	f.save("house", "text", true)

	got := briefings(t, f.server(f.member), "")
	want := filesFor(t, nil, false)
	for path, file := range got {
		assert.Equal(t, want[path], file.Protocol, path)
		assert.Empty(t, file.Fleet, path)
	}
}

func TestHandler_BriefingsRefusals(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	assert.Equal(t, http.StatusUnauthorized, call(f.server(nil), http.MethodGet, "/api/fleet_instructions/briefings", "").Code)
	assert.Equal(t, http.StatusNotFound, call(f.server(f.member), http.MethodGet,
		"/api/fleet_instructions/briefings?version_id=7f2c1c1e-0000-4000-8000-000000000000", "").Code)
}

// A version outlives its author's account, without their id.
func TestHandler_AVersionOfADeletedAuthorIsFlaggedAndNamesNoId(t *testing.T) {
	f := newTenant(t, dbtest.WithDB(t))
	f.save("house", "comment a plan first", true)
	_, err := f.db.Global().Exec(context.Background(), `DELETE FROM global_users WHERE id = $1`, f.owner.ID)
	require.NoError(t, err)

	rec := call(f.server(f.member), http.MethodGet, "/api/fleet_instructions", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), f.owner.ID.String())
	var got fleetinstructions.FleetInstructionsBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Versions, 1)
	assert.Nil(t, got.Versions[0].CreatedBy)
	assert.True(t, got.Versions[0].CreatedByDeleted)
}
