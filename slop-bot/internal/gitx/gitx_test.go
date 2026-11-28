package gitx_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/gitx"
	"github.com/lowaa/slop-factory/slop-bot/internal/gitx/gitxtest"
)

func newGit(t *testing.T, env []string) *gitx.Git {
	t.Helper()
	g, err := gitx.New(&gitx.Options{Bin: "git", Env: env, Logger: slog.New(slog.DiscardHandler)})
	require.NoError(t, err)
	return g
}

func initRepo(t *testing.T, g *gitx.Git) string {
	t.Helper()
	dir := t.TempDir()
	_, err := g.Run(context.Background(), dir, "init", "-q")
	require.NoError(t, err)
	_, err = g.Run(context.Background(), dir, "commit", "-q", "--allow-empty", "-m", "init")
	require.NoError(t, err)
	return dir
}

func TestMissingBinary(t *testing.T) {
	t.Parallel()
	_, err := gitx.New(&gitx.Options{Bin: filepath.Join(t.TempDir(), "no-git"), Logger: slog.New(slog.DiscardHandler)})
	require.ErrorIs(t, err, gitx.ErrGitNotFound)
}

func TestRunReturnsStdout(t *testing.T) {
	t.Parallel()
	g := gitxtest.New(t)
	dir := initRepo(t, g)
	out, err := g.Run(context.Background(), dir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, "main\n", out)
}

func TestFailuresMapToSentinels(t *testing.T) {
	t.Parallel()
	g := gitxtest.New(t)
	repo := initRepo(t, g)
	ctx := context.Background()

	_, err := g.Run(ctx, t.TempDir(), "status")
	require.ErrorIs(t, err, gitx.ErrNotARepo)
	var ge *gitx.Error
	require.ErrorAs(t, err, &ge)
	assert.Equal(t, 128, ge.ExitCode)
	assert.Contains(t, ge.Stderr, "not a git repository")
	assert.Contains(t, err.Error(), "git status")

	_, err = g.Run(ctx, repo, "branch", "main")
	require.ErrorIs(t, err, gitx.ErrBranchExists)

	_, err = g.Run(ctx, repo, "rev-parse", "--verify", "nope")
	require.ErrorIs(t, err, gitx.ErrUnknownRevision)

	_, err = g.Run(ctx, repo, "worktree", "add", repo)
	require.ErrorIs(t, err, gitx.ErrPathExists)

	_, err = g.Run(ctx, repo, "worktree", "remove", t.TempDir())
	require.ErrorIs(t, err, gitx.ErrNotAWorktree)
}

func TestIsExit(t *testing.T) {
	t.Parallel()
	g := gitxtest.New(t)
	repo := initRepo(t, g)
	_, err := g.Run(context.Background(), repo, "symbolic-ref", "-q", "refs/remotes/origin/HEAD")
	assert.True(t, gitx.IsExit(err, 1), "%v", err)
	assert.False(t, gitx.IsExit(nil, 1))
}

// A caller inside a git hook has GIT_DIR set; inherited, it would point the
// command at the caller's repo, not the dir we name.
func TestCallersRepoEnvDoesNotLeak(t *testing.T) {
	t.Parallel()
	base := gitxtest.Env(t)
	plain := newGit(t, base)
	other := initRepo(t, plain)
	target := initRepo(t, plain)
	_, err := plain.Run(context.Background(), target, "checkout", "-q", "-b", "target-branch")
	require.NoError(t, err)

	env := append(append([]string{}, base...),
		"GIT_DIR="+filepath.Join(other, ".git"),
		"GIT_WORK_TREE="+other,
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.bare", "GIT_CONFIG_VALUE_0=true",
		"LC_ALL=fr_FR.UTF-8",
	)
	g := newGit(t, env)
	out, err := g.Run(context.Background(), target, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, "target-branch\n", out)
}

func TestCancelledContext(t *testing.T) {
	t.Parallel()
	g := gitxtest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := g.Run(ctx, t.TempDir(), "status")
	require.ErrorIs(t, err, context.Canceled)
}

// A command that never finishes is cut off by the per-command deadline even
// when the caller's context has none: cleanup runs on a context stripped of
// its caller's deadline. The fake binary blocks opening a FIFO nobody writes
// to, so nothing here depends on how long anything takes.
func TestCommandTimeoutStopsAHungCommand(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fifo := filepath.Join(dir, "never")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	bin := filepath.Join(dir, "hung-git")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nread _ < "+fifo+"\n"), 0o700)) // #nosec G306 -- an executable test script
	g, err := gitx.New(&gitx.Options{Bin: bin, Env: gitxtest.Env(t), CommandTimeout: time.Millisecond})
	require.NoError(t, err)

	_, err = g.Run(context.Background(), dir, "status")
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

// hookRepo is a repo whose post-commit hook runs body.
func hookRepo(t *testing.T, g *gitx.Git, body string) string {
	t.Helper()
	dir := initRepo(t, g)
	hook := filepath.Join(dir, ".git", "hooks", "post-commit")
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\n"+body+"\n"), 0o700)) // #nosec G306 -- a hook must be executable
	return dir
}

// killPidFile stops a hook's sleep, which would outlive the test otherwise.
func killPidFile(t *testing.T, pidFile string) {
	t.Cleanup(func() {
		raw, err := os.ReadFile(pidFile) // #nosec G304 -- the test's own temp file
		if err != nil {
			return
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
}

// A hook that leaves a background child holding git's output must not hang
// Run once git itself has exited.
func TestABackgroundChildHoldingTheOutputDoesNotHangRun(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(t.TempDir(), "pid")
	killPidFile(t, pidFile)
	g, err := gitx.New(&gitx.Options{Bin: "git", Env: gitxtest.Env(t), WaitDelay: time.Millisecond})
	require.NoError(t, err)
	repo := hookRepo(t, g, "sleep 300 &\necho $! > "+pidFile)

	_, err = g.Run(context.Background(), repo, "commit", "-q", "--allow-empty", "-m", "x")
	require.NoError(t, err)
}

// Cancelling stops git's hooks too, so Run returns rather than waiting on a
// hook that holds the output (WaitDelay here is far past the test timeout).
// Logger is left nil on purpose: it defaults to discarding.
func TestCancelStopsGitsWholeProcessGroup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fifo := filepath.Join(dir, "ready")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	pidFile := filepath.Join(dir, "pid")
	killPidFile(t, pidFile)
	g, err := gitx.New(&gitx.Options{Bin: "git", Env: gitxtest.Env(t), WaitDelay: time.Hour})
	require.NoError(t, err)
	repo := hookRepo(t, g, "echo $$ > "+pidFile+"\necho ready > "+fifo+"\nexec sleep 300")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		// Opening the FIFO blocks until the hook is running.
		f, err := os.Open(fifo) // #nosec G304 -- the test's own FIFO
		if err == nil {
			_, _ = f.Read(make([]byte, 8))
			_ = f.Close()
		}
		cancel()
	}()
	_, err = g.Run(ctx, repo, "commit", "-q", "--allow-empty", "-m", "x")
	require.ErrorIs(t, err, context.Canceled)
}
