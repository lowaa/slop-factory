package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An agent's own worktrees under an ignored path (.claude/worktrees/) are
// invisible to `git status`, and `git worktree remove` would delete them,
// files and all, with the ticket's.
func TestANestedWorktreeIsKeptAndNeverForceRemoved(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	ctx := context.Background()
	wt := f.create("60", []string{"a"}, "a")[0]
	f.commit(wt.Path, ".gitignore", ".claude/worktrees/")
	f.run(wt.Path, "push", "-q", "origin", "HEAD")
	sub := filepath.Join(wt.Path, ".claude", "worktrees", "sub")
	f.run(wt.RepoPath, "worktree", "add", "-q", "--detach", sub)
	f.write(sub, "precious", "uncommitted")

	r := f.m.Cleanup(ctx, "60", []*Worktree{wt})[0]
	require.NoError(t, r.Err)
	assert.False(t, r.Removed)
	assert.Equal(t, []ReasonKind{Nested}, kinds(r.Reasons))

	require.ErrorIs(t, f.m.ForceCleanup(ctx, "60", wt), ErrCollision)
	assert.FileExists(t, filepath.Join(sub, "precious"))
}

// A symref under the ticket's prefix isn't one of its branches; deleting
// through it would delete its target.
func TestASymrefAmongTheTicketsBranchesIsLeftAlone(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	repo := f.repos["a"]
	wt := f.create("61", []string{"a"}, "a")[0]
	unpushed := f.commit(repo, "main-work", "unpushed on main")
	f.run(repo, "symbolic-ref", "refs/heads/slop-bot/t61/alias", "refs/heads/main")

	r := f.m.Cleanup(context.Background(), "61", []*Worktree{wt})[0]
	require.NoError(t, r.Err)
	assert.True(t, r.Removed)
	assert.Equal(t, unpushed, f.run(repo, "rev-parse", "refs/heads/main"))
}

// The rebase's state lives in the common git dir even when its checkout is
// on storage that isn't mounted, and the branch is still needed.
func TestABranchRebasingInAnUnmountedWorktreeIsKept(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	wt := f.create("62", []string{"a"}, "a")[0]
	mount := filepath.Join(f.root, "usb")
	users := filepath.Join(mount, "users-checkout")
	f.run(wt.RepoPath, "worktree", "add", "-q", "--no-track", "-b", "slop-bot/t62/side", users, "origin/main")
	f.commit(users, "side", "side work")
	f.run(users, "push", "-q", "origin", "HEAD")
	_, err := f.git.Run(context.Background(), users, "rebase", "--force-rebase", "-x", "false", "origin/main")
	require.Error(t, err)
	f.run(wt.RepoPath, "worktree", "lock", users)
	unmounted := filepath.Join(f.root, "unmounted")
	require.NoError(t, os.Rename(mount, unmounted))

	r := f.m.Cleanup(context.Background(), "62", []*Worktree{wt})[0]
	require.Error(t, r.Err)
	assert.Contains(t, r.Err.Error(), "slop-bot/t62/side")
	assert.True(t, f.branchExists("a", "slop-bot/t62/side"))

	require.NoError(t, os.Rename(unmounted, mount))
	f.run(users, "rebase", "--abort")
}

func TestABranchInAnApplyRebaseOrABisectIsKept(t *testing.T) {
	t.Parallel()
	cases := map[string]func(f *fixture, users string){
		"rebase --apply": func(f *fixture, users string) {
			f.commit(users, "README", "side's README")
			f.run(users, "push", "-q", "origin", "HEAD")
			f.commit(f.repos["a"], "README", "main's README")
			_, err := f.git.Run(context.Background(), users, "rebase", "--apply", "main")
			require.Error(f.t, err, "the conflicting rebase must stop")
		},
		"bisect": func(f *fixture, users string) {
			for _, n := range []string{"one", "two", "three"} {
				f.commit(users, n, n)
			}
			f.run(users, "push", "-q", "origin", "HEAD")
			f.run(users, "bisect", "start", "HEAD", "origin/main")
		},
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, "a")
			wt := f.create("63", []string{"a"}, "a")[0]
			users := filepath.Join(f.root, "users-checkout")
			f.run(wt.RepoPath, "worktree", "add", "-q", "--no-track", "-b", "slop-bot/t63/side", users, "origin/main")
			arrange(f, users)

			r := f.m.Cleanup(context.Background(), "63", []*Worktree{wt})[0]
			require.Error(t, r.Err)
			assert.Contains(t, r.Err.Error(), "slop-bot/t63/side")
			assert.True(t, f.branchExists("a", "slop-bot/t63/side"))
		})
	}
}

// What recover leaves if the add after dropping the registration fails or is
// interrupted: the resume file, from which the next Create carries on.
func TestCreateResumesWhereAnInterruptedRecoverLeftOff(t *testing.T) {
	t.Parallel()
	for _, detached := range []bool{false, true} {
		f := newFixture(t, "a")
		ctx := context.Background()
		wt := f.create("64", []string{"a"}, "a")[0]
		var res *resume
		if detached {
			f.run(wt.Path, "switch", "-q", "--detach")
			res = &resume{Head: f.commit(wt.Path, "work", "detached")}
			f.run(wt.RepoPath, "branch", "slop-bot/t64/saved", res.Head)
		} else {
			_, err := f.m.NewBranch(ctx, wt, "second")
			require.NoError(t, err)
			f.commit(wt.Path, "second", "second PR")
			res = &resume{Branch: "slop-bot/t64/second"}
		}
		require.NoError(t, os.RemoveAll(wt.Path))
		require.NoError(t, f.m.writeResume("64", "a", res))
		require.NoError(t, f.m.unregister(ctx, wt.RepoPath, wt.Path))

		again := f.create("64", []string{"a"}, "a")[0]
		if detached {
			assert.Empty(t, again.Branch)
			assert.Equal(t, res.Head, f.run(again.Path, "rev-parse", "HEAD"))
		} else {
			assert.Equal(t, "slop-bot/t64/second", f.run(again.Path, "branch", "--show-current"))
		}
		assert.False(t, onDisk(t, f.m.resumePath("64", "a")), "the resume file goes once the worktree is back")
	}
}

// recover records what to resume before dropping the registration: if it
// can't, the registration, and with it the branch, must still be there.
func TestRecoverKeepsTheRegistrationIfItCannotRecordWhatToResume(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only directory this relies on")
	}
	f := newFixture(t, "a")
	ctx := context.Background()
	wt := f.create("65", []string{"a"}, "a")[0]
	_, err := f.m.NewBranch(ctx, wt, "second")
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(wt.Path))
	require.NoError(t, os.Chmod(f.ws, 0o500))       // #nosec G302 -- read-only on purpose
	t.Cleanup(func() { _ = os.Chmod(f.ws, 0o700) }) // #nosec G302 -- so TempDir can be removed
	_, err = f.m.Create(ctx, "65", []string{"a"}, "a")
	require.Error(t, err)
	require.NoError(t, os.Chmod(f.ws, 0o700)) // #nosec G302 -- writable again

	again := f.create("65", []string{"a"}, "a")[0]
	assert.Equal(t, "slop-bot/t65/second", again.Branch)
}

// The lookup may name a bare repo; its worktrees work the same.
func TestABareRepoInTheLookup(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	bare := filepath.Join(f.root, "repos", "bare.git")
	f.run(f.root, "clone", "-q", "--bare", f.repos["a"], bare)
	f.repos["bare"] = bare

	wt := f.create("66", []string{"bare"}, "bare")[0]
	assert.Equal(t, "slop-bot/t66/work", f.run(wt.Path, "branch", "--show-current"))
	f.commit(wt.Path, "work", "work")
	f.run(bare, "update-ref", "refs/heads/main", "slop-bot/t66/work")

	r := f.m.Cleanup(context.Background(), "66", []*Worktree{wt})[0]
	require.NoError(t, r.Err)
	assert.True(t, r.Removed)
}
