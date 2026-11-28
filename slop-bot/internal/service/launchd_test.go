package service_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/service"
)

const (
	printCmd    = "launchctl print gui/1000/dev.slop-bot.daemon"
	disabledCmd = "launchctl print-disabled gui/1000"
	plistName   = "dev.slop-bot.daemon.plist"
)

// notLoaded is `launchctl print` for a job that isn't bootstrapped.
var notLoaded = &service.Result{ExitCode: 113, Stderr: `Could not find service "dev.slop-bot.daemon" in domain for user gui: 1000`}

var loadedRunning = &service.Result{Stdout: "gui/1000/dev.slop-bot.daemon = {\n\tactive count = 1\n\tstate = running\n\tprogram = /x\n}\n"}

func TestLaunchdInstallWritesThePlistAndBootstrapsIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t, &fixtureOpts{goos: "darwin"})
	f.runner.Answer(printCmd, notLoaded)

	res, err := f.mgr.Install(context.Background())
	require.NoError(t, err)

	want := filepath.Join(f.home, "Library", "LaunchAgents", plistName)
	assert.Equal(t, want, res.UnitPath)
	assert.FileExists(t, want)
	assert.DirExists(t, f.opts.Paths.LogDir(), "launchd won't create the log directory")
	assert.Equal(t, []string{
		printCmd,
		"launchctl enable gui/1000/dev.slop-bot.daemon",
		"launchctl bootstrap gui/1000 " + want,
	}, f.runner.Calls())
	assert.Equal(t, service.LingerNotApplicable, res.Linger)
	assert.Equal(t, []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "PATH"},
		envNames(res.Env))
	assert.Contains(t, res.Env, service.EnvVar{Name: "XDG_RUNTIME_DIR", Value: filepath.Join(f.home, "tmp")},
		"the resolved runtime dir, so the daemon's socket is where the CLI looks")
}

func TestLaunchdReinstallBootsTheOldJobOutFirst(t *testing.T) {
	t.Parallel()
	f := newFixture(t, &fixtureOpts{goos: "darwin"})
	f.runner.Answer(printCmd, loadedRunning)
	_, err := f.mgr.Install(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{
		printCmd,
		"launchctl bootout gui/1000/dev.slop-bot.daemon",
		"launchctl enable gui/1000/dev.slop-bot.daemon",
		"launchctl bootstrap gui/1000 " + f.mgr.UnitPath(),
	}, f.runner.Calls())
}

func TestLaunchdStartAndStop(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		printed   *service.Result
		wantStart []string
		wantStop  []string
	}{
		{
			name: "loaded", printed: loadedRunning,
			wantStart: []string{printCmd, "launchctl kickstart gui/1000/dev.slop-bot.daemon"},
			wantStop:  []string{printCmd, "launchctl bootout gui/1000/dev.slop-bot.daemon"},
		},
		{
			name: "not loaded", printed: notLoaded,
			wantStart: []string{printCmd, "launchctl bootstrap gui/1000 PLIST"},
			wantStop:  []string{printCmd},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, &fixtureOpts{goos: "darwin"})
			ctx := context.Background()
			_, err := f.mgr.Install(ctx)
			require.NoError(t, err)
			f.runner.Answer(printCmd, tc.printed)

			f.runner.Reset()
			require.NoError(t, f.mgr.Start(ctx))
			assert.Equal(t, withPlist(tc.wantStart, f.mgr.UnitPath()), f.runner.Calls())
			f.runner.Reset()
			require.NoError(t, f.mgr.Stop(ctx))
			assert.Equal(t, tc.wantStop, f.runner.Calls())
		})
	}
}

func TestLaunchdUninstall(t *testing.T) {
	t.Parallel()
	f := newFixture(t, &fixtureOpts{goos: "darwin"})
	ctx := context.Background()
	assert.ErrorIs(t, f.mgr.Uninstall(ctx), service.ErrNotInstalled)
	_, err := f.mgr.Install(ctx)
	require.NoError(t, err)
	f.runner.Answer(printCmd, loadedRunning)
	f.runner.Reset()
	require.NoError(t, f.mgr.Uninstall(ctx))
	assert.Equal(t, []string{printCmd, "launchctl bootout gui/1000/dev.slop-bot.daemon"}, f.runner.Calls())
	assert.NoFileExists(t, f.mgr.UnitPath())
}

func TestLaunchdStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		install  bool
		printed  *service.Result
		disabled string
		want     service.Status
	}{
		{
			name: "running", install: true, printed: loadedRunning,
			disabled: "disabled services = {\n\t\"com.apple.x\" => enabled\n}\n",
			want:     service.Status{Installed: true, Enabled: true, Running: true, State: "running"},
		},
		{
			name: "disabled and not loaded", install: true, printed: notLoaded,
			disabled: "disabled services = {\n\t\"dev.slop-bot.daemon\" => disabled\n}\n",
			want:     service.Status{Installed: true, State: "not loaded"},
		},
		{
			name: "older macOS spells disabled true", install: true, printed: notLoaded,
			disabled: "disabled services = {\n\t\"dev.slop-bot.daemon\" => true\n}\n",
			want:     service.Status{Installed: true, State: "not loaded"},
		},
		{
			name: "loaded but exited", install: true,
			printed: &service.Result{Stdout: "gui/1000/dev.slop-bot.daemon = {\n\tstate = not running\n}\n"},
			want:    service.Status{Installed: true, Enabled: true, State: "not running"},
		},
		{
			name: "not installed", printed: notLoaded,
			want: service.Status{State: "not loaded"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, &fixtureOpts{goos: "darwin"})
			if tc.install {
				_, err := f.mgr.Install(context.Background())
				require.NoError(t, err)
			}
			f.runner.Answer(printCmd, tc.printed)
			f.runner.Answer(disabledCmd, &service.Result{Stdout: tc.disabled})
			got, err := f.mgr.Status(context.Background())
			require.NoError(t, err)
			tc.want.UnitPath = f.mgr.UnitPath()
			tc.want.Linger = service.LingerNotApplicable
			assert.Equal(t, &tc.want, got)
		})
	}
}

func withPlist(calls []string, plist string) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		if c == "launchctl bootstrap gui/1000 PLIST" {
			c = "launchctl bootstrap gui/1000 " + plist
		}
		out[i] = c
	}
	return out
}
