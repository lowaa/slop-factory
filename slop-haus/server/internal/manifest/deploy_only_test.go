package manifest_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

const tagLines = `\s*newTag: \S+`

func withDeployOnly(rules ...manifest.DeployOnlyRule) []manifest.Repo {
	return []manifest.Repo{{Path: "app", Remote: "github.com/acme/mono", DeployOnly: rules}}
}

func TestNormalise_KeepsDeployOnlyRules(t *testing.T) {
	got, err := manifest.Normalise(withDeployOnly(
		manifest.DeployOnlyRule{Path: " deploy/envs/*/kustomization.yaml ", Lines: tagLines},
		manifest.DeployOnlyRule{Path: "deploy/pins.yaml"},
	))

	require.NoError(t, err)
	assert.Equal(t, []manifest.DeployOnlyRule{
		{Path: "deploy/envs/*/kustomization.yaml", Lines: tagLines},
		{Path: "deploy/pins.yaml"},
	}, got[0].DeployOnly)

	got, err = manifest.Normalise(withDeployOnly())
	require.NoError(t, err)
	assert.Nil(t, got[0].DeployOnly, "no rules is none, as a repo without the field")
}

func TestNormalise_RefusesDeployOnlyRulesTheGateCouldNotApply(t *testing.T) {
	tooMany := make([]manifest.DeployOnlyRule, manifest.MaxDeployOnly+1)
	for i := range tooMany {
		tooMany[i] = manifest.DeployOnlyRule{Path: "f" + strings.Repeat("x", i)}
	}
	for name, rules := range map[string][]manifest.DeployOnlyRule{
		"too many":          tooMany,
		"empty path":        {{Path: " "}},
		"absolute":          {{Path: "/deploy/k.yaml"}},
		"a directory":       {{Path: "deploy/"}},
		"parent segment":    {{Path: "deploy/../src/main.go"}},
		"dot segment":       {{Path: "./deploy/k.yaml"}},
		"empty segment":     {{Path: "deploy//k.yaml"}},
		"bad pattern":       {{Path: "deploy/[k.yaml"}},
		"control in path":   {{Path: "deploy/\x1bk.yaml"}},
		"path too long":     {{Path: strings.Repeat("a", manifest.MaxDeployOnlyText+1)}},
		"bad lines":         {{Path: "deploy/k.yaml", Lines: "newTag: ("}},
		"lines over a line": {{Path: "deploy/k.yaml", Lines: "a\nb"}},
		"lines too long":    {{Path: "deploy/k.yaml", Lines: strings.Repeat("a", manifest.MaxDeployOnlyText+1)}},
	} {
		_, err := manifest.Normalise(withDeployOnly(rules...))
		assert.ErrorIs(t, err, manifest.ErrInvalid, name)
	}
}

// What the gate reads is what an owner stored, through the same policy the
// ticket and change services hold.
func TestDeployOnly_IsStoredAndPlacedWithItsRepo(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	_, err := a.svc.Set(a.ctx, a.owner, []manifest.Repo{
		{Path: "app", Remote: "github.com/acme/mono", DeployOnly: []manifest.DeployOnlyRule{{Path: "deploy/*/k.yaml", Lines: tagLines}}},
		{Path: "infra", Remote: "github.com/acme/infra"},
	})
	require.NoError(t, err)

	got, err := a.svc.Get(a.ctx)
	require.NoError(t, err)
	assert.Equal(t, []manifest.DeployOnlyRule{{Path: "deploy/*/k.yaml", Lines: tagLines}}, got.Repos[0].DeployOnly)
	assert.Nil(t, got.Repos[1].DeployOnly)

	policy := manifest.NewPolicy(manifest.NewPostgresStore(e.database))
	pl, err := policy.Place(a.ctx, []string{"app/src/"})
	require.NoError(t, err)
	require.Len(t, pl.Repos, 1)
	assert.Equal(t, []scope.DeployOnlyRule{{Path: "deploy/*/k.yaml", Lines: tagLines}}, pl.Repos[0].DeployOnly)
	assert.Equal(t, []scope.WorkspaceRepo{
		{Path: "app", Remote: "github.com/acme/mono", DeployOnly: []scope.DeployOnlyRule{{Path: "deploy/*/k.yaml", Lines: tagLines}}},
		{Path: "infra", Remote: "github.com/acme/infra"},
	}, pl.All, "every repo of the placement carries its own rules")
	pl, err = policy.Place(a.ctx, []string{"infra/x/"})
	require.NoError(t, err)
	require.Len(t, pl.Repos, 1)
	assert.Nil(t, pl.Repos[0].DeployOnly, "a repo's rules are its own")
}

// The rules loosen the merge gate, so a member cannot widen them: they are
// part of the manifest, which only an owner changes.
func TestDeployOnly_OnlyAnOwnerSetsThem(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	_, err := a.svc.Set(a.ctx, a.owner, withDeployOnly(manifest.DeployOnlyRule{Path: "deploy/k.yaml", Lines: tagLines}))
	require.NoError(t, err)

	_, err = a.svc.Set(a.ctx, a.member, withDeployOnly(manifest.DeployOnlyRule{Path: "*"}, manifest.DeployOnlyRule{Path: "*/*"}))

	require.ErrorIs(t, err, manifest.ErrNotOwner)
	got, err := a.svc.Get(a.ctx)
	require.NoError(t, err)
	assert.Equal(t, []manifest.DeployOnlyRule{{Path: "deploy/k.yaml", Lines: tagLines}}, got.Repos[0].DeployOnly)
}
