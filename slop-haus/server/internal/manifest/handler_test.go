package manifest_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"log/slog"
)

// server mounts the real routes with the signed-in user the test picks, in
// place of the session middleware: what this checks is what the handler does
// with a user, not how one is found.
func (f *tenantFixture) server(who *authn.User) *http.ServeMux {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		if who != nil {
			ctx = huma.WithContext(ctx, authn.NewUserContext(ctx.Context(), who))
		}
		next(ctx)
	})
	manifest.RegisterRoutes(api, manifest.NewHandler(f.svc, slog.DiscardHandler))
	return mux
}

func call(mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

type workspaceResponse struct {
	Manifest *manifest.ManifestBody        `json:"manifest"`
	CanEdit  bool                          `json:"can_edit"`
	Proposal *manifest.Proposal            `json:"proposal"`
	Agents   []manifest.AgentWorkspaceBody `json:"agents"`
}

func getWorkspace(t *testing.T, mux *http.ServeMux) workspaceResponse {
	t.Helper()
	rec := call(mux, http.MethodGet, "/api/workspace", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out workspaceResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func TestGetWorkspace_ProposesFromReportsUntilThereIsAManifest(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	mono := func() *agent.Layout { return layout(false, "", repoEntry("mono", "https://github.com/acme/mono")) }
	a.newAgent("one", mono())
	a.newAgent("two", mono())
	a.newAgent("odd", layout(false, "", repoEntry("mono", "github.com/acme/fork")))
	a.newAgent("silent", nil)

	got := getWorkspace(t, a.server(a.owner))
	assert.Nil(t, got.Manifest, "the server never makes one up")
	assert.True(t, got.CanEdit)
	require.NotNil(t, got.Proposal)
	assert.Equal(t, []manifest.Repo{{Path: "mono", Remote: "github.com/acme/mono"}}, got.Proposal.Repos)
	require.Len(t, got.Proposal.Disagreements, 1)
	assert.Equal(t, "odd", got.Proposal.Disagreements[0].AgentName)
	require.Len(t, got.Agents, 4)
	for _, ag := range got.Agents {
		want := manifest.StatusReported
		if ag.AgentName == "silent" {
			want = manifest.StatusUnknown
		}
		assert.Equal(t, want, ag.Status, ag.AgentName)
		assert.NotNil(t, ag.Mismatches, "a list, never null")
	}

	rec := call(a.server(a.owner), http.MethodPut, "/api/workspace/manifest",
		`{"repos":[{"path":"mono","remote":"git@github.com:acme/mono.git"}]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got = getWorkspace(t, a.server(a.member))
	require.NotNil(t, got.Manifest)
	assert.Equal(t, "github.com/acme/mono", got.Manifest.Repos[0].Remote)
	assert.False(t, got.CanEdit, "a member sees it but cannot edit")
	assert.Nil(t, got.Proposal, "a proposal is only for a tenant with none")
	statuses := map[string]manifest.Status{}
	for _, ag := range got.Agents {
		statuses[ag.AgentName] = ag.Status
	}
	assert.Equal(t, map[string]manifest.Status{
		"one": manifest.StatusMatches, "two": manifest.StatusMatches,
		"odd": manifest.StatusMismatch, "silent": manifest.StatusUnknown,
	}, statuses)
}

func TestManifestRoutes_OwnersOnlyAndValidated(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	body := `{"repos":[{"path":"app","remote":"github.com/acme/app"}]}`

	assert.Equal(t, http.StatusForbidden, call(a.server(a.member), http.MethodPut, "/api/workspace/manifest", body).Code)
	assert.Equal(t, http.StatusForbidden, call(a.server(a.member), http.MethodDelete, "/api/workspace/manifest", "").Code)
	assert.Nil(t, getWorkspace(t, a.server(a.owner)).Manifest, "the member's write did nothing")

	assert.Equal(t, http.StatusOK, call(a.server(a.owner), http.MethodPut, "/api/workspace/manifest", body).Code)
	assert.Equal(t, http.StatusForbidden, call(a.server(a.member), http.MethodDelete, "/api/workspace/manifest", "").Code)
	require.NotNil(t, getWorkspace(t, a.server(a.owner)).Manifest, "nor did the member's delete")

	for name, bad := range map[string]string{
		"nested path": `{"repos":[{"path":"a/b","remote":"github.com/acme/app"}]}`,
		"no remote":   `{"repos":[{"path":"app","remote":""}]}`,
		"empty list":  `{"repos":[]}`,
	} {
		rec := call(a.server(a.owner), http.MethodPut, "/api/workspace/manifest", bad)
		assert.Contains(t, []int{http.StatusUnprocessableEntity, http.StatusBadRequest}, rec.Code, name)
	}
	assert.Equal(t, "github.com/acme/app", getWorkspace(t, a.server(a.owner)).Manifest.Repos[0].Remote, "invalid writes changed nothing")

	assert.Equal(t, http.StatusNoContent, call(a.server(a.owner), http.MethodDelete, "/api/workspace/manifest", "").Code)
	assert.Nil(t, getWorkspace(t, a.server(a.owner)).Manifest)
}

func TestWorkspaceRoutes_NeedASignedInUser(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	assert.Equal(t, http.StatusUnauthorized, call(a.server(nil), http.MethodGet, "/api/workspace", "").Code)
	assert.Equal(t, http.StatusUnauthorized, call(a.server(nil), http.MethodPut, "/api/workspace/manifest",
		`{"repos":[{"path":"app","remote":"github.com/a/b"}]}`).Code)
}

type slotManifestResponse struct {
	Revision int64                   `json:"revision"`
	Repos    []manifest.SlotRepoBody `json:"repos"`
}

func TestGetManifestForSlot_RevisionAndPathRemoteBaseOnly(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	mux := a.server(nil)
	// The real route gets its tenant from the credential's middleware.
	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/workspace/manifest/slot", nil).WithContext(a.ctx))
		return rec
	}

	rec := get()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var none slotManifestResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &none))
	assert.Zero(t, none.Revision)
	assert.NotNil(t, none.Repos, "no manifest: an empty list, not null or an error")
	assert.Empty(t, none.Repos)

	set, err := a.svc.Set(a.ctx, a.owner, []manifest.Repo{
		{Path: "app", Remote: "github.com/acme/app", BaseBranch: "slop-haus-acme",
			SharedPaths: []manifest.SharedPathRule{{Path: "gen/", Resolve: "regenerate"}}},
		{Path: "deploy", Remote: "github.com/acme/deploy"},
	})
	require.NoError(t, err)

	rec = get()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got slotManifestResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, set.Revision, got.Revision)
	assert.Equal(t, []manifest.SlotRepoBody{
		{Path: "app", Remote: "github.com/acme/app", Base: "slop-haus-acme"},
		{Path: "deploy", Remote: "github.com/acme/deploy"},
	}, got.Repos)
	var raw struct {
		Repos []map[string]any `json:"repos"`
	}
	var top map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &top))
	assert.ElementsMatch(t, []string{"$schema", "revision", "repos"}, mapKeys(top), "nothing beyond the revision and repos")
	for _, r := range raw.Repos {
		assert.ElementsMatch(t, []string{"path", "remote", "base"}, mapKeys(r), "nothing beyond path, remote and base")
	}
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
