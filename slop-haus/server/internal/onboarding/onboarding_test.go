package onboarding_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/onboarding"
)

type fakeManifests struct {
	m   *manifest.Manifest
	err error
}

func (f fakeManifests) Get(context.Context) (*manifest.Manifest, error) { return f.m, f.err }

type fakeCodeHost struct {
	connected bool
	err       error
}

func (f fakeCodeHost) Connected(context.Context) (bool, error) { return f.connected, f.err }

func status(t *testing.T, m *manifest.Manifest, connected bool) (*onboarding.Service, *onboarding.Status) {
	t.Helper()
	svc := onboarding.NewService(fakeManifests{m: m}, fakeCodeHost{connected: connected})
	st, err := svc.Status(context.Background())
	require.NoError(t, err)
	return svc, st
}

func done(st *onboarding.Status) map[string]bool {
	out := map[string]bool{}
	for _, s := range st.Steps {
		out[s.Key] = s.Done
	}
	return out
}

func TestStatus_NewTenantHasNothingDone(t *testing.T) {
	svc, st := status(t, nil, false)

	assert.Equal(t, map[string]bool{"repos": false, "code_host": false}, done(st))
	assert.False(t, st.CodeWorkReady())
	assert.Contains(t, svc.SetupGap(context.Background()), "no repo")
}

func TestStatus_ManifestWithNoReposIsNotARepo(t *testing.T) {
	_, st := status(t, &manifest.Manifest{}, false)

	assert.False(t, done(st)["repos"])
}

func TestStatus_EitherStepLetsCodeWorkProceed(t *testing.T) {
	withRepo := &manifest.Manifest{Repos: []manifest.Repo{{Path: "app", Remote: "github.com/acme/app"}}}

	svc, st := status(t, withRepo, false)
	assert.Equal(t, map[string]bool{"repos": true, "code_host": false}, done(st))
	assert.True(t, st.CodeWorkReady())
	assert.Empty(t, svc.SetupGap(context.Background()))
	assert.Len(t, st.Missing(), 1, "the banner still lists the step left")

	svc, st = status(t, nil, true)
	assert.True(t, st.CodeWorkReady())
	assert.Empty(t, svc.SetupGap(context.Background()))
}

// A lookup that fails says nothing about the tenant's setup, so a claim is not
// refused for it; the board's read of the same state reports the error.
func TestStatus_ALookupThatFailsIsAnErrorAndNeverARefusedClaim(t *testing.T) {
	boom := errors.New("db down")
	for name, svc := range map[string]*onboarding.Service{
		"manifest":  onboarding.NewService(fakeManifests{err: boom}, fakeCodeHost{}),
		"code host": onboarding.NewService(fakeManifests{}, fakeCodeHost{err: boom}),
	} {
		_, err := svc.Status(context.Background())
		require.ErrorIs(t, err, boom, name)
		assert.Empty(t, svc.SetupGap(context.Background()), name)
	}
}
