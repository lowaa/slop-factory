package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
)

// gitRepo makes a repo with one commit, for the daemon's real worktree
// manager to make worktrees of.
func gitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", dir},
		{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		out, err := exec.Command("git", args...).CombinedOutput() // #nosec G204 -- the test's own git calls
		require.NoError(t, err, "git %v: %s", args, out)
	}
}

// A worktree kept for its work at the ticket's end is listed with why, a
// plain clean keeps it, and a confirmed --force discards it. Through the
// real daemon and git: the listing is Cleanup's own verdict.
func TestKeptWorktreesAreListedAndCleanedThroughTheCLI(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	repo, ws := filepath.Join(home, "src", "app"), filepath.Join(home, "ws")
	gitRepo(t, repo)
	e := newE2EWithConfig(t, "user: ada\nharnesses: {claude-code: {}}\nworkspace_dir: "+ws+"\nrepos: [{name: app, path: "+repo+"}]\n")
	s, tk := e.started("--repo", "app")
	wt := filepath.Join(ws, "t"+tk.ID+"-app")
	require.NoError(t, os.WriteFile(filepath.Join(wt, "notes.txt"), []byte("work\n"), 0o600))

	// The agent is live: its worktree is in use, and not the user's to clean.
	assert.Contains(t, e.h.ok(t, "worktrees"), "ticket "+tk.ID+", app: in use")
	require.Equal(t, 1, e.h.run(t, "worktrees", "clean", tk.ID))
	// Refused as live, or as still starting if the start hasn't settled.
	assert.Contains(t, e.h.err.String(), "worktrees are in use")
	// Nor an agent's to see.
	r := e.agentRun(s, "worktrees")
	assert.NotEqual(t, 0, r.Code)
	assert.Contains(t, r.Stderr, "user")

	e.agent(s, "done", "--summary", "Done.")
	e.do(s, fake.CmdTurnEnd)
	e.await("agent.finished", nil)

	listed := e.h.ok(t, "worktrees")
	assert.Contains(t, listed, "ticket "+tk.ID+", app: kept")
	assert.Contains(t, listed, wt)
	assert.Contains(t, listed, "untracked: 1 untracked file")
	assert.Contains(t, listed, "slop-bot worktrees clean "+tk.ID+" --force")
	// The kept message names the command, not manual git steps.
	assert.Contains(t, e.h.ok(t, "inbox"), "`slop-bot worktrees clean "+tk.ID+" --force` discards it")

	assert.Contains(t, e.h.ok(t, "worktrees", "clean", tk.ID), "kept "+wt+", which holds work")
	assert.DirExists(t, wt)

	e.h.deps.Stdin = strings.NewReader("no\n")
	require.Equal(t, 1, e.h.run(t, "worktrees", "clean", tk.ID, "--force"))
	assert.Contains(t, e.h.out.String(), "untracked: 1 untracked file", "it shows what it would discard")
	assert.Contains(t, e.h.err.String(), "nothing was discarded")
	assert.DirExists(t, wt)

	e.h.deps.Stdin = strings.NewReader("discard\n")
	assert.Contains(t, e.h.ok(t, "worktrees", "clean", tk.ID, "--force"), "removed "+wt)
	assert.NoDirExists(t, wt)
	assert.Contains(t, e.h.ok(t, "worktrees"), "no worktrees recorded")
	require.Equal(t, 1, e.h.run(t, "worktrees", "clean", tk.ID))
	assert.Contains(t, e.h.err.String(), "no worktrees recorded")
}
