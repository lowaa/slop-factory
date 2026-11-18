package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/config"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
)

func TestLandingPipelines_NoWoodpeckerFallsBackToTheCodeHostsChecks(t *testing.T) {
	t.Parallel()
	p, err := landingPipelines(&config.Config{}, &landing.Config{FallbackChecksTimeout: 9 * time.Minute})
	require.NoError(t, err)

	assert.ErrorIs(t, p.Retry(t.Context(), "abc"), landing.ErrNoRetry)
	timeout, err := p.RepoTimeout(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 9*time.Minute, timeout)
}

func TestLandingPipelines_WoodpeckerReadsItsTokenFromTheFile(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(file, []byte("tok\n"), 0o600))
	cfg := &config.Config{Woodpecker: config.WoodpeckerConfig{URL: "https://ci.example.com", TokenFile: file, RepoID: 7}}

	p, err := landingPipelines(cfg, &landing.Config{})
	require.NoError(t, err)

	assert.IsType(t, &landing.WoodpeckerPipelines{}, p)
}

func TestLandingPipelines_AnUnreadableTokenFileRefusesTheStart(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Woodpecker: config.WoodpeckerConfig{URL: "https://ci.example.com", TokenFile: filepath.Join(t.TempDir(), "missing"), RepoID: 7}}

	_, err := landingPipelines(cfg, &landing.Config{})

	assert.Error(t, err)
}

func TestMergeMethodIsARegisteredSetting(t *testing.T) {
	t.Parallel()
	require.NoError(t, registerSettings())

	v, err := settings.Default(landing.SettingMergeMethod)

	require.NoError(t, err)
	assert.Equal(t, landing.MergeMethodWorker, v.Str)
}

// Without Guidance the runner hands conflicts and red checks back with generic
// text, so the wiring is worth a test of its own.
func TestLandingOptions_SetGuidanceSoHandBacksNameTheCollidingChange(t *testing.T) {
	t.Parallel()
	database := dbtest.WithDB(t)

	o := landingOptions(&landingDeps{
		cfg: &config.Config{}, database: database, svcs: &services{}, b: &board{},
		logHandler: slog.DiscardHandler,
	})

	assert.NotNil(t, o.Guidance)
}
