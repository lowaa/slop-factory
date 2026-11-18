package manifest_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
)

func withProtected(rules ...manifest.ProtectedPathRule) []manifest.Repo {
	return []manifest.Repo{{Path: "app", Remote: "github.com/acme/mono", ProtectedPaths: rules}}
}

func TestNormalise_KeepsProtectedPaths(t *testing.T) {
	got, err := manifest.Normalise(withProtected(
		manifest.ProtectedPathRule{Path: " infrastructure/ ", Reason: " prod config "},
		manifest.ProtectedPathRule{Path: "*.env"},
		manifest.ProtectedPathRule{Path: "billing/server/internal/erasure*"},
	))

	require.NoError(t, err)
	assert.Equal(t, []manifest.ProtectedPathRule{
		{Path: "infrastructure/", Reason: "prod config"},
		{Path: "*.env"},
		{Path: "billing/server/internal/erasure*"},
	}, got[0].ProtectedPaths)
}

func TestNormalise_RefusesProtectedPathsTheMatcherCouldNotApply(t *testing.T) {
	tooMany := make([]manifest.ProtectedPathRule, manifest.MaxProtectedPaths+1)
	for i := range tooMany {
		tooMany[i] = manifest.ProtectedPathRule{Path: "d" + strings.Repeat("x", i) + "/"}
	}
	for name, rules := range map[string][]manifest.ProtectedPathRule{
		"too many":            tooMany,
		"empty path":          {{Path: " "}},
		"the root":            {{Path: "/"}},
		"absolute":            {{Path: "/db/"}},
		"parent segment":      {{Path: "db/../src/"}},
		"bad pattern":         {{Path: "gen/[a.ts"}},
		"pattern in a prefix": {{Path: "gen/*/"}},
		"control in path":     {{Path: "db/\x1b/"}},
		"listed twice":        {{Path: "db/"}, {Path: "db/", Reason: "again"}},
		"reason too long":     {{Path: "db/", Reason: strings.Repeat("a", manifest.MaxProtectedReason+1)}},
		"multi-line reason":   {{Path: "db/", Reason: "a\nb"}},
	} {
		_, err := manifest.Normalise(withProtected(rules...))
		assert.ErrorIs(t, err, manifest.ErrInvalid, name)
	}
}

func TestProtected_MatchesLikeSharedPaths(t *testing.T) {
	repos := []manifest.Repo{
		{Path: "app", Remote: "github.com/acme/mono", ProtectedPaths: []manifest.ProtectedPathRule{
			{Path: "infrastructure/", Reason: "prod"}, {Path: "*.env"}, {Path: "server/erasure*"}, {Path: "gen/*"},
		}},
		{Path: "web", Remote: "github.com/acme/web"},
	}
	for path, want := range map[string]string{
		"app/infrastructure/k8s/a.yaml": "infrastructure/",
		"app/infrastructure/":           "infrastructure/",
		"app/prod.env":                  "*.env",
		"app/server/erasure_job.go":     "server/erasure*",
	} {
		rule, ok := manifest.Protected(repos, path)
		require.True(t, ok, path)
		assert.Equal(t, want, rule.Path, path)
	}
	for _, path := range []string{
		"app/infrastructure",       // no slash: not under the directory
		"app/sub/prod.env",         // * does not cross a /
		"app/gen/",                 // a directory is not wholly inside a file pattern
		"web/infrastructure/a.yml", // another repo's rules do not apply
		"other/infrastructure/a",   // outside every repo
	} {
		_, ok := manifest.Protected(repos, path)
		assert.False(t, ok, path)
	}
}

func TestDiff_NamesProtectedPathsGainedAndLost(t *testing.T) {
	old := []manifest.Repo{{Path: "app", Remote: "github.com/a/app", ProtectedPaths: []manifest.ProtectedPathRule{
		{Path: "keep/", Reason: "x"}, {Path: "old/"}, {Path: "re/", Reason: "a"}}}}
	next := []manifest.Repo{{Path: "app", Remote: "github.com/a/app", ProtectedPaths: []manifest.ProtectedPathRule{
		{Path: "keep/", Reason: "x"}, {Path: "new/"}, {Path: "re/", Reason: "b"}}}}

	got := manifest.Diff(old, next)

	require.Len(t, got, 1)
	assert.Equal(t, []manifest.ProtectedPathRule{{Path: "new/"}, {Path: "re/", Reason: "b"}}, got[0].ProtectedPathsAdded)
	assert.Equal(t, []manifest.ProtectedPathRule{{Path: "old/"}, {Path: "re/", Reason: "a"}}, got[0].ProtectedPathsRemoved)
}

func TestProtectedPaths_AreStoredAndOnlyAnOwnerSetsThem(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	rule := manifest.ProtectedPathRule{Path: "infrastructure/", Reason: "prod"}
	_, err := a.svc.Set(a.ctx, a.owner, withProtected(rule))
	require.NoError(t, err)

	_, err = a.svc.Set(a.ctx, a.member, withProtected())

	require.ErrorIs(t, err, manifest.ErrNotOwner)
	got, err := a.svc.Get(a.ctx)
	require.NoError(t, err)
	assert.Equal(t, []manifest.ProtectedPathRule{rule}, got.Repos[0].ProtectedPaths)
}
