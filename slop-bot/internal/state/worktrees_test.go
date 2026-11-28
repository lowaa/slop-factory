package state_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

// `slop-bot worktrees` reads what is still recorded: an ended agent's kept
// worktrees as well as a live one's, and no agent with none left. Oldest
// first, so a worktree two agents record is listed under the newer.
func TestWithWorktreesListsEveryAgentStillHoldingOne(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	withWorktree := func(id ident.AgentID, ticket ident.TicketID) {
		n := newAgent(id, ticket)
		n.Worktrees = []state.Worktree{{Repo: "app", Path: "/w/t" + string(ticket) + "-app", Primary: true}}
		_, err := f.Agents.Create(ctx, n)
		require.NoError(t, err)
	}
	withWorktree("kept", "1")
	f.setState(t, "kept", state.StateDone)
	withWorktree("cleaned", "2")
	require.NoError(t, f.Agents.RemoveWorktree(ctx, "cleaned", "app"))
	f.createAgent(t, "none", "3")
	// Created in the same millisecond as "kept": the insert order decides.
	withWorktree("live", "1")

	got, err := f.Agents.WithWorktrees(ctx)
	require.NoError(t, err)
	ids := make([]ident.AgentID, 0, len(got))
	for _, a := range got {
		ids = append(ids, a.ID)
	}
	assert.Equal(t, []ident.AgentID{"kept", "live"}, ids)
	assert.NotNil(t, got[0].EndedAt)
	assert.Equal(t, []state.Worktree{{Repo: "app", Path: "/w/t1-app", Primary: true}}, got[1].Worktrees)
}
