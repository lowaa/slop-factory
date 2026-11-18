package manifest_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
)

type fixedManifest struct {
	manifest.Store
	m *manifest.Manifest
}

func (f fixedManifest) Get(context.Context) (*manifest.Manifest, error) { return f.m, nil }

func TestPolicy_PlacementsCarryTheManifestsBaseBranch(t *testing.T) {
	m := &manifest.Manifest{Repos: []manifest.Repo{
		{Path: "app", Remote: "git.example/acme/app", BaseBranch: "slop-haus-acme"},
		{Path: "web", Remote: "git.example/acme/web"},
	}}
	pl, err := manifest.NewPolicy(fixedManifest{m: m}).Place(context.Background(), []string{"app/server/"})

	require.NoError(t, err)
	require.Len(t, pl.Repos, 1)
	assert.Equal(t, "slop-haus-acme", pl.Repos[0].BaseBranch)
	require.Len(t, pl.All, 2)
	assert.Equal(t, "slop-haus-acme", pl.All[0].BaseBranch)
	assert.Empty(t, pl.All[1].BaseBranch)
}
