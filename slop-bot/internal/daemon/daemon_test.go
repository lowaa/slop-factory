package daemon

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/paths"
)

func testPaths(t *testing.T) *paths.Paths {
	t.Helper()
	home := t.TempDir()
	// The runtime dir is kept short: socket paths have a length limit.
	rt, err := os.MkdirTemp("", "sbd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(rt) })
	p, err := paths.Resolve(&paths.Env{
		Lookup: func(k string) (string, bool) {
			if k == "XDG_RUNTIME_DIR" {
				return rt, true
			}
			return "", false
		},
		Home: home, UID: os.Getuid(), GOOS: "linux",
	})
	require.NoError(t, err)
	return p
}

func newDaemon(t *testing.T, p *paths.Paths) *Daemon {
	t.Helper()
	cfg, err := config.Parse(nil, p.Config.Path, t.TempDir())
	require.NoError(t, err)
	fixed := time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC)
	d, err := New(&Options{Config: cfg, Paths: p, Logger: slog.New(slog.DiscardHandler), Clock: clock.Func(func() time.Time { return fixed })})
	require.NoError(t, err)
	return d
}

// run starts d and returns a stop function that waits for Run to return.
func run(t *testing.T, d *Daemon, p *paths.Paths) func() error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	select {
	case <-d.Ready():
	case err := <-done:
		require.NoError(t, err, "the daemon exited before it was ready")
	}
	var once sync.Once
	var runErr error
	stop := func() error {
		once.Do(func() { cancel(); runErr = <-done })
		return runErr
	}
	t.Cleanup(func() { _ = stop() })
	return stop
}

func TestHealthAnswersOverTheSocket(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	run(t, newDaemon(t, p), p)

	var h api.Health
	require.NoError(t, control.NewClient(p.Socket(), "").Do(context.Background(), http.MethodGet, api.PathHealth, nil, &h))
	assert.Equal(t, os.Getpid(), h.PID)
	assert.Equal(t, p.Socket(), h.Socket)
	assert.Equal(t, time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC), h.StartedAt)
}

func TestASecondDaemonIsRefused(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	run(t, newDaemon(t, p), p)

	err := newDaemon(t, p).Run(context.Background())
	assert.ErrorIs(t, err, ErrAlreadyRunning)
}

func TestStoppingRemovesTheSocketAndReleasesTheLock(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	stop := run(t, newDaemon(t, p), p)
	require.NoError(t, stop())

	_, err := os.Stat(p.Socket())
	assert.ErrorIs(t, err, os.ErrNotExist)
	// A new daemon can start: the lock was released.
	run(t, newDaemon(t, p), p)
}

func TestAgentTokensAreRefusedUntilAgentsExist(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	run(t, newDaemon(t, p), p)
	err := control.NewClient(p.Socket(), "some-token").Do(context.Background(), http.MethodGet, api.PathHealth, nil, nil)
	var apiErr *control.APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusUnauthorized, apiErr.Status)
}

func TestNewValidatesOptions(t *testing.T) {
	t.Parallel()
	_, err := New(&Options{})
	assert.Error(t, err)
}
