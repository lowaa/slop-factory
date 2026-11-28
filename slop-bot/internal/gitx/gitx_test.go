package gitx_test

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

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
