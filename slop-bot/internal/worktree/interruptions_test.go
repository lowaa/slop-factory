package worktree

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ForceCleanup is the way out whatever state the ticket's branches are in,
// including a rescue branch someone has moved since.
func TestForceCleanupOfADeletedDetachedWorktree(t *testing.T) {
	t.Parallel()
	for _, moved := range []bool{false, true} {
		f := newFixture(t, "a")
		ctx := context.Background()
		wt := f.create("50", []string{"a"}, "a")[0]
		f.run(wt.Path, "switch", "-q", "--detach")
		head := f.commit(wt.Path, "work", "detached")
		require.NoError(t, os.RemoveAll(wt.Path))
		if moved {
			f.run(wt.RepoPath, "branch", "slop-bot/t50/detached-"+head, "main")
		}

		require.NoError(t, f.m.ForceCleanup(ctx, "50", wt), "moved=%v", moved)
		assert.NotContains(t, f.run(wt.RepoPath, "worktree", "list"), wt.Path)
		assert.Empty(t, f.run(wt.RepoPath, "for-each-ref", "refs/heads/slop-bot/t50/"))
	}
}

// Mid-rebase, git lists a worktree as detached, but the rebase still needs
// its branch to finish.
func TestCleanupLeavesABranchBeingRebasedElsewhere(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	wt := f.create("51", []string{"a"}, "a")[0]
	users := filepath.Join(f.root, "users-checkout")
	f.run(wt.RepoPath, "worktree", "add", "-q", "--no-track", "-b", "slop-bot/t51/side", users, "origin/main")
	f.commit(users, "side", "side work")
	f.run(users, "push", "-q", "origin", "HEAD")
	_, err := f.git.Run(context.Background(), users, "rebase", "--force-rebase", "-x", "false", "origin/main")
	require.Error(t, err, "the rebase must stop part way")

	r := f.m.Cleanup(context.Background(), "51", []*Worktree{wt})[0]
	require.Error(t, r.Err)
	assert.Contains(t, r.Err.Error(), "slop-bot/t51/side")
	assert.True(t, f.branchExists("a", "slop-bot/t51/side"))
	f.run(users, "rebase", "--abort")
}

func TestWaitingForTheRepoLockHonoursTheContext(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	unlock, err := f.m.acquire(context.Background(), "repo")
	require.NoError(t, err)
	defer unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = f.m.acquire(ctx, "repo")
	require.ErrorIs(t, err, context.Canceled)
}

// cancelDuringHook installs a post-checkout hook in repo that signals, then
// blocks, and returns a context cancelled once the hook is running.
func cancelDuringHook(t *testing.T, repo string) context.Context {
	t.Helper()
	fifo := filepath.Join(t.TempDir(), "ready")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	hook := filepath.Join(repo, ".git", "hooks", "post-checkout")
	body := "#!/bin/sh\necho ready > " + fifo + "\nexec sleep 300\n"
	require.NoError(t, os.WriteFile(hook, []byte(body), 0o700)) // #nosec G306 -- a hook must be executable
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		fh, err := os.Open(fifo) // #nosec G304 -- the test's own FIFO
		if err == nil {
			_, _ = fh.Read(make([]byte, 8))
			_ = fh.Close()
		}
		cancel()
	}()
	return ctx
}

// Cancelled while a post-checkout hook runs, git has already made the
// worktree; Create must still say so, though its context is done.
func TestCreateCancelledDuringAHookReportsTheWorktree(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	ctx := cancelDuringHook(t, f.repos["a"])

	wts, err := f.m.Create(ctx, "52", []string{"a"}, "a")
	require.ErrorIs(t, err, context.Canceled)
	if assert.Len(t, wts, 1) {
		assert.Equal(t, filepath.Join(f.ws, "t52-a"), wts[0].Path)
		assert.Contains(t, f.run(f.repos["a"], "worktree", "list"), wts[0].Path)
	}
}

func TestNewBranchCancelledDuringAHookReportsTheBranch(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	wt := f.create("53", []string{"a"}, "a")[0]
	ctx := cancelDuringHook(t, f.repos["a"])

	next, err := f.m.NewBranch(ctx, wt, "second")
	require.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, next)
	assert.Equal(t, "slop-bot/t53/second", next.Branch)
}
