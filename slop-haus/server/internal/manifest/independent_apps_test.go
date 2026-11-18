package manifest_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
)

func withApps(enforce bool, apps ...string) []manifest.Repo {
	return []manifest.Repo{{Path: "app", Remote: "github.com/acme/mono", IndependentApps: apps, SkipRetestAcrossApps: enforce}}
}

func TestNormalise_KeepsIndependentAppsAsDirectoryPrefixes(t *testing.T) {
	got, err := manifest.Normalise(withApps(true, " slop-haus/ ", "billing", "a/b/"))

	require.NoError(t, err)
	assert.Equal(t, []string{"slop-haus/", "billing/", "a/b/"}, got[0].IndependentApps)
	assert.True(t, got[0].SkipRetestAcrossApps)
}

func TestNormalise_AnEmptyAppsListStaysEmptyAndTheFlagDefaultsOff(t *testing.T) {
	got, err := manifest.Normalise(withApps(false))

	require.NoError(t, err)
	assert.Empty(t, got[0].IndependentApps)
	assert.False(t, got[0].SkipRetestAcrossApps)
}

func TestNormalise_RefusesIndependentAppsThatCouldNotBeApplied(t *testing.T) {
	tooMany := make([]string, manifest.MaxIndependentApps+1)
	for i := range tooMany {
		tooMany[i] = "d" + strings.Repeat("x", i)
	}
	for name, apps := range map[string][]string{
		"too many":         tooMany,
		"empty":            {" "},
		"the root":         {"/"},
		"absolute":         {"/app/"},
		"parent segment":   {"a/../b/"},
		"dot segment":      {"./a/"},
		"empty segment":    {"a//b/"},
		"double slash":     {"a//"},
		"pattern":          {"app*/"},
		"bracket":          {"app[1]/"},
		"control":          {"a\x1b/"},
		"too long":         {strings.Repeat("a", manifest.MaxDeployOnlyText+1) + "/"},
		"duplicate":        {"a/", "a/"},
		"duplicate sloppy": {"a/", " a"},
	} {
		_, err := manifest.Normalise(withApps(false, apps...))
		assert.ErrorIs(t, err, manifest.ErrInvalid, name)
	}
}

func TestIndependentApps_AreStoredAndPlacedWithTheirRepo(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	_, err := a.svc.Set(a.ctx, a.owner, []manifest.Repo{
		{Path: "app", Remote: "github.com/acme/mono", IndependentApps: []string{"slop-haus/", "billing/"}, SkipRetestAcrossApps: true},
		{Path: "infra", Remote: "github.com/acme/infra"},
	})
	require.NoError(t, err)

	got, err := a.svc.Get(a.ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"slop-haus/", "billing/"}, got.Repos[0].IndependentApps)
	assert.True(t, got.Repos[0].SkipRetestAcrossApps)
	assert.Empty(t, got.Repos[1].IndependentApps)
	assert.False(t, got.Repos[1].SkipRetestAcrossApps, "off unless an owner turns it on")

	pl, err := manifest.NewPolicy(manifest.NewPostgresStore(e.database)).Place(a.ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"slop-haus/", "billing/"}, pl.All[0].IndependentApps)
	assert.True(t, pl.All[0].SkipRetestAcrossApps)
	assert.Empty(t, pl.All[1].IndependentApps)

	pl, err = manifest.NewPolicy(manifest.NewPostgresStore(e.database)).Place(a.ctx, []string{"app/slop-haus/x.go"})
	require.NoError(t, err)
	require.Len(t, pl.Repos, 1)
	assert.Equal(t, []string{"slop-haus/", "billing/"}, pl.Repos[0].IndependentApps)
	assert.True(t, pl.Repos[0].SkipRetestAcrossApps)
}

func TestIndependentApps_OnlyAnOwnerSetsThem(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	_, err := a.svc.Set(a.ctx, a.owner, withApps(false, "slop-haus/"))
	require.NoError(t, err)

	_, err = a.svc.Set(a.ctx, a.member, withApps(true, "slop-haus/", "billing/"))

	require.ErrorIs(t, err, manifest.ErrNotOwner)
	got, err := a.svc.Get(a.ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"slop-haus/"}, got.Repos[0].IndependentApps)
	assert.False(t, got.Repos[0].SkipRetestAcrossApps)
}

func TestDiffNamesIndependentAppsAndTheFlag(t *testing.T) {
	old := withApps(false, "a/", "b/")
	next := withApps(true, "b/", "c/")

	got := manifest.Diff(old, next)

	require.Len(t, got, 1)
	assert.Equal(t, []string{"c/"}, got[0].IndependentAppsAdded)
	assert.Equal(t, []string{"a/"}, got[0].IndependentAppsRemoved)
	assert.Equal(t, &manifest.FlagChange{Old: false, New: true}, got[0].SkipRetestAcrossApps)

	onlyFlag := manifest.Diff(withApps(false, "a/"), withApps(true, "a/"))
	require.Len(t, onlyFlag, 1, "a flipped flag alone is a change an owner must see")
	assert.Equal(t, manifest.OpChanged, onlyFlag[0].Op)
	assert.Empty(t, manifest.Diff(old, old))

	off := manifest.Diff(withApps(true, "a/"), withApps(false, "a/"))
	require.Len(t, off, 1, "turning enforcement off is as visible as turning it on")
	assert.Equal(t, &manifest.FlagChange{Old: true, New: false}, off[0].SkipRetestAcrossApps)
}

func TestIndependentApps_ALegacyStoredFlagTurnsOnBothSwitches(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	_, err := a.svc.Set(a.ctx, a.owner, withApps(false, "slop-haus/"))
	require.NoError(t, err)
	_, err = a.db.Exec(a.ctx, `UPDATE workspace_manifests SET repos = $1::jsonb`,
		`[{"path":"app","remote":"github.com/acme/mono","independent_apps":["slop-haus/"],"enforce_independent_apps":true},
		  {"path":"infra","remote":"github.com/acme/infra","independent_apps":["x/"]}]`)
	require.NoError(t, err)

	got, err := a.svc.Get(a.ctx)

	require.NoError(t, err)
	assert.True(t, got.Repos[0].BatchWithinApps)
	assert.True(t, got.Repos[0].SkipRetestAcrossApps)
	assert.False(t, got.Repos[1].BatchWithinApps)
	assert.False(t, got.Repos[1].SkipRetestAcrossApps)
	pl, err := manifest.NewPolicy(manifest.NewPostgresStore(e.database)).Place(a.ctx, nil)
	require.NoError(t, err)
	assert.True(t, pl.All[0].BatchWithinApps)
	assert.True(t, pl.All[0].SkipRetestAcrossApps)
}

func TestIndependentApps_ALegacyFlagNeverOverridesAnExplicitSwitch(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	_, err := a.svc.Set(a.ctx, a.owner, withApps(false, "slop-haus/"))
	require.NoError(t, err)
	_, err = a.db.Exec(a.ctx, `UPDATE workspace_manifests SET repos = $1::jsonb`,
		`[{"path":"app","remote":"github.com/acme/mono","enforce_independent_apps":true,"batch_within_apps":false}]`)
	require.NoError(t, err)

	got, err := a.svc.Get(a.ctx)

	require.NoError(t, err)
	assert.False(t, got.Repos[0].BatchWithinApps)
	assert.True(t, got.Repos[0].SkipRetestAcrossApps)
}

func TestIndependentApps_SwitchesAreStoredIndependently(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	_, err := a.svc.Set(a.ctx, a.owner, []manifest.Repo{{Path: "app", Remote: "github.com/acme/mono", IndependentApps: []string{"x/"}, BatchWithinApps: true}})
	require.NoError(t, err)

	got, err := a.svc.Get(a.ctx)

	require.NoError(t, err)
	assert.True(t, got.Repos[0].BatchWithinApps)
	assert.False(t, got.Repos[0].SkipRetestAcrossApps)
}

func TestDiff_EachSwitchIsItsOwnChange(t *testing.T) {
	base := []manifest.Repo{{Path: "app", Remote: "github.com/acme/mono", IndependentApps: []string{"a/"}}}
	batch := []manifest.Repo{{Path: "app", Remote: "github.com/acme/mono", IndependentApps: []string{"a/"}, BatchWithinApps: true}}

	got := manifest.Diff(base, batch)

	require.Len(t, got, 1)
	assert.Equal(t, &manifest.FlagChange{Old: false, New: true}, got[0].BatchWithinApps)
	assert.Nil(t, got[0].SkipRetestAcrossApps)
}

func TestIndependentApps_AMoveKeepsLegacyRowsSwitchesOn(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	_, err := a.svc.Set(a.ctx, a.owner, withApps(false, "slop-haus/"))
	require.NoError(t, err)
	_, err = a.db.Exec(a.ctx, `UPDATE workspace_manifests SET repos = $1::jsonb`,
		`[{"path":"app","remote":"github.com/acme/mono","independent_apps":["slop-haus/"],"enforce_independent_apps":true}]`)
	require.NoError(t, err)

	_, err = a.svc.Move(a.ctx, a.owner, manifest.MoveRequest{From: "app", To: "mono"}, false)

	require.NoError(t, err)
	got, err := a.svc.Get(a.ctx)
	require.NoError(t, err)
	assert.Equal(t, "mono", got.Repos[0].Path)
	assert.True(t, got.Repos[0].BatchWithinApps)
	assert.True(t, got.Repos[0].SkipRetestAcrossApps)
}
