package cli

import (
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/service"
	"github.com/lowaa/slop-factory/slop-bot/internal/service/servicetest"
)

// newServiceHarness is the CLI harness with a fake service runner, so no
// test reaches the real systemd.
func newServiceHarness(t *testing.T) (*harness, *servicetest.Runner) {
	t.Helper()
	h := newHarness(t)
	runner := servicetest.NewRunner()
	h.deps.ServiceRunner = runner
	h.deps.FindBinary = func() (string, error) { return "/usr/local/bin/slop-bot", nil }
	lookup := h.deps.Env.Lookup
	h.deps.Env.Lookup = func(k string) (string, bool) {
		if k == "PATH" {
			return "/usr/local/bin:/usr/bin", true
		}
		return lookup(k)
	}
	return h, runner
}

func TestServiceInstallPrintsTheUnitAndItsEnvironment(t *testing.T) {
	t.Parallel()
	h, runner := newServiceHarness(t)
	runner.Answer("loginctl show-user "+strconv.Itoa(h.deps.Env.UID)+" -p Linger", &service.Result{Stdout: "Linger=no\n"})

	require.Equal(t, 0, h.run(t, "service", "install"), h.err.String())
	out := h.out.String()
	unit := filepath.Join(h.home, ".config", "systemd", "user", "slop-bot.service")
	assert.FileExists(t, unit)
	assert.Contains(t, out, "installed the slop-bot service: "+unit)
	assert.Contains(t, out, "it runs: /usr/local/bin/slop-bot daemon")
	assert.Contains(t, out, "XDG_CONFIG_HOME="+filepath.Join(h.home, ".config"))
	assert.Contains(t, out, "default ($XDG_CONFIG_HOME unset)")
	assert.Contains(t, out, "PATH=/usr/local/bin:/usr/bin")
	assert.Contains(t, out, "loginctl enable-linger")
	// The harness's runtime dir isn't /run/user/<uid>, which the service
	// would use.
	assert.Contains(t, out, "warning: your runtime dir is")
}

func TestServiceInstallTakesABinaryOverride(t *testing.T) {
	t.Parallel()
	h, _ := newServiceHarness(t)
	require.Equal(t, 0, h.run(t, "service", "install", "--binary", "/opt/sb/slop-bot"), h.err.String())
	assert.Contains(t, h.out.String(), "it runs: /opt/sb/slop-bot daemon")
}

func TestServiceStartBeforeInstallSaysToInstall(t *testing.T) {
	t.Parallel()
	h, runner := newServiceHarness(t)
	assert.Equal(t, 1, h.run(t, "service", "start"))
	assert.Contains(t, h.err.String(), "slop-bot service install")
	assert.Empty(t, runner.Calls())
}

func TestServiceUninstallWhenNotInstalledIsNotAnError(t *testing.T) {
	t.Parallel()
	h, _ := newServiceHarness(t)
	require.Equal(t, 0, h.run(t, "service", "uninstall"))
	assert.Contains(t, h.out.String(), "isn't installed")
}

func TestServiceRoundTrip(t *testing.T) {
	t.Parallel()
	h, runner := newServiceHarness(t)
	require.Equal(t, 0, h.run(t, "service", "install"), h.err.String())
	runner.Answer("systemctl --user is-active slop-bot.service", &service.Result{Stdout: "active\n"})
	runner.Answer("systemctl --user is-enabled slop-bot.service", &service.Result{Stdout: "enabled\n"})

	require.Equal(t, 0, h.run(t, "service", "status"), h.err.String())
	assert.Regexp(t, `installed\s+yes`, h.out.String())
	assert.Regexp(t, `running\s+yes \(active\)`, h.out.String())

	runner.Reset()
	require.Equal(t, 0, h.run(t, "service", "stop"), h.err.String())
	require.Equal(t, 0, h.run(t, "service", "start"), h.err.String())
	require.Equal(t, 0, h.run(t, "service", "uninstall"), h.err.String())
	assert.Equal(t, []string{
		"systemctl --user stop slop-bot.service",
		"systemctl --user start slop-bot.service",
		"systemctl --user disable --now slop-bot.service",
		"systemctl --user daemon-reload",
	}, runner.Calls())
	assert.Contains(t, h.out.String(), "uninstalled the slop-bot service")
}
