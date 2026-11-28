package cli

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/daemon"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/paths"
)

type harness struct {
	deps          *Deps
	out, err      *bytes.Buffer
	home, runtime string
	// config is a slop-bot-config.yaml under home naming a fixed user.
	config string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	home := t.TempDir()
	rt, err := os.MkdirTemp("", "sbcli")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(rt) })
	h := &harness{out: &bytes.Buffer{}, err: &bytes.Buffer{}, home: home, runtime: rt,
		config: filepath.Join(home, "slop-bot-config.yaml")}
	require.NoError(t, os.WriteFile(h.config, []byte("user: ada\nharnesses: {claude-code: {}, pi: {}}\n"), 0o600))
	vars := map[string]string{"XDG_RUNTIME_DIR": rt}
	h.deps = &Deps{
		Stdout: h.out, Stderr: h.err,
		Env: &paths.Env{
			Lookup: func(k string) (string, bool) { v, ok := vars[k]; return v, ok },
			Home:   home, UID: os.Getuid(), GOOS: "linux",
		},
	}
	// Never a real harness in tests: tickets these tests assign get a fake
	// that answers on its own (no controller), unless a test sets its own.
	self, err := os.Executable()
	require.NoError(t, err)
	h.deps.harnesses = daemon.Harnesses{"claude-code": &e2eHarness{
		Harness: &fake.Harness{Binary: self, Logger: slog.New(slog.DiscardHandler), Clock: clock.Real{}},
		h:       h,
	}}
	return h
}

func (h *harness) run(t *testing.T, args ...string) int {
	t.Helper()
	h.out.Reset()
	h.err.Reset()
	return Execute(context.Background(), h.deps, args)
}

func TestVersion(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	require.Equal(t, 0, h.run(t, "version"))
	assert.Equal(t, "dev\n", h.out.String())
}

func TestPathsShowsEveryLocationAndWhereItCameFrom(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	require.Equal(t, 0, h.run(t, "paths"))
	out := h.out.String()
	assert.Contains(t, out, filepath.Join(h.runtime, "slop-bot", "slop-bot.sock"))
	assert.Contains(t, out, filepath.Join(h.home, ".local", "state", "slop-bot", "state.db"))
	assert.Contains(t, out, "default ($XDG_CONFIG_HOME unset)")
	assert.Contains(t, out, "$XDG_RUNTIME_DIR")
	assert.Contains(t, out, "user briefing")
	assert.Contains(t, out, "(none)")
}

func TestABrokenConfigIsReportedNotDefaulted(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	cfg := filepath.Join(h.home, "bad.yaml")
	require.NoError(t, os.WriteFile(cfg, []byte("workspace_dri: x\n"), 0o600))
	assert.Equal(t, 1, h.run(t, "--config", cfg, "daemon"))
	assert.Contains(t, h.err.String(), "workspace_dri")
}

func TestAnExplicitMissingConfigIsAnError(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	assert.Equal(t, 1, h.run(t, "--config", filepath.Join(h.home, "nope.yaml"), "daemon"))
	assert.Contains(t, h.err.String(), "nope.yaml")
}

func TestHealthWithoutADaemonNamesTheSocket(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	assert.Equal(t, 1, h.run(t, "health"))
	assert.Contains(t, h.err.String(), "can't reach the slop-bot daemon at "+filepath.Join(h.runtime, "slop-bot", "slop-bot.sock"))
}

// startDaemon runs `slop-bot daemon` in-process with h's environment, and
// returns once it serves. The returned stop waits for it to exit cleanly.
func startDaemon(t *testing.T, h *harness) (stop func()) {
	t.Helper()
	ready := make(chan *daemon.Daemon, 1)
	daemonDeps := *h.deps
	daemonDeps.daemonBuilt = func(d *daemon.Daemon) { ready <- d }
	daemonDeps.Stdout, daemonDeps.Stderr = &bytes.Buffer{}, &bytes.Buffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- Execute(ctx, &daemonDeps, []string{"--config", h.config, "daemon"}) }()
	// A daemon that fails before it is built (a config it refuses) never
	// reports one.
	var d *daemon.Daemon
	select {
	case d = <-ready:
	case code := <-done:
		require.FailNowf(t, "daemon exited", "code %d: %s", code, daemonDeps.Stderr)
	}
	select {
	case <-d.Ready():
	case code := <-done:
		require.FailNowf(t, "daemon exited", "code %d: %s", code, daemonDeps.Stderr)
	}
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			assert.Equal(t, 0, <-done, "daemon: %s", daemonDeps.Stderr)
		})
	}
	t.Cleanup(stop)
	return stop
}

func TestHealthAgainstARunningDaemon(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	startDaemon(t, h)
	require.Equal(t, 0, h.run(t, "health"), h.err.String())
	assert.True(t, strings.HasPrefix(h.out.String(), "daemon dev running"), h.out.String())
}

func TestUnknownCommandFails(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	assert.Equal(t, 1, h.run(t, "frobnicate"))
	assert.Contains(t, h.err.String(), "unknown command")
}
