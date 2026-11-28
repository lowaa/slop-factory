package agents_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/worktree"
)

// A forced remove fails on a locked worktree (git wants -f -f or an
// unlock), and on one holding a nested worktree it deletes that worktree's
// files: the advice puts the right step first.
func TestRemovalAdviceFitsWhyTheWorktreeWasKept(t *testing.T) {
	t.Parallel()
	wt := &worktree.Worktree{RepoPath: "/src/app", Path: "/ws/t1-app"}
	remove := "git -C /src/app worktree remove --force /ws/t1-app"
	cases := map[string]struct {
		reasons []worktree.Reason
		want    string
	}{
		"uncommitted": {[]worktree.Reason{{Kind: worktree.Uncommitted, Detail: "1 file"}}, remove},
		"locked": {[]worktree.Reason{{Kind: worktree.Locked, Detail: "locked: none"}},
			"first git -C /src/app worktree unlock /ws/t1-app, then " + remove},
		"nested": {[]worktree.Reason{{Kind: worktree.Nested, Detail: "holds the worktree at /ws/t1-app/sub"}},
			"first deal with the worktrees inside it (holds the worktree at /ws/t1-app/sub), then " + remove},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, agents.RemovalAdvice(&worktree.CleanupResult{Worktree: wt, Reasons: tc.reasons}))
		})
	}
}
