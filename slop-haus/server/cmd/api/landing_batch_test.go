package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
)

func TestManifestPolicy_BatchesOnlyWithinEnforcedIndependentApps(t *testing.T) {
	t.Parallel()
	p := newManifestPolicy(&manifest.Manifest{Repos: []manifest.Repo{
		{Path: "mono", IndependentApps: []string{"a/", "b/"}, BatchWithinApps: true},
		{Path: "other", IndependentApps: []string{"c/"}},
	}})

	app, ok := p.App([]string{"mono/a/server/x.go", "mono/a/web/y.ts"})
	assert.True(t, ok)
	assert.Equal(t, "mono/a/", app)
	_, ok = p.App([]string{"mono/a/x.go", "mono/b/y.go"})
	assert.False(t, ok, "two apps are a batch of one")
	_, ok = p.App([]string{"other/c/x.go"})
	assert.False(t, ok, "an app whose repo does not enforce is not independent")

	assert.False(t, p.Solo("a/server/x.go"))
	assert.True(t, p.Solo(".woodpecker/ci.yaml"), "CI's definition is outside every app")
	assert.True(t, p.Solo("c/x.go"))
}

func TestManifestPolicy_NoManifestBatchesNothing(t *testing.T) {
	t.Parallel()
	p := newManifestPolicy(nil)

	_, ok := p.App([]string{"mono/a/x.go"})
	assert.False(t, ok)
	assert.True(t, p.Solo("a/x.go"))
}

func TestManifestPolicy_AppsEnforcedInTwoReposBatchNothing(t *testing.T) {
	t.Parallel()
	p := newManifestPolicy(&manifest.Manifest{Repos: []manifest.Repo{
		{Path: "one", IndependentApps: []string{"a/"}, BatchWithinApps: true},
		{Path: "two", IndependentApps: []string{"a/"}, BatchWithinApps: true},
	}})

	_, ok := p.App([]string{"one/a/x.go"})
	assert.False(t, ok)
	assert.True(t, p.Solo("a/x.go"))
}

type stubManifests struct {
	manifest.Store
	m *manifest.Manifest
}

func (s stubManifests) Get(context.Context) (*manifest.Manifest, error) { return s.m, nil }

type stubFiles map[int64][]string

func (s stubFiles) ChangedFiles(_ context.Context, row *landing.LandingRow) ([]string, error) {
	return s[row.TicketID], nil
}

func TestManifestBatchPolicy_SelectsOnlyChangesWithinTheApp(t *testing.T) {
	t.Parallel()
	m := &manifest.Manifest{Repos: []manifest.Repo{{Path: "mono", IndependentApps: []string{"a/"}, BatchWithinApps: true}}}
	sel := landing.NewBatchSelectorFrom(manifestBatchPolicy{manifests: stubManifests{m: m}},
		stubFiles{1: {"a/x.go"}, 2: {"a/y.go"}, 3: {"a/z.go", ".woodpecker/ci.yaml"}})
	cand := func(id int64) landing.Candidate {
		return landing.Candidate{Row: &landing.LandingRow{TicketID: id}, Paths: []string{"mono/a/"}}
	}

	got, err := sel.Select(context.Background(), []landing.Candidate{cand(1), cand(3), cand(2)}, 3)

	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, []int64{1, 2}, []int64{got[0].Row.TicketID, got[1].Row.TicketID}, "the change touching CI's definition is left for its own batch")
}

func TestManifestPolicy_TheRetestSwitchAloneNeverBatches(t *testing.T) {
	t.Parallel()
	p := newManifestPolicy(&manifest.Manifest{Repos: []manifest.Repo{
		{Path: "mono", IndependentApps: []string{"a/"}, SkipRetestAcrossApps: true},
	}})

	_, ok := p.App([]string{"mono/a/x.go"})
	assert.False(t, ok)
}

func TestManifestPolicy_MigrationIsASharedPathResolvedAsMigration(t *testing.T) {
	t.Parallel()
	p := newManifestPolicy(&manifest.Manifest{Repos: []manifest.Repo{{
		Path: "mono", IndependentApps: []string{"a/"}, BatchWithinApps: true,
		SharedPaths: []manifest.SharedPathRule{
			{Path: "a/migrations/", Resolve: "migration"},
			{Path: "a/gen/", Resolve: "regenerate"},
		},
	}}})

	dir, ok := p.Migration("a/migrations/0002_x.sql")
	assert.True(t, ok)
	assert.Equal(t, "a/migrations/", dir)
	_, ok = p.Migration("a/gen/x.go")
	assert.False(t, ok)
}
