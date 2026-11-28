package cli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/service"
)

// stoppingRunner is the service runner, where stopping the service stops
// its daemon.
type stoppingRunner struct {
	service.Runner
	daemon *atomic.Bool
}

func (r *stoppingRunner) Run(ctx context.Context, name string, args ...string) (*service.Result, error) {
	if name == "systemctl" && slices.Contains(args, "--now") {
		r.daemon.Store(false)
	}
	return r.Runner.Run(ctx, name, args...)
}

// withDaemon has a daemon answer at the CLI's socket, as the installed
// service's does: the service's socket is logind's runtime dir, which a
// test can't listen in. stopsWithService says whether stopping the service
// stops it, or it was started by hand.
func (h *installHarness) withDaemon(stopsWithService bool) {
	var running atomic.Bool
	running.Store(true)
	if stopsWithService {
		h.deps.ServiceRunner = &stoppingRunner{Runner: h.deps.ServiceRunner, daemon: &running}
	}
	h.deps.daemonAnswers = func(context.Context, string) bool { return running.Load() }
}

// The installed service's own daemon is running after install: purge stops
// it with the service, and only then checks nothing else is.
func TestPurgeStopsTheInstalledServiceFirst(t *testing.T) {
	t.Parallel()
	h := newInstallHarness(t)
	require.Equal(t, 0, h.run(t, "install"), h.err.String()+h.out.String())
	state := filepath.Join(h.home, ".local", "state", "slop-bot")
	require.NoError(t, os.MkdirAll(state, 0o700))
	h.withDaemon(true)

	require.Equal(t, 0, h.run(t, "uninstall", "--purge", "--yes"), h.err.String())
	assert.Contains(t, h.out.String(), "stopped and removed the slop-bot service")
	assert.NoDirExists(t, state)
	assert.NoFileExists(t, h.configFile())
}

func TestPurgeRefusesWhileAHandStartedDaemonOutlivesTheService(t *testing.T) {
	t.Parallel()
	h := newInstallHarness(t)
	require.Equal(t, 0, h.run(t, "install"), h.err.String()+h.out.String())
	h.withDaemon(false)

	assert.Equal(t, 1, h.run(t, "uninstall", "--purge", "--yes"))
	assert.Contains(t, h.err.String(), "started by hand is still running")
	assert.Contains(t, h.err.String(), "nothing was deleted")
	assert.NoFileExists(t, h.unit(), "the service is gone")
	assert.FileExists(t, h.configFile(), "its data isn't")
}
