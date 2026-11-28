package agents_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
	"github.com/lowaa/slop-factory/slop-bot/internal/worktree"
)

// A first start that failed before its agent was recorded tells the user to
// clean what it couldn't remove: the ticket's repos find it.
func TestACleanFindsAWorktreeNothingRecords(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	tk := f.file("t", func(nt *tickets.NewTicket) { nt.Assignee = "" })
	path := filepath.Join(f.cfg.WorkspaceDir, "t"+string(tk.ID)+"-app")
	require.NoError(t, os.MkdirAll(path, 0o700))

	res, err := f.mgr.CleanWorktrees(f.ctx(), tk.ID, false)
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.True(t, res[0].Removed)
	assert.Equal(t, []string{path}, f.wts.removed)
}

// A worktree released to a person is theirs: only --force touches it.
func TestAPlainCleanLeavesAReleasedWorktreeAlone(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	ctx := f.ctx()
	f.recordAgent("t9-mine", "9", true)
	_, err := f.state.Agents.SetState(ctx, &state.Transition{Agent: "t9-mine", To: state.StateReleased})
	require.NoError(t, err)

	list, err := f.mgr.ListWorktrees(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, agents.WorktreeReleased, list[0].Status)

	res, err := f.mgr.CleanWorktrees(ctx, "9", false)
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.False(t, res[0].Removed)
	require.Len(t, res[0].Reasons, 1)
	assert.Equal(t, agents.Released, res[0].Reasons[0].Kind)
	assert.Empty(t, f.wts.removed)

	res, err = f.mgr.CleanWorktrees(ctx, "9", true)
	require.NoError(t, err)
	assert.True(t, res[0].Removed)
}

// Check finds nothing at risk: what it finds is the real manager's to
// decide, and tested there.
func (w *recordingWorktrees) Check(context.Context, ident.TicketID, *worktree.Worktree) ([]worktree.Reason, error) {
	return nil, nil
}

func (w *recordingWorktrees) ForceCleanup(_ context.Context, ticket ident.TicketID, wt *worktree.Worktree) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cleanups = append(w.cleanups, ticket)
	if wt.RepoPath == "" {
		return errors.New("no repo path")
	}
	// Marked, so a test can tell a forced removal from Cleanup's.
	w.removed = append(w.removed, wt.Path+" (forced)")
	return nil
}

// recordAgent records an agent on ticket holding the app worktree, ended
// unless live.
func (f *fixture) recordAgent(id ident.AgentID, ticket ident.TicketID, live bool) {
	f.t.Helper()
	ctx := context.Background()
	_, err := f.state.Agents.Create(ctx, &state.NewAgent{
		ID: id, Ticket: ticket, Harness: "claude-code", Driver: "rpc", Pool: state.PoolNormal,
		State: state.StateWorking, TokenHash: control.HashToken("token-" + string(id)),
		Worktrees: []state.Worktree{{Repo: "app", Path: "/ws/t" + string(ticket) + "-app", Branch: "slop-bot/t" + string(ticket) + "/work", Primary: true}},
	})
	require.NoError(f.t, err)
	if !live {
		_, err = f.state.Agents.SetState(ctx, &state.Transition{Agent: id, To: state.StateDone})
		require.NoError(f.t, err)
	}
}

// A worktree an ended agent kept, and the ticket's next agent reused, is
// recorded twice: it is listed once, under the newer agent, and a clean
// forgets it for both.
func TestAKeptWorktreeIsListedOnceAndCleanedForEveryAgentRecordingIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	ctx := f.ctx()
	f.recordAgent("t9-old", "9", false)
	f.recordAgent("t9-new", "9", false)
	f.recordAgent("t8-live", "8", true)

	list, err := f.mgr.ListWorktrees(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, ident.AgentID("t9-new"), list[0].Agent.ID)
	assert.Equal(t, agents.WorktreeRemovable, list[0].Status)
	assert.Equal(t, "slop-bot/t9/work", list[0].Worktree.Branch)
	assert.Equal(t, ident.AgentID("t8-live"), list[1].Agent.ID)
	assert.Equal(t, agents.WorktreeInUse, list[1].Status)

	res, err := f.mgr.CleanWorktrees(ctx, "9", false)
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.True(t, res[0].Removed)
	assert.Equal(t, []string{"/ws/t9-app"}, f.wts.removed)
	left, err := f.state.Agents.WithWorktrees(ctx)
	require.NoError(t, err)
	require.Len(t, left, 1)
	assert.Equal(t, ident.AgentID("t8-live"), left[0].ID)

	_, err = f.mgr.CleanWorktrees(ctx, "9", false)
	assert.ErrorIs(t, err, agents.ErrNoWorktrees)
}

// A live agent's worktrees are its to work in; they're cleaned up when its
// ticket ends.
func TestALiveAgentsWorktreesArentCleaned(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	f.recordAgent("t8-live", "8", true)
	for _, force := range []bool{false, true} {
		_, err := f.mgr.CleanWorktrees(f.ctx(), "8", force)
		require.ErrorIs(t, err, agents.ErrWorktreesInUse)
		assert.Contains(t, err.Error(), "t8-live")
	}
	assert.Empty(t, f.wts.removed)
}

func TestAForcedCleanDiscardsEachWorktree(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	f.recordAgent("t9-old", "9", false)
	res, err := f.mgr.CleanWorktrees(f.ctx(), "9", true)
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.True(t, res[0].Removed)
	assert.Equal(t, []string{"/ws/t9-app (forced)"}, f.wts.removed)
}
