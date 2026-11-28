package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/testexec"
)

// git unregisters a worktree even when it couldn't delete all its files; the
// rest must be reported as left behind, not taken for someone else's next time.
func TestCleanupReportsAWorktreeGitCouldNotFullyDelete(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only directory this relies on")
	}
	f := newFixture(t, "a")
	wt := f.create("40", []string{"a"}, "a")[0]
	f.commit(wt.Path, ".gitignore", "cache")
	f.run(wt.Path, "push", "-q", "origin", "HEAD")
	cache := filepath.Join(wt.Path, "cache")
	require.NoError(t, os.Mkdir(cache, 0o750))
	f.write(cache, "entry", "x")
	require.NoError(t, os.Chmod(cache, 0o500))       // #nosec G302 -- read-only on purpose
	t.Cleanup(func() { _ = os.Chmod(cache, 0o700) }) // #nosec G302 -- so TempDir can be removed

	r := f.m.Cleanup(context.Background(), "40", []*Worktree{wt})[0]
	require.ErrorIs(t, r.Err, ErrLeftover)
	assert.False(t, r.Removed)
	assert.False(t, f.branchExists("a", "slop-bot/t40/work"), "the branches were checked safe, so they go")
}

// Tickets on one repo run at once; git reads every worktree's admin dir, so
// Manager must keep them from racing inside git.
func TestConcurrentTicketsOnOneRepo(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	const tickets, rounds = 8, 2
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, tickets*rounds)
	for i := range tickets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			ticket := ident.TicketID(fmt.Sprintf("c%d", i))
			<-start
			for range rounds {
				wts, err := f.m.Create(ctx, ticket, []string{"a"}, "a")
				if err != nil {
					errs <- err
					return
				}
				for _, r := range f.m.Cleanup(ctx, ticket, wts) {
					if r.Err != nil || !r.Removed {
						errs <- fmt.Errorf("cleanup %s: removed=%v err=%w", ticket, r.Removed, r.Err)
						return
					}
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
}

// `push -u` records an upstream in the repo's config; deleting the branch
// must take that with it, or the ticket's next branch of that name inherits it.
func TestCleanupDropsTheBranchConfigItLeavesBehind(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	wt := f.create("41", []string{"a"}, "a")[0]
	f.commit(wt.Path, "work", "pushed")
	f.run(wt.Path, "push", "-q", "-u", "origin", "HEAD")
	require.True(t, f.m.Cleanup(context.Background(), "41", []*Worktree{wt})[0].Removed)

	again := f.create("41", []string{"a"}, "a")[0]
	_, err := f.git.Run(context.Background(), again.Path, "rev-parse", "--abbrev-ref", "@{upstream}")
	assert.Error(t, err, "the new branch must have no upstream")
}

// A timeout says nothing about the worktree, and must not read as broken:
// ForceCleanup deletes broken worktrees by hand.
func TestACancelledCheckIsNotABrokenWorktree(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	wt := f.create("42", []string{"a"}, "a")[0]
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := f.m.checkWorktree(ctx, wt.RepoPath, wt.Path)
	require.ErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, ErrBroken)
	require.Error(t, f.m.forceRemove(ctx, wt.RepoPath, wt.Path))
	assert.True(t, onDisk(t, wt.Path))
}

// A rescue branch that's been moved since doesn't hold the detached commits;
// taking it as if it did would let Cleanup drop them.
func TestARescueBranchThatMovedDoesNotCountAsSaved(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	wt := f.create("43", []string{"a"}, "a")[0]
	f.run(wt.Path, "switch", "-q", "--detach")
	head := f.commit(wt.Path, "work", "detached")
	f.run(wt.RepoPath, "branch", "slop-bot/t43/detached-"+head, "main")
	require.NoError(t, os.RemoveAll(wt.Path))

	r := f.m.Cleanup(context.Background(), "43", []*Worktree{wt})[0]
	require.ErrorIs(t, r.Err, ErrUnsavedWork)
	assert.False(t, r.Removed)
	assert.Contains(t, f.run(wt.RepoPath, "worktree", "list", "--porcelain"), head, "the registration still holds it")
}

// A different repo cloned over a deleted worktree isn't this repo's worktree.
func TestAnotherRepoAtTheWorktreePathIsNotReused(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	ctx := context.Background()
	wt := f.create("44", []string{"a"}, "a")[0]
	require.NoError(t, os.RemoveAll(wt.Path))
	f.run(f.root, "init", "-q", wt.Path)
	f.commit(wt.Path, "theirs", "someone else's repo")

	wts, err := f.m.Create(ctx, "44", []string{"a"}, "a")
	require.ErrorIs(t, err, ErrCollision)
	assert.Empty(t, wts, "git made nothing, so there's nothing to record")
	require.ErrorIs(t, f.m.ForceCleanup(ctx, "44", wt), ErrCollision)
	assert.FileExists(t, filepath.Join(wt.Path, "theirs"))
}

// git runs post-checkout after the checkout is done; when the hook fails, what
// git made must still come back to the caller.
func TestAFailingCheckoutHookStillReportsWhatWasMade(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a", "b")
	ctx := context.Background()
	hook := filepath.Join(f.repos["b"], ".git", "hooks", "post-checkout")
	testexec.WriteScript(t, hook, "#!/bin/sh\nexit 1\n")

	wts, err := f.m.Create(ctx, "45", []string{"a", "b"}, "b")
	require.Error(t, err)
	require.Len(t, wts, 2)
	assert.Equal(t, filepath.Join(f.ws, "t45-b"), wts[1].Path)
	assert.True(t, wts[1].Primary)

	next, err := f.m.NewBranch(ctx, wts[1], "next")
	require.Error(t, err)
	require.NotNil(t, next)
	assert.Equal(t, "slop-bot/t45/next", next.Branch)
}

func TestCleanupWithoutOriginHEADOnAnUnbornMainBranch(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	repo := f.repos["a"]
	wt := f.create("46", []string{"a"}, "a")[0]
	f.run(repo, "remote", "set-head", "origin", "--delete")
	f.run(repo, "switch", "-q", "--orphan", "fresh")

	r := f.m.Cleanup(context.Background(), "46", []*Worktree{wt})[0]
	require.NoError(t, r.Err)
	assert.True(t, r.Removed)
}
