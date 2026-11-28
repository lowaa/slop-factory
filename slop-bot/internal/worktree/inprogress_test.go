package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stoppedWithAutostash leaves wt mid-operation with an uncommitted edit held
// only in the autostash, and `git status` clean.
var stoppedWithAutostash = map[string]func(f *fixture, wt *Worktree){
	"rebase": func(f *fixture, wt *Worktree) {
		f.commit(wt.Path, "work", "pushed work")
		f.run(wt.Path, "push", "-q", "origin", "HEAD")
		f.write(wt.Path, "README", "uncommitted edit\n")
		_, err := f.git.Run(context.Background(), wt.Path, "rebase", "--autostash", "--force-rebase", "-x", "false", "origin/main")
		require.Error(f.t, err, "the rebase must stop part way")
	},
	"merge": func(f *fixture, wt *Worktree) {
		f.commit(wt.Path, "conflict", "ours")
		f.run(wt.Path, "push", "-q", "origin", "HEAD")
		f.run(wt.Path, "branch", "theirs", "origin/main")
		theirs := f.repos["a"]
		f.run(theirs, "switch", "-q", "theirs")
		f.commit(theirs, "conflict", "theirs")
		f.run(theirs, "switch", "-q", "main")
		f.write(wt.Path, "README", "uncommitted edit\n")
		_, err := f.git.Run(context.Background(), wt.Path, "merge", "--autostash", "theirs")
		require.Error(f.t, err, "the merge must conflict")
		f.run(wt.Path, "checkout", "--ours", "conflict")
		f.run(wt.Path, "add", "conflict")
	},
}

// With worktree.useRelativePaths git writes the admin dir's gitdir file
// relative to it; a lookup that only understood absolute paths would find no
// admin dir and treat the worktree as safe to remove.
func TestRelativeGitdirStillFindsTheAdminDir(t *testing.T) {
	t.Parallel()
	checks := map[string]func(t *testing.T, f *fixture, wt *Worktree){
		"cleanup keeps a mid-rebase worktree": func(t *testing.T, f *fixture, wt *Worktree) {
			r := f.m.Cleanup(context.Background(), "72", []*Worktree{wt})[0]
			require.NoError(t, r.Err)
			assert.False(t, r.Removed, "removing it loses the autostashed edit")
			assert.Contains(t, kinds(r.Reasons), InProgress)
		},
		"a deleted worktree's autostash is saved": func(t *testing.T, f *fixture, wt *Worktree) {
			require.NoError(t, os.RemoveAll(wt.Path))
			f.create("72", []string{"a"}, "a")
			assert.Contains(t, f.run(wt.RepoPath, "stash", "list"), "slop-bot: autostash from "+wt.Path)
		},
	}
	for name, check := range checks {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, "a")
			f.run(f.repos["a"], "config", "worktree.useRelativePaths", "true")
			wt := f.create("72", []string{"a"}, "a")[0]
			stoppedWithAutostash["rebase"](f, wt)
			check(t, f, wt)
		})
	}
}

// A registered worktree whose admin dir can't be matched may be holding an
// autostash, so atRisk refuses rather than guess it's safe. Git itself drops
// such a worktree from its list, so this is reachable only through a path
// mismatch; the test builds that state directly.
func TestAtRiskFailsClosedWhenTheAdminDirIsNotFound(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	st := &state{path: filepath.Join(t.TempDir(), "elsewhere"), reg: &registration{path: "elsewhere"}}

	_, err := f.m.atRisk(context.Background(), f.repos["a"], st)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no admin dir")
}

func TestCleanupKeepsAWorktreeMidOperationWithACleanStatus(t *testing.T) {
	t.Parallel()
	for name, arrange := range stoppedWithAutostash {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, "a")
			wt := f.create("70", []string{"a"}, "a")[0]
			arrange(f, wt)
			require.Empty(t, f.run(wt.Path, "status", "--porcelain"), "the edit is only in the autostash")

			r := f.m.Cleanup(context.Background(), "70", []*Worktree{wt})[0]
			require.NoError(t, r.Err)
			assert.False(t, r.Removed)
			assert.Contains(t, kinds(r.Reasons), InProgress)
			assert.True(t, onDisk(t, wt.Path))
		})
	}
}

// A worktree deleted mid-rebase: the autostash lives in the admin dir that
// re-adding it drops, so it goes to the stash list first.
func TestCreateSavesADeletedWorktreesAutostash(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	wt := f.create("71", []string{"a"}, "a")[0]
	stoppedWithAutostash["rebase"](f, wt)
	require.NoError(t, os.RemoveAll(wt.Path))

	f.create("71", []string{"a"}, "a")
	assert.Contains(t, f.run(wt.RepoPath, "stash", "list"), "slop-bot: autostash from "+wt.Path)
	assert.Contains(t, f.run(wt.RepoPath, "stash", "show", "-p", "stash@{0}"), "+uncommitted edit")
}
