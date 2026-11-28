package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCleanupSafeguards(t *testing.T) {
	t.Parallel()
	const branch = "slop-bot/t9/work"
	tests := []struct {
		name string
		// arrange leaves the worktree in the state under test.
		arrange func(f *fixture, wt *Worktree)
		kept    []ReasonKind
	}{
		{
			name:    "clean with no commits is removed",
			arrange: func(*fixture, *Worktree) {},
		},
		{
			name:    "uncommitted change to a tracked file is kept",
			arrange: func(f *fixture, wt *Worktree) { f.write(wt.Path, "README", "edited\n") },
			kept:    []ReasonKind{Uncommitted},
		},
		{
			name: "staged change is kept",
			arrange: func(f *fixture, wt *Worktree) {
				f.write(wt.Path, "new", "x")
				f.run(wt.Path, "add", "new")
			},
			kept: []ReasonKind{Uncommitted},
		},
		{
			name:    "untracked file is kept",
			arrange: func(f *fixture, wt *Worktree) { f.write(wt.Path, "notes", "x") },
			kept:    []ReasonKind{Untracked},
		},
		{
			name: "ignored file alone is removed",
			arrange: func(f *fixture, wt *Worktree) {
				f.commit(wt.Path, ".gitignore", "build")
				f.run(wt.Path, "push", "-q", "origin", branch)
				f.write(wt.Path, "build", "output")
			},
		},
		{
			name:    "unpushed commit is kept",
			arrange: func(f *fixture, wt *Worktree) { f.commit(wt.Path, "work", "unpushed") },
			kept:    []ReasonKind{Unpushed},
		},
		{
			name: "commit merged into the local default branch is removed",
			arrange: func(f *fixture, wt *Worktree) {
				f.commit(wt.Path, "work", "merged")
				f.run(wt.RepoPath, "merge", "-q", "--ff-only", branch)
			},
		},
		{
			name: "commit merged into origin's default branch is removed",
			arrange: func(f *fixture, wt *Worktree) {
				f.commit(wt.Path, "work", "merged upstream")
				f.run(wt.Path, "push", "-q", "origin", "HEAD:main")
			},
		},
		{
			// The remote has the commits, so removing the local branch loses
			// nothing; the open PR lives on the remote.
			name: "pushed but unmerged branch is removed",
			arrange: func(f *fixture, wt *Worktree) {
				f.commit(wt.Path, "work", "pushed")
				f.run(wt.Path, "push", "-q", "origin", branch)
			},
		},
		{
			name: "pushed, then another commit on top, is kept",
			arrange: func(f *fixture, wt *Worktree) {
				f.commit(wt.Path, "work", "pushed")
				f.run(wt.Path, "push", "-q", "origin", branch)
				f.commit(wt.Path, "more", "not pushed")
			},
			kept: []ReasonKind{Unpushed},
		},
		{
			name: "an earlier PR's unpushed branch is kept",
			arrange: func(f *fixture, wt *Worktree) {
				f.commit(wt.Path, "work", "pr1, never pushed")
				_, err := f.m.NewBranch(context.Background(), wt, "pr2")
				require.NoError(f.t, err)
			},
			kept: []ReasonKind{Unpushed},
		},
		{
			name: "detached HEAD with commits on no branch is kept",
			arrange: func(f *fixture, wt *Worktree) {
				f.run(wt.Path, "switch", "-q", "--detach")
				f.commit(wt.Path, "work", "detached")
			},
			kept: []ReasonKind{Unpushed},
		},
		{
			name: "dirty and unpushed reports both",
			arrange: func(f *fixture, wt *Worktree) {
				f.commit(wt.Path, "work", "unpushed")
				f.write(wt.Path, "notes", "x")
			},
			kept: []ReasonKind{Untracked, Unpushed},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, "a")
			wt := f.create("9", []string{"a"}, "a")[0]
			tc.arrange(f, wt)

			res := f.m.Cleanup(context.Background(), "9", []*Worktree{wt})
			require.Len(t, res, 1)
			r := res[0]
			require.NoError(t, r.Err)
			assert.Same(t, wt, r.Worktree)
			if tc.kept == nil {
				assert.True(t, r.Removed)
				assert.Empty(t, r.Reasons)
				assert.False(t, exists(t, wt.Path))
				assert.False(t, f.branchExists("a", branch))
				assert.NotContains(t, f.run(wt.RepoPath, "worktree", "list"), wt.Path)
				return
			}
			assert.False(t, r.Removed)
			assert.Equal(t, tc.kept, kinds(r.Reasons))
			for _, reason := range r.Reasons {
				assert.NotEmpty(t, reason.Detail)
			}
			assert.True(t, exists(t, wt.Path))
			assert.True(t, f.branchExists("a", branch), "a kept worktree keeps its branches")
		})
	}
}

func TestCleanupReasonDetails(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	wt := f.create("9", []string{"a"}, "a")[0]
	f.commit(wt.Path, "one", "one")
	f.commit(wt.Path, "two", "two")
	f.write(wt.Path, "README", "edited\n")
	f.write(wt.Path, "x", "x")
	f.write(wt.Path, "y", "y")

	r := f.m.Cleanup(context.Background(), "9", []*Worktree{wt})[0]
	require.NoError(t, r.Err)
	assert.Equal(t, []Reason{
		{Kind: Uncommitted, Detail: "1 file with uncommitted changes"},
		{Kind: Untracked, Detail: "2 untracked files"},
		{Kind: Unpushed, Detail: "branch slop-bot/t9/work has 2 commits on no remote and not merged into refs/remotes/origin/main"},
	}, r.Reasons)
}

func TestCleanupAcrossReposReportsEach(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a", "b", "c")
	wts := f.create("4", []string{"a", "b", "c"}, "a")
	f.write(wts[1].Path, "notes", "x")
	f.commit(wts[2].Path, "work", "pushed")
	f.run(wts[2].Path, "push", "-q", "origin", "HEAD")

	res := f.m.Cleanup(context.Background(), "4", wts)
	require.Len(t, res, 3)
	for _, r := range res {
		require.NoError(t, r.Err)
	}
	assert.True(t, res[0].Removed)
	assert.False(t, res[1].Removed)
	assert.Equal(t, []ReasonKind{Untracked}, kinds(res[1].Reasons))
	assert.True(t, res[2].Removed)
	assert.True(t, exists(t, wts[1].Path))
	assert.False(t, exists(t, wts[0].Path))
	assert.False(t, exists(t, wts[2].Path))
}

// Ticket 4's cleanup must not see ticket 42's branches as its own.
func TestCleanupLeavesOtherTicketsAlone(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	t4 := f.create("4", []string{"a"}, "a")[0]
	t42 := f.create("42", []string{"a"}, "a")[0]
	f.commit(t42.Path, "work", "42's unpushed work")
	f.run(t42.Path, "switch", "-q", "--detach")

	r := f.m.Cleanup(context.Background(), "4", []*Worktree{t4})[0]
	require.NoError(t, r.Err)
	assert.True(t, r.Removed)
	assert.True(t, f.branchExists("a", "slop-bot/t42/work"))
	assert.True(t, exists(t, t42.Path))
}

// A worktree deleted by hand: its registration is pruned, and its branch is
// removed if safe or kept if it holds work.
func TestCleanupOfAWorktreeAlreadyGone(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a", "b")
	wts := f.create("6", []string{"a", "b"}, "a")
	f.commit(wts[1].Path, "work", "unpushed")
	for _, wt := range wts {
		require.NoError(t, os.RemoveAll(wt.Path))
	}

	res := f.m.Cleanup(context.Background(), "6", wts)
	require.NoError(t, res[0].Err)
	assert.True(t, res[0].Removed)
	assert.False(t, f.branchExists("a", "slop-bot/t6/work"))
	assert.NotContains(t, f.run(f.repos["a"], "worktree", "list", "--porcelain"), wts[0].Path)

	require.NoError(t, res[1].Err)
	assert.False(t, res[1].Removed)
	assert.Equal(t, []ReasonKind{Unpushed}, kinds(res[1].Reasons))
	assert.True(t, f.branchExists("b", "slop-bot/t6/work"))
}

func TestCleanupRefusesWhatIsNotOurs(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	ctx := context.Background()
	wt := f.create("8", []string{"a"}, "a")[0]

	// Another ticket's record.
	r := f.m.Cleanup(ctx, "80", []*Worktree{wt})[0]
	require.ErrorIs(t, r.Err, ErrNotOurs)
	assert.False(t, r.Removed)

	// A record whose path is this ticket's but which names another ticket.
	other := *wt
	other.Ticket = "80"
	r = f.m.Cleanup(ctx, "8", []*Worktree{&other})[0]
	require.ErrorIs(t, r.Err, ErrNotOurs)

	// A record pointing outside the flat layout.
	stray := *wt
	stray.Path = f.repos["a"]
	r = f.m.Cleanup(ctx, "8", []*Worktree{&stray})[0]
	require.ErrorIs(t, r.Err, ErrNotOurs)
	require.ErrorIs(t, f.m.ForceCleanup(ctx, "8", &stray), ErrNotOurs)

	// A directory at the right path that isn't the repo's worktree.
	mine := filepath.Join(f.ws, "t81-a")
	require.NoError(t, os.Mkdir(mine, 0o750))
	f.write(mine, "precious", "x")
	notOurs := &Worktree{Ticket: "81", Repo: "a", RepoPath: f.repos["a"], Path: mine}
	r = f.m.Cleanup(ctx, "81", []*Worktree{notOurs})[0]
	require.ErrorIs(t, r.Err, ErrCollision)
	require.ErrorIs(t, f.m.ForceCleanup(ctx, "81", notOurs), ErrCollision)
	assert.FileExists(t, filepath.Join(mine, "precious"))

	assert.True(t, exists(t, wt.Path))
}

// A ticket branch checked out somewhere else can't be deleted; the worktree
// is still reported removed, since it is, with the error for the branch.
func TestCleanupReportsABranchItCouldNotDelete(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	wt := f.create("11", []string{"a"}, "a")[0]
	elsewhere := filepath.Join(f.root, "users-checkout")
	f.run(f.repos["a"], "worktree", "add", "-q", "--no-track", "-b", "slop-bot/t11/side", elsewhere, "origin/main")

	r := f.m.Cleanup(context.Background(), "11", []*Worktree{wt})[0]
	require.Error(t, r.Err)
	assert.True(t, r.Removed)
	assert.False(t, exists(t, wt.Path))
	assert.True(t, exists(t, elsewhere))
}

// With its .git link gone, git run in the worktree would find the repo
// enclosing the workspace, here the workspace itself; nothing may act on it.
func TestABrokenWorktreeNeverReachesAnEnclosingRepo(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	ctx := context.Background()
	wt := f.create("12", []string{"a"}, "a")[0]
	f.run(f.ws, "init", "-q")
	f.run(f.ws, "commit", "-q", "--allow-empty", "-m", "enclosing")
	require.NoError(t, os.Remove(filepath.Join(wt.Path, ".git")))

	r := f.m.Cleanup(ctx, "12", []*Worktree{wt})[0]
	require.ErrorIs(t, r.Err, ErrBroken)
	assert.False(t, r.Removed)
	assert.True(t, exists(t, wt.Path))

	_, err := f.m.NewBranch(ctx, wt, "next")
	require.ErrorIs(t, err, ErrBroken)
	assert.Equal(t, "main", f.run(f.ws, "branch", "--show-current"))
}

func TestForceCleanupRemovesWorkTheUserHasLookedAt(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	ctx := context.Background()
	wt := f.create("10", []string{"a"}, "a")[0]
	f.commit(wt.Path, "work", "unpushed")
	next, err := f.m.NewBranch(ctx, wt, "pr2")
	require.NoError(t, err)
	f.write(wt.Path, "notes", "x")
	f.write(wt.Path, "README", "edited\n")
	f.run(f.repos["a"], "worktree", "lock", wt.Path)

	require.False(t, f.m.Cleanup(ctx, "10", []*Worktree{next})[0].Removed)
	require.NoError(t, f.m.ForceCleanup(ctx, "10", next))
	assert.False(t, exists(t, wt.Path))
	assert.False(t, f.branchExists("a", "slop-bot/t10/work"))
	assert.False(t, f.branchExists("a", "slop-bot/t10/pr2"))

	// Again, with it all gone, is a no-op.
	require.NoError(t, f.m.ForceCleanup(ctx, "10", next))
}
