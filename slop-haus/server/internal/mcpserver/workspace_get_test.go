package mcpserver_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

// workspaceGetResult is the structured output, decoded as a client sees it.
type workspaceGetResult struct {
	Declared bool  `json:"declared"`
	Revision int64 `json:"revision"`
	Repos    []struct {
		Path       string `json:"path"`
		Remote     string `json:"remote"`
		BaseBranch string `json:"base_branch"`
		DeployOnly []struct {
			Path  string `json:"path"`
			Lines string `json:"lines"`
		} `json:"deploy_only"`
		ProtectedPaths []struct {
			Path   string `json:"path"`
			Reason string `json:"reason"`
		} `json:"protected_paths"`
		SharedPaths []struct {
			Path    string `json:"path"`
			Resolve string `json:"resolve"`
			OnClash string `json:"on_clash"`
		} `json:"shared_paths"`
		IndependentApps      []string `json:"independent_apps"`
		SkipRetestAcrossApps bool     `json:"skip_retest_across_apps"`
	} `json:"repos"`
	UpdatedAt string `json:"updated_at"`
	UpdatedBy string `json:"updated_by"`
}

func (c *consoleSession) workspace(t *testing.T) (workspaceGetResult, string) {
	t.Helper()
	res := c.call(t, "workspace_get", map[string]any{})
	require.False(t, res.IsError, resultText(t, res))
	raw, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	var out workspaceGetResult
	require.NoError(t, json.Unmarshal(raw, &out))
	return out, resultText(t, res)
}

func (r *realRig) declare(repos []manifest.Repo) {
	r.t.Helper()
	_, err := manifest.NewPostgresStore(r.database).Put(db.WithTenant(r.ctx, r.tenantID), repos, r.human)
	require.NoError(r.t, err)
}

func newManifestRigIn(t *testing.T, database *db.DB) *realRig {
	t.Helper()
	return newRealRigWith(t, database, &realRigOptions{manifests: manifest.NewPostgresStore(database)})
}

func TestWorkspaceGet_EveryAgentKindReadsItsOwnTenantsManifestOnly(t *testing.T) {
	database := dbtest.WithDB(t)
	mine, theirs := newManifestRigIn(t, database), newManifestRigIn(t, database)
	mine.declare([]manifest.Repo{{
		Path: "app", Remote: "github.com/acme/app", BaseBranch: "slop-haus-team-a",
		DeployOnly:      []manifest.DeployOnlyRule{{Path: "deploy/*/kustomization.yaml", Lines: `\s*newTag: \S+`}},
		SharedPaths:     []manifest.SharedPathRule{{Path: "db/migrations/", Resolve: scope.ResolveMigration}, {Path: "gen/*.go", Resolve: scope.ResolveRegenerate}},
		ProtectedPaths:  []manifest.ProtectedPathRule{{Path: "infrastructure/", Reason: "prod"}},
		IndependentApps: []string{"app/", "lib/"}, SkipRetestAcrossApps: true,
	}})
	theirs.declare([]manifest.Repo{{Path: "app", Remote: "github.com/other/secret-repo", BaseBranch: "slop-haus-team-b"}})

	sessions := map[string]*consoleSession{
		"worker":    mine.agentSession("worker", false),
		"console":   mine.agentSession("console", true),
		"conductor": mine.conductorSession("conductor"),
	}
	for kind, s := range sessions {
		t.Run(kind, func(t *testing.T) {
			got, shown := s.workspace(t)
			assert.True(t, got.Declared)
			require.Len(t, got.Repos, 1)
			repo := got.Repos[0]
			assert.Equal(t, "app", repo.Path)
			assert.Equal(t, "github.com/acme/app", repo.Remote)
			assert.Equal(t, "slop-haus-team-a", repo.BaseBranch)
			assert.NotContains(t, shown, "team-b")
			require.Len(t, repo.DeployOnly, 1)
			assert.Equal(t, "deploy/*/kustomization.yaml", repo.DeployOnly[0].Path)
			assert.Equal(t, `\s*newTag: \S+`, repo.DeployOnly[0].Lines)
			require.Len(t, repo.SharedPaths, 2)
			assert.Equal(t, "db/migrations/", repo.SharedPaths[0].Path)
			assert.Equal(t, scope.ResolveMigration, repo.SharedPaths[0].Resolve)
			assert.Equal(t, "gen/*.go", repo.SharedPaths[1].Path)
			assert.Equal(t, scope.ResolveRegenerate, repo.SharedPaths[1].Resolve)
			require.Len(t, repo.ProtectedPaths, 1)
			assert.Equal(t, "infrastructure/", repo.ProtectedPaths[0].Path)
			assert.Equal(t, "prod", repo.ProtectedPaths[0].Reason)
			assert.Contains(t, shown, "protected: infrastructure/ (prod)")
			assert.Equal(t, []string{"app/", "lib/"}, repo.IndependentApps)
			assert.True(t, repo.SkipRetestAcrossApps)
			assert.NotEmpty(t, got.UpdatedAt)
			assert.Equal(t, scope.ResolveAdvice(scope.ResolveMigration), repo.SharedPaths[0].OnClash)
			assert.Equal(t, scope.ResolveAdvice(scope.ResolveRegenerate), repo.SharedPaths[1].OnClash)
			assert.Equal(t, mine.human.String(), got.UpdatedBy)
			assert.Contains(t, shown, scope.ResolveAdvice(scope.ResolveMigration))
			assert.NotContains(t, shown, "secret-repo")
		})
	}
}

func TestWorkspaceGet_TheOtherTenantReadsItsOwnManifest(t *testing.T) {
	database := dbtest.WithDB(t)
	mine, theirs := newManifestRigIn(t, database), newManifestRigIn(t, database)
	mine.declare([]manifest.Repo{{Path: "app", Remote: "github.com/acme/app", IndependentApps: []string{"acme-app/"}, SkipRetestAcrossApps: true}})
	theirs.declare([]manifest.Repo{{Path: "app", Remote: "github.com/other/secret-repo"}})

	got, shown := theirs.agentSession("worker", false).workspace(t)
	require.Len(t, got.Repos, 1)
	assert.Equal(t, "github.com/other/secret-repo", got.Repos[0].Remote)
	assert.NotContains(t, shown, "acme")
	assert.NotNil(t, got.Repos[0].IndependentApps, "an undeclared list is [], not null")
	assert.Empty(t, got.Repos[0].IndependentApps, "the other tenant's list is not leaked")
	assert.False(t, got.Repos[0].SkipRetestAcrossApps)
	assert.NotContains(t, shown, "acme-app")
}

func TestWorkspaceGet_ADeletedOwnerLeavesUpdatedByOut(t *testing.T) {
	r := newManifestRigIn(t, dbtest.WithDB(t))
	r.declare([]manifest.Repo{{Path: "app", Remote: "github.com/acme/app"}})
	_, err := r.database.Global().Exec(r.ctx, `DELETE FROM global_users WHERE id = $1`, r.human)
	require.NoError(t, err)

	got, _ := r.agentSession("worker", false).workspace(t)
	assert.True(t, got.Declared)
	assert.Empty(t, got.UpdatedBy)
}

func TestWorkspaceGet_ATenantWithNoManifestGetsAnEmptyListNotAnError(t *testing.T) {
	r := newManifestRigIn(t, dbtest.WithDB(t))
	got, shown := r.agentSession("worker", false).workspace(t)
	assert.False(t, got.Declared)
	assert.NotNil(t, got.Repos)
	assert.Empty(t, got.Repos)
	assert.Contains(t, shown, "no workspace manifest")
}

func TestWorkspaceGet_IsOnlyOfferedWhenWired(t *testing.T) {
	r := newRealRig(t)
	_, err := r.agentSession("worker", false).session.CallTool(context.Background(), &mcp.CallToolParams{Name: "workspace_get"})
	require.Error(t, err)
}
