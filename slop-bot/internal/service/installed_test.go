package service_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/service"
)

// The unit and plist are read back to what was written, escaping included:
// a home with a space, %, $ and XML specials resolves to the same paths.
func TestServicePathsReadBackWhatInstallWrote(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, goos, home string }{
		{"systemd", "linux", ""},
		{"systemd_awkward", "linux", awkwardHome},
		{"launchd", "darwin", ""},
		{"launchd_awkward", "darwin", awkwardHome},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, &fixtureOpts{goos: tc.goos, homeName: tc.home,
				vars: map[string]string{"XDG_STATE_HOME": `~/st "te\%$`}})
			_, err := f.mgr.Install(context.Background())
			require.NoError(t, err)
			got, err := service.ServicePaths(f.mgr)
			require.NoError(t, err)
			assert.Equal(t, f.opts.Paths.Config.Path, got.Config.Path)
			assert.Equal(t, filepath.Join(f.home, `st "te\%$`, "slop-bot"), got.State.Path)
			assert.Equal(t, f.opts.Paths.Data.Path, got.Data.Path)
			assert.Equal(t, f.opts.Paths.Socket(), got.Socket())
			assert.Empty(t, service.Mismatches(f.opts.Paths, got))
		})
	}
}

func TestServicePathsBeforeInstallIsNotInstalled(t *testing.T) {
	t.Parallel()
	for _, goos := range []string{"linux", "darwin"} {
		f := newFixture(t, &fixtureOpts{goos: goos})
		_, err := service.ServicePaths(f.mgr)
		assert.ErrorIs(t, err, service.ErrNotInstalled, goos)
	}
}

// systemd gives the service logind's runtime dir whatever the installing
// shell had, so a shell with another one resolves a socket nobody serves.
func TestSystemdServiceUsesLoginsRuntimeDir(t *testing.T) {
	t.Parallel()
	f := newFixture(t, &fixtureOpts{goos: "linux", vars: map[string]string{"XDG_RUNTIME_DIR": "/tmp/elsewhere"}})
	_, err := f.mgr.Install(context.Background())
	require.NoError(t, err)
	got, err := service.ServicePaths(f.mgr)
	require.NoError(t, err)
	assert.Equal(t, "/run/user/1000/slop-bot/slop-bot.sock", got.Socket())
	assert.Equal(t, []service.Mismatch{{
		What: "control socket", CLI: "/tmp/elsewhere/slop-bot/slop-bot.sock",
		Service: "/run/user/1000/slop-bot/slop-bot.sock",
	}}, service.Mismatches(f.opts.Paths, got))
}

func TestMismatchesNamesEveryDifferingDir(t *testing.T) {
	t.Parallel()
	f := newFixture(t, &fixtureOpts{goos: "darwin"})
	_, err := f.mgr.Install(context.Background())
	require.NoError(t, err)
	svc, err := service.ServicePaths(f.mgr)
	require.NoError(t, err)
	// This shell has since exported its own data home.
	other := newFixture(t, &fixtureOpts{goos: "darwin", vars: map[string]string{"XDG_DATA_HOME": "/elsewhere"}})
	cli := *f.opts.Paths
	cli.Data = other.opts.Paths.Data
	assert.Equal(t, []service.Mismatch{{What: "data dir", CLI: "/elsewhere/slop-bot", Service: f.opts.Paths.Data.Path}},
		service.Mismatches(&cli, svc))
}

func TestSystemdPreflight(t *testing.T) {
	t.Parallel()
	f := newFixture(t, &fixtureOpts{goos: "linux"})
	require.NoError(t, f.mgr.Preflight(context.Background()))
	assert.Equal(t, []string{"systemctl --user show-environment"}, f.runner.Calls(), "it changes nothing")

	f.runner.Answer("systemctl --user show-environment", &service.Result{ExitCode: 1, Stderr: "Failed to connect to bus"})
	err := f.mgr.Preflight(context.Background())
	require.ErrorIs(t, err, service.ErrNoServiceManager)
	assert.Contains(t, err.Error(), "Failed to connect to bus")

	f.runner.Answer("systemctl --user show-environment", &service.Result{Stdout: "XDG_CONFIG_HOME=/elsewhere\n"})
	err = f.mgr.Preflight(context.Background())
	require.Error(t, err)
	assert.False(t, errors.Is(err, service.ErrNoServiceManager), "the manager answered")
	assert.Contains(t, err.Error(), "/elsewhere/systemd/user")
}

func TestLaunchdPreflight(t *testing.T) {
	t.Parallel()
	f := newFixture(t, &fixtureOpts{goos: "darwin"})
	require.NoError(t, f.mgr.Preflight(context.Background()))
	assert.Equal(t, []string{"launchctl print gui/1000"}, f.runner.Calls())

	f.runner.Answer("launchctl print gui/1000", &service.Result{ExitCode: 113, Stderr: "Could not find domain"})
	err := f.mgr.Preflight(context.Background())
	require.ErrorIs(t, err, service.ErrNoServiceManager)
	assert.Contains(t, err.Error(), "logged in at the Mac")
}
