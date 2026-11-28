package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/service"
)

const lingerCmd = "loginctl show-user 1000 -p Linger"

func TestSystemdInstallWritesTheUnitAndRestartsIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t, &fixtureOpts{goos: "linux"})
	f.runner.Answer(lingerCmd, &service.Result{Stdout: "Linger=no\n"})

	res, err := f.mgr.Install(context.Background())
	require.NoError(t, err)

	want := filepath.Join(f.home, ".config", "systemd", "user", "slop-bot.service")
	assert.Equal(t, want, f.mgr.UnitPath())
	assert.Equal(t, want, res.UnitPath)
	info, err := os.Stat(want)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	assert.Equal(t, []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable slop-bot.service",
		"systemctl --user restart slop-bot.service",
		lingerCmd,
	}, f.runner.Calls())
	assert.Equal(t, service.LingerOff, res.Linger)
	assert.Equal(t, f.opts.Binary, res.Binary)
	assert.Equal(t, []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "PATH"},
		envNames(res.Env), "systemd sets XDG_RUNTIME_DIR itself")
	assert.Empty(t, res.Warnings)
}

func TestSystemdInstallUsesTheResolvedConfigHome(t *testing.T) {
	t.Parallel()
	f := newFixture(t, &fixtureOpts{goos: "linux", vars: map[string]string{"XDG_CONFIG_HOME": "~/cfg"}})
	res, err := f.mgr.Install(context.Background())
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(f.home, "cfg", "systemd", "user", "slop-bot.service"), res.UnitPath)
	assert.Contains(t, res.Env, service.EnvVar{Name: "XDG_CONFIG_HOME", Value: filepath.Join(f.home, "cfg")})
}

func TestSystemdInstallWarnsWhenTheCLIsRuntimeDirIsNotTheServices(t *testing.T) {
	t.Parallel()
	f := newFixture(t, &fixtureOpts{goos: "linux", vars: map[string]string{"XDG_RUNTIME_DIR": "/tmp/elsewhere"}})
	res, err := f.mgr.Install(context.Background())
	require.NoError(t, err)
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], "/run/user/1000/slop-bot")
}

func TestInstallCleansPATH(t *testing.T) {
	t.Parallel()
	const extra = "::.:bin:/usr/bin/:/opt/x"
	f := newFixture(t, &fixtureOpts{goos: "linux", extraPATH: extra})
	res, err := f.mgr.Install(context.Background())
	require.NoError(t, err)
	base := strings.TrimSuffix(f.opts.PATH, extra)
	assert.Equal(t, service.EnvVar{Name: "PATH", Value: base + ":/opt/x"},
		res.Env[len(res.Env)-1], "relative and repeated entries go, order stays")
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], `["." "bin"]`)
}

func TestSystemdInstallRefusesABinaryPathSystemdRejects(t *testing.T) {
	t.Parallel()
	f := newFixture(t, &fixtureOpts{goos: "linux", binary: `/opt/it's/slop-bot`})
	_, err := f.mgr.Install(context.Background())
	require.ErrorContains(t, err, "quote or backslash")
	assert.Empty(t, f.runner.Calls())
	assert.NoFileExists(t, f.mgr.UnitPath())
}

func TestInstallRefusesARelativeBinary(t *testing.T) {
	t.Parallel()
	f := newFixture(t, &fixtureOpts{goos: "linux", binary: "slop-bot"})
	_, err := f.mgr.Install(context.Background())
	require.ErrorContains(t, err, "absolute")
}

func TestInstallRefusesAControlCharacter(t *testing.T) {
	t.Parallel()
	f := newFixture(t, &fixtureOpts{goos: "linux", extraPATH: ":/opt/a\nExecStartPre=/bin/evil"})
	_, err := f.mgr.Install(context.Background())
	require.ErrorContains(t, err, "control character")
	assert.NoFileExists(t, f.mgr.UnitPath())
}

func TestSystemdInstallReportsAFailingSystemctl(t *testing.T) {
	t.Parallel()
	f := newFixture(t, &fixtureOpts{goos: "linux"})
	f.runner.Answer("systemctl --user daemon-reload",
		&service.Result{ExitCode: 1, Stderr: "Failed to connect to bus: No medium found\n"})
	_, err := f.mgr.Install(context.Background())
	require.ErrorContains(t, err, "Failed to connect to bus")
	assert.Equal(t, []string{"systemctl --user daemon-reload"}, f.runner.Calls(), "nothing after the failure")
}

func TestSystemdCommandsNeedAnInstalledUnit(t *testing.T) {
	t.Parallel()
	f := newFixture(t, &fixtureOpts{goos: "linux"})
	ctx := context.Background()
	for name, op := range map[string]func(context.Context) error{
		"uninstall": f.mgr.Uninstall, "start": f.mgr.Start, "stop": f.mgr.Stop,
	} {
		assert.ErrorIs(t, op(ctx), service.ErrNotInstalled, name)
	}
	assert.Empty(t, f.runner.Calls())
}

func TestSystemdStartStopAndUninstall(t *testing.T) {
	t.Parallel()
	f := newFixture(t, &fixtureOpts{goos: "linux"})
	ctx := context.Background()
	_, err := f.mgr.Install(ctx)
	require.NoError(t, err)

	f.runner.Reset()
	require.NoError(t, f.mgr.Stop(ctx))
	require.NoError(t, f.mgr.Start(ctx))
	require.NoError(t, f.mgr.Uninstall(ctx))
	assert.Equal(t, []string{
		"systemctl --user stop slop-bot.service",
		"systemctl --user start slop-bot.service",
		"systemctl --user disable --now slop-bot.service",
		"systemctl --user daemon-reload",
	}, f.runner.Calls())
	assert.NoFileExists(t, f.mgr.UnitPath())
}

func TestSystemdStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name            string
		install         bool
		active, enabled *service.Result
		linger          *service.Result
		want            service.Status
	}{
		{
			name: "running", install: true,
			active:  &service.Result{Stdout: "active\n"},
			enabled: &service.Result{Stdout: "enabled\n"},
			linger:  &service.Result{Stdout: "Linger=yes\n"},
			want:    service.Status{Installed: true, Enabled: true, Running: true, State: "active", Linger: service.LingerOn},
		},
		{
			name: "failed", install: true,
			active:  &service.Result{Stdout: "failed\n", ExitCode: 3},
			enabled: &service.Result{Stdout: "enabled\n"},
			linger:  &service.Result{Stdout: "Linger=no\n"},
			want:    service.Status{Installed: true, Enabled: true, State: "failed", Linger: service.LingerOff},
		},
		{
			name:    "not installed",
			active:  &service.Result{Stdout: "inactive\n", ExitCode: 3},
			enabled: &service.Result{Stdout: "not-found\n", ExitCode: 4},
			linger:  &service.Result{ExitCode: 1, Stderr: "Failed to get user: no such user"},
			want:    service.Status{State: "inactive", Linger: service.LingerUnknown},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, &fixtureOpts{goos: "linux"})
			if tc.install {
				_, err := f.mgr.Install(context.Background())
				require.NoError(t, err)
			}
			f.runner.Answer("systemctl --user is-active slop-bot.service", tc.active)
			f.runner.Answer("systemctl --user is-enabled slop-bot.service", tc.enabled)
			f.runner.Answer(lingerCmd, tc.linger)
			got, err := f.mgr.Status(context.Background())
			require.NoError(t, err)
			tc.want.UnitPath = f.mgr.UnitPath()
			assert.Equal(t, &tc.want, got)
		})
	}
}

func TestSystemdStatusReportsSystemctlItselfFailing(t *testing.T) {
	t.Parallel()
	f := newFixture(t, &fixtureOpts{goos: "linux"})
	f.runner.Answer("systemctl --user is-active slop-bot.service",
		&service.Result{ExitCode: 1, Stderr: "Failed to connect to bus"})
	_, err := f.mgr.Status(context.Background())
	require.ErrorContains(t, err, "Failed to connect to bus")
}

func TestARunnerThatCannotRunIsAnError(t *testing.T) {
	t.Parallel()
	f := newFixture(t, &fixtureOpts{goos: "linux"})
	f.runner.Fail("systemctl --user is-active slop-bot.service", errors.New("systemctl: not found"))
	_, err := f.mgr.Status(context.Background())
	require.ErrorContains(t, err, "not found")
}

func envNames(env []service.EnvVar) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		out = append(out, e.Name)
	}
	return out
}
