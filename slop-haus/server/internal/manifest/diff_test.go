package manifest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
)

func TestDiffIsEmptyForTheSameManifest(t *testing.T) {
	repos := []manifest.Repo{{Path: "app", Remote: "github.com/a/b", DeployOnly: []manifest.DeployOnlyRule{{Path: "x"}}}}
	assert.Empty(t, manifest.Diff(repos, repos))
	assert.Empty(t, manifest.Diff(nil, nil))
}

func TestDiffNamesWhatEachRepoGainsAndLoses(t *testing.T) {
	old := []manifest.Repo{
		{Path: "app", Remote: "github.com/a/app",
			DeployOnly:  []manifest.DeployOnlyRule{{Path: "k/*.yaml", Lines: "a"}, {Path: "keep"}},
			SharedPaths: []manifest.SharedPathRule{{Path: "gen/", Resolve: "regenerate"}, {Path: "db/", Resolve: "migration"}, {Path: "old/", Resolve: "migration"}}},
		{Path: "gone", Remote: "github.com/a/gone", DeployOnly: []manifest.DeployOnlyRule{{Path: "z"}}},
	}
	next := []manifest.Repo{
		{Path: "app", Remote: "github.com/a/app2",
			DeployOnly:  []manifest.DeployOnlyRule{{Path: "k/*.yaml", Lines: "b"}, {Path: "keep"}},
			SharedPaths: []manifest.SharedPathRule{{Path: "gen/", Resolve: "regenerate"}, {Path: "db/", Resolve: "regenerate"}, {Path: "new/", Resolve: "migration"}}},
		{Path: "fresh", Remote: "github.com/a/fresh", SharedPaths: []manifest.SharedPathRule{{Path: "g/", Resolve: "regenerate"}}},
	}

	got := manifest.Diff(old, next)
	require.Len(t, got, 3)
	assert.Equal(t, []string{"app", "fresh", "gone"}, []string{got[0].Path, got[1].Path, got[2].Path}, "in path order")

	app := got[0]
	assert.Equal(t, manifest.OpChanged, app.Op)
	assert.Equal(t, "github.com/a/app", app.OldRemote)
	assert.Equal(t, "github.com/a/app2", app.NewRemote)
	assert.Equal(t, []manifest.DeployOnlyRule{{Path: "k/*.yaml", Lines: "b"}}, app.DeployOnlyAdded, "a changed pattern is a removal and an addition")
	assert.Equal(t, []manifest.DeployOnlyRule{{Path: "k/*.yaml", Lines: "a"}}, app.DeployOnlyRemoved)
	assert.Equal(t, []manifest.SharedPathRule{{Path: "new/", Resolve: "migration"}}, app.SharedPathsAdded)
	assert.Equal(t, []manifest.SharedPathRule{{Path: "old/", Resolve: "migration"}}, app.SharedPathsRemoved)
	assert.Equal(t, []manifest.SharedPathResolve{{Path: "db/", Old: "migration", New: "regenerate"}}, app.SharedPathsResolved)

	assert.Equal(t, manifest.OpAdded, got[1].Op)
	assert.Equal(t, "github.com/a/fresh", got[1].NewRemote)
	assert.Empty(t, got[1].OldRemote)
	assert.Len(t, got[1].SharedPathsAdded, 1)

	assert.Equal(t, manifest.OpRemoved, got[2].Op)
	assert.Equal(t, "github.com/a/gone", got[2].OldRemote)
	assert.Empty(t, got[2].NewRemote)
	assert.Len(t, got[2].DeployOnlyRemoved, 1)
}

func TestDiffAgainstNoManifestAddsEverything(t *testing.T) {
	got := manifest.Diff(nil, []manifest.Repo{{Path: "app", Remote: "github.com/a/b"}})
	require.Len(t, got, 1)
	assert.Equal(t, manifest.OpAdded, got[0].Op)
}
