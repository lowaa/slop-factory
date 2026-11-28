package agents_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/worktree"
)

// The advice names slop-bot's own command, which takes a locked worktree
// too; it refuses one holding a nested worktree, whose files a forced remove
// would delete, so that one is dealt with first.
func TestRemovalAdviceFitsWhyTheWorktreeWasKept(t *testing.T) {
	t.Parallel()
	wt := &worktree.Worktree{RepoPath: "/src/app", Path: "/ws/t1-app"}
	clean := "`slop-bot worktrees clean 1` removes it once nothing in it is at risk, " +
		"and `slop-bot worktrees clean 1 --force` discards it"
	cases := map[string]struct {
		reasons []worktree.Reason
		want    string
	}{
		"uncommitted": {[]worktree.Reason{{Kind: worktree.Uncommitted, Detail: "1 file"}}, clean},
		"locked":      {[]worktree.Reason{{Kind: worktree.Locked, Detail: "locked: none"}}, clean},
		"nested": {[]worktree.Reason{{Kind: worktree.Nested, Detail: "holds the worktree at /ws/t1-app/sub"}},
			"first deal with the worktree inside it (holds the worktree at /ws/t1-app/sub); then " + clean},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, agents.RemovalAdvice("1", &worktree.CleanupResult{Worktree: wt, Reasons: tc.reasons}))
		})
	}
}
