package manifest_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

func withShared(rules ...manifest.SharedPathRule) []manifest.Repo {
	return []manifest.Repo{{Path: "app", Remote: "github.com/acme/mono", SharedPaths: rules}}
}

func TestNormalise_KeepsSharedPaths(t *testing.T) {
	got, err := manifest.Normalise(withShared(
		manifest.SharedPathRule{Path: " db/migrations/ ", Resolve: "migration"},
		manifest.SharedPathRule{Path: "web/gen/*.ts", Resolve: "regenerate"},
		manifest.SharedPathRule{Path: "docs/", Resolve: "merge"},
	))

	require.NoError(t, err)
	assert.Equal(t, []manifest.SharedPathRule{
		{Path: "db/migrations/", Resolve: "migration"},
		{Path: "web/gen/*.ts", Resolve: "regenerate"},
		{Path: "docs/", Resolve: "merge"},
	}, got[0].SharedPaths)
}

func TestNormalise_RefusesSharedPathsTheGuardCouldNotApply(t *testing.T) {
	tooMany := make([]manifest.SharedPathRule, manifest.MaxSharedPaths+1)
	for i := range tooMany {
		tooMany[i] = manifest.SharedPathRule{Path: "d" + strings.Repeat("x", i) + "/", Resolve: "migration"}
	}
	for name, rules := range map[string][]manifest.SharedPathRule{
		"too many":            tooMany,
		"unknown kind":        {{Path: "db/", Resolve: "ignore"}},
		"no kind":             {{Path: "db/"}},
		"empty path":          {{Path: " ", Resolve: "migration"}},
		"the root":            {{Path: "/", Resolve: "migration"}},
		"absolute":            {{Path: "/db/", Resolve: "migration"}},
		"parent segment":      {{Path: "db/../src/", Resolve: "migration"}},
		"empty segment":       {{Path: "db//m/", Resolve: "migration"}},
		"bad pattern":         {{Path: "gen/[a.ts", Resolve: "regenerate"}},
		"pattern in a prefix": {{Path: "gen/*/", Resolve: "regenerate"}},
		"control in path":     {{Path: "db/\x1b/", Resolve: "migration"}},
		"path too long":       {{Path: strings.Repeat("a", manifest.MaxDeployOnlyText) + "/", Resolve: "migration"}},
	} {
		_, err := manifest.Normalise(withShared(rules...))
		assert.ErrorIs(t, err, manifest.ErrInvalid, name)
	}
}

func TestSharedPaths_AreStoredAndPlacedWithTheirRepo(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	rule := manifest.SharedPathRule{Path: "db/migrations/", Resolve: "migration"}
	_, err := a.svc.Set(a.ctx, a.owner, []manifest.Repo{
		{Path: "app", Remote: "github.com/acme/mono", SharedPaths: []manifest.SharedPathRule{rule}},
		{Path: "infra", Remote: "github.com/acme/infra"},
	})
	require.NoError(t, err)

	got, err := a.svc.Get(a.ctx)
	require.NoError(t, err)
	assert.Equal(t, []manifest.SharedPathRule{rule}, got.Repos[0].SharedPaths)

	pl, err := manifest.NewPolicy(manifest.NewPostgresStore(e.database)).Place(a.ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, []scope.SharedPathRule{{Path: "db/migrations/", Resolve: scope.ResolveMigration}}, pl.All[0].Shared)
	assert.Nil(t, pl.All[1].Shared, "a repo's rules are its own")

	pl, err = manifest.NewPolicy(manifest.NewPostgresStore(e.database)).Place(a.ctx, []string{"app/db/migrations/001.sql"})
	require.NoError(t, err)
	require.Len(t, pl.Repos, 1)
	assert.Equal(t, []scope.SharedPathRule{{Path: "db/migrations/", Resolve: scope.ResolveMigration}}, pl.Repos[0].Shared)
	pl, err = manifest.NewPolicy(manifest.NewPostgresStore(e.database)).Place(a.ctx, []string{"infra/x/"})
	require.NoError(t, err)
	require.Len(t, pl.Repos, 1)
	assert.Nil(t, pl.Repos[0].Shared)
}

// Shared paths loosen the scope guard, so a member cannot add them: they are
// part of the manifest, which only an owner changes.
func TestSharedPaths_OnlyAnOwnerSetsThem(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	_, err := a.svc.Set(a.ctx, a.owner, withShared(manifest.SharedPathRule{Path: "db/migrations/", Resolve: "migration"}))
	require.NoError(t, err)

	_, err = a.svc.Set(a.ctx, a.member, withShared(manifest.SharedPathRule{Path: "src/", Resolve: "regenerate"}))

	require.ErrorIs(t, err, manifest.ErrNotOwner)
	got, err := a.svc.Get(a.ctx)
	require.NoError(t, err)
	assert.Equal(t, []manifest.SharedPathRule{{Path: "db/migrations/", Resolve: "migration"}}, got.Repos[0].SharedPaths)
}
