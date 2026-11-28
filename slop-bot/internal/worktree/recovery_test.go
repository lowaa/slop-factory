package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/gitx"
)

// The states a worktree is left in when something outside slop-bot happens
// to it: its directory deleted, locked, half-made, or its repo's origin/HEAD
// gone.

func (f *fixture) reachable(repo, oid string) bool {
	f.t.Helper()
	out := f.run(f.repos[repo], "branch", "--all", "--contains", oid)
	return out != ""
}

// A deleted worktree's detached HEAD is all that holds its commits. Cleanup
// must save them, and keep saying so on every later run.
func TestCleanupOfADeletedDetachedWorktreeSavesItsCommits(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	wt := f.create("20", []string{"a"}, "a")[0]
	f.run(wt.Path, "switch", "-q", "--detach")
	head := f.commit(wt.Path, "work", "detached")
	require.NoError(t, os.RemoveAll(wt.Path))

	for range 2 {
		r := f.m.Cleanup(context.Background(), "20", []*Worktree{wt})[0]
		require.NoError(t, r.Err)
		assert.False(t, r.Removed)
		assert.Equal(t, []ReasonKind{Unpushed}, kinds(r.Reasons))
		assert.True(t, f.reachable("a", head), "the detached commit must stay on a branch")
	}
	assert.True(t, f.branchExists("a", "slop-bot/t20/detached-"+head[:12]))
}

// Only the ticket's own registration is touched: a user's worktree on
// storage that isn't mounted keeps its HEAD and index.
func TestAnUnrelatedStaleWorktreeSurvives(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	ctx := context.Background()
	repo := f.repos["a"]
	users := filepath.Join(f.root, "usb", "users-worktree")
	f.run(repo, "worktree", "add", "-q", "--detach", users)
	usersHead := f.commit(users, "mine", "user's detached work")
	moved := filepath.Join(f.root, "unmounted")
	require.NoError(t, os.Rename(filepath.Join(f.root, "usb"), moved))

	wt := f.create("21", []string{"a"}, "a")[0]
	require.NoError(t, os.RemoveAll(wt.Path))
	f.create("21", []string{"a"}, "a") // recovers ours
	require.NoError(t, os.RemoveAll(wt.Path))
	r := f.m.Cleanup(ctx, "21", []*Worktree{wt})[0]
	require.NoError(t, r.Err)
	require.True(t, r.Removed)

	// Mounted again, the user's worktree still works.
	require.NoError(t, os.Rename(moved, filepath.Join(f.root, "usb")))
	assert.Equal(t, usersHead, f.run(users, "rev-parse", "HEAD"))
}

func TestCreateRecoversOnTheBranchTheWorktreeWasOn(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	wt := f.create("22", []string{"a"}, "a")[0]
	next, err := f.m.NewBranch(context.Background(), wt, "second")
	require.NoError(t, err)
	head := f.commit(wt.Path, "second", "second PR")
	require.NoError(t, os.RemoveAll(wt.Path))

	again := f.create("22", []string{"a"}, "a")[0]
	assert.Equal(t, next.Branch, again.Branch)
	assert.Equal(t, "slop-bot/t22/second", f.run(again.Path, "branch", "--show-current"))
	assert.Equal(t, head, f.run(again.Path, "rev-parse", "HEAD"))
}

func TestCreateRecoversADetachedWorktreeWithItsCommits(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	wt := f.create("23", []string{"a"}, "a")[0]
	f.run(wt.Path, "switch", "-q", "--detach")
	head := f.commit(wt.Path, "work", "detached")
	require.NoError(t, os.RemoveAll(wt.Path))

	again := f.create("23", []string{"a"}, "a")[0]
	assert.Empty(t, again.Branch)
	assert.Equal(t, head, f.run(again.Path, "rev-parse", "HEAD"))
	assert.True(t, f.reachable("a", head))
}

// A worktree locked and missing may be on storage that isn't mounted: Create
// won't paper over it, and Cleanup keeps it, registration and all.
func TestALockedMissingWorktreeIsLeftAlone(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	ctx := context.Background()
	wt := f.create("24", []string{"a"}, "a")[0]
	f.run(f.repos["a"], "worktree", "lock", "--reason", "on a usb disk", wt.Path)
	require.NoError(t, os.RemoveAll(wt.Path))

	_, err := f.m.Create(ctx, "24", []string{"a"}, "a")
	require.ErrorIs(t, err, ErrBroken)
	assert.False(t, onDisk(t, wt.Path))

	r := f.m.Cleanup(ctx, "24", []*Worktree{wt})[0]
	require.NoError(t, r.Err)
	assert.False(t, r.Removed)
	assert.Equal(t, []ReasonKind{Locked}, kinds(r.Reasons))
	assert.Contains(t, f.run(f.repos["a"], "worktree", "list"), wt.Path)

	require.NoError(t, f.m.ForceCleanup(ctx, "24", wt))
	assert.NotContains(t, f.run(f.repos["a"], "worktree", "list"), wt.Path)
}

// What an interrupted `git worktree add` leaves: Create refuses it rather
// than reporting a half-made worktree as ready, and ForceCleanup clears it.
func TestAnInterruptedCreateIsRefusedUntilForceCleaned(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	ctx := context.Background()
	wt := f.create("25", []string{"a"}, "a")[0]
	f.run(f.repos["a"], "worktree", "lock", "--reason", lockInitializing, wt.Path)

	_, err := f.m.Create(ctx, "25", []string{"a"}, "a")
	require.ErrorIs(t, err, ErrBroken)
	require.NoError(t, f.m.ForceCleanup(ctx, "25", wt))
	f.create("25", []string{"a"}, "a")
}

func TestCreateRefusesAWorktreeReplacedByASymlink(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	wt := f.create("26", []string{"a"}, "a")[0]
	require.NoError(t, os.RemoveAll(wt.Path))
	require.NoError(t, os.Symlink(f.repos["a"], wt.Path))

	_, err := f.m.Create(context.Background(), "26", []string{"a"}, "a")
	require.ErrorIs(t, err, ErrCollision)
}

func TestCreateAndForceCleanupOfAWorktreeWithItsGitLinkGone(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	ctx := context.Background()
	wt := f.create("27", []string{"a"}, "a")[0]
	require.NoError(t, os.Remove(filepath.Join(wt.Path, ".git")))

	_, err := f.m.Create(ctx, "27", []string{"a"}, "a")
	require.ErrorIs(t, err, ErrBroken)

	require.NoError(t, f.m.ForceCleanup(ctx, "27", wt))
	assert.False(t, onDisk(t, wt.Path))
	assert.NotContains(t, f.run(f.repos["a"], "worktree", "list"), wt.Path)
	f.create("27", []string{"a"}, "a")
}

func TestARepoPathInsideAnotherRepoIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	sub := filepath.Join(f.repos["a"], "subdir")
	require.NoError(t, os.Mkdir(sub, 0o750))
	f.repos["sub"] = sub

	_, err := f.m.Create(context.Background(), "28", []string{"sub"}, "sub")
	require.ErrorIs(t, err, gitx.ErrNotARepo)
	assert.False(t, f.branchExists("a", "slop-bot/t28/work"))
}

func TestADanglingOriginHEADIsReported(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	ctx := context.Background()
	wt := f.create("29", []string{"a"}, "a")[0]
	f.run(f.repos["a"], "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/gone")

	_, err := f.m.Create(ctx, "30", []string{"a"}, "a")
	require.ErrorIs(t, err, ErrNoDefaultBranch)
	r := f.m.Cleanup(ctx, "29", []*Worktree{wt})[0]
	require.ErrorIs(t, r.Err, ErrNoDefaultBranch)
	assert.True(t, onDisk(t, wt.Path))
}

// Without origin/HEAD, the main checkout's HEAD is no stand-in for the
// default branch when it's detached on the ticket's own work, as when the
// user is looking at it.
func TestWithoutOriginHEADAUserLookingAtTheWorkDoesNotMakeItMerged(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	repo := f.repos["a"]
	wt := f.create("31", []string{"a"}, "a")[0]
	f.commit(wt.Path, "work", "unpushed")
	f.run(repo, "remote", "set-head", "origin", "--delete")
	f.run(repo, "switch", "-q", "--detach", "slop-bot/t31/work")

	r := f.m.Cleanup(context.Background(), "31", []*Worktree{wt})[0]
	require.NoError(t, r.Err)
	assert.False(t, r.Removed)
	assert.Equal(t, []Reason{{
		Kind:   Unpushed,
		Detail: "branch slop-bot/t31/work has 1 commit on no remote, no default branch known (origin/HEAD is unset)",
	}}, r.Reasons)
}

func TestWithoutOriginHEADTheMainCheckoutsBranchCountsAsMerged(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	repo := f.repos["a"]
	wt := f.create("32", []string{"a"}, "a")[0]
	f.commit(wt.Path, "work", "merged locally")
	f.run(repo, "remote", "set-head", "origin", "--delete")
	f.run(repo, "merge", "-q", "--ff-only", "slop-bot/t32/work")

	r := f.m.Cleanup(context.Background(), "32", []*Worktree{wt})[0]
	require.NoError(t, r.Err)
	assert.True(t, r.Removed)
}

// "HEAD" inside a worktree is the worktree's, so the fallback base must be
// resolved in the main checkout.
func TestNewBranchWithoutOriginHEADStartsFromTheMainCheckout(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	repo := f.repos["a"]
	f.run(repo, "remote", "set-head", "origin", "--delete")
	wt := f.create("33", []string{"a"}, "a")[0]
	f.commit(wt.Path, "pr1", "first PR")
	mainHead := f.commit(repo, "main-moved", "main moved on")

	_, err := f.m.NewBranch(context.Background(), wt, "pr2")
	require.NoError(t, err)
	assert.Equal(t, mainHead, f.run(wt.Path, "rev-parse", "HEAD"))
}

func TestNewBranchRefusesToStrandDetachedCommits(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	ctx := context.Background()
	wt := f.create("34", []string{"a"}, "a")[0]
	f.run(wt.Path, "switch", "-q", "--detach")
	head := f.commit(wt.Path, "work", "detached")

	_, err := f.m.NewBranch(ctx, wt, "next")
	require.ErrorIs(t, err, ErrUnsavedWork)
	assert.Equal(t, head, f.run(wt.Path, "rev-parse", "HEAD"))

	// Detached on commits a branch already holds is fine.
	f.run(wt.Path, "switch", "-q", "slop-bot/t34/work")
	f.run(wt.Path, "switch", "-q", "--detach")
	_, err = f.m.NewBranch(ctx, wt, "next")
	require.NoError(t, err)
}

// A commit that lands between the check and the deletion stops the deletion.
func TestABranchThatMovedSinceTheCheckIsNotDeleted(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	ctx := context.Background()
	wt := f.create("35", []string{"a"}, "a")[0]
	st, err := f.m.inspect(ctx, "35", wt)
	require.NoError(t, err)
	head := f.commit(wt.Path, "late", "landed after the check")
	f.run(wt.Path, "switch", "-q", "--detach")

	require.Error(t, f.m.deleteBranches(ctx, wt.RepoPath, st.branches))
	assert.Equal(t, head, f.run(wt.RepoPath, "rev-parse", "slop-bot/t35/work"))
}
