package state_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

func TestPruneWithNoCutoffsDeletesNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.createAgent(t, "a1", "1")
	f.setState(t, "a1", state.StateDone)
	f.appendEvent(t, "e")
	f.clock.Advance(365 * 24 * time.Hour)

	r, err := f.Pruner.Prune(ctx, &state.PruneCutoffs{})
	require.NoError(t, err)
	assert.Equal(t, &state.PruneResult{}, r)
	assert.EqualValues(t, 1, f.count(t, "agents"))
	assert.EqualValues(t, 1, f.count(t, "events"))
}

func TestPruneEventsAndStateChangesStrictlyBeforeTheCutoff(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.appendEvent(t, "old")
	f.createAgent(t, "a1", "1") // its first state change, at t0
	f.clock.Advance(time.Hour)
	cutoff := f.clock.Now()
	kept := f.appendEvent(t, "at the cutoff")
	f.setState(t, "a1", state.StateWorking)

	r, err := f.Pruner.Prune(ctx, &state.PruneCutoffs{Events: cutoff, StateChanges: cutoff})
	require.NoError(t, err)
	assert.Equal(t, &state.PruneResult{Events: 1, StateChanges: 1}, r)

	evs, err := f.Events.ReadAfter(ctx, 0, 0)
	require.NoError(t, err)
	assert.Equal(t, []int64{kept}, seqs(evs))
	hist, err := f.Agents.History(ctx, "a1")
	require.NoError(t, err)
	require.Len(t, hist, 1)
	assert.Equal(t, state.StateWorking, hist[0].To)
	// The agent itself is live, so it stays.
	_, err = f.Agents.Get(ctx, "a1")
	assert.NoError(t, err)
}

func TestPruneReadMessagesKeepsUnreadAndOpenAsks(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	read := f.enqueue(t, tell(state.UserParty, "a1", "read"))
	unread := f.enqueue(t, tell(state.UserParty, "a1", "unread"))
	open, err := f.Messages.Ask(ctx, &state.NewAsk{From: "a2", Question: "still open?"})
	require.NoError(t, err)
	answered, err := f.Messages.Ask(ctx, &state.NewAsk{From: "a1", Question: "answered?"})
	require.NoError(t, err)
	_, _, err = f.Messages.Answer(ctx, &state.NewMessage{From: state.UserParty, To: "a1", Body: "yes"})
	require.NoError(t, err)
	_, err = f.Messages.MarkRead(ctx, "a1", []int64{read.ID})
	require.NoError(t, err)
	// The user read both asks; only the answered one may go.
	_, err = f.Messages.MarkRead(ctx, state.UserParty, []int64{open.ID, answered.ID})
	require.NoError(t, err)

	f.clock.Advance(time.Hour)
	r, err := f.Pruner.Prune(ctx, &state.PruneCutoffs{ReadMessages: f.clock.Now()})
	require.NoError(t, err)
	assert.EqualValues(t, 2, r.Messages)

	log, err := f.Messages.Log(ctx, &state.LogQuery{})
	require.NoError(t, err)
	assert.Equal(t, []string{unread.Body, open.Body, "yes"}, bodies(log))
	asks, err := f.Messages.OpenAsks(ctx)
	require.NoError(t, err)
	require.Len(t, asks, 1)
	assert.Equal(t, open.ID, asks[0].ID)
	assert.EqualValues(t, 1, f.count(t, "asks"), "the pruned ask's row went with its message")
}

func TestPruneReadMessagesUsesWhenTheyWereRead(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	m := f.enqueue(t, tell(state.UserParty, "a1", "sent long ago"))
	f.clock.Advance(time.Hour)
	_, err := f.Messages.MarkRead(ctx, "a1", []int64{m.ID})
	require.NoError(t, err)

	r, err := f.Pruner.Prune(ctx, &state.PruneCutoffs{ReadMessages: f.clock.Now()})
	require.NoError(t, err)
	assert.EqualValues(t, 0, r.Messages, "read just now")
}

func TestPruneEndedAgentsKeepsLiveOnesAndOnesWithWorktrees(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.createAgent(t, "ended", "1")
	f.createAgent(t, "kept-worktree", "2")
	require.NoError(t, f.Agents.AddWorktree(ctx, "kept-worktree", &state.Worktree{Repo: "r", Path: "/w/t2-r", Primary: true}))
	f.createAgent(t, "live", "3")
	f.createAgent(t, "recent", "4")
	f.setState(t, "ended", state.StateDone)
	f.setState(t, "kept-worktree", state.StateDone)
	f.clock.Advance(time.Hour)
	cutoff := f.clock.Now()
	f.setState(t, "recent", state.StateReleased)

	r, err := f.Pruner.Prune(ctx, &state.PruneCutoffs{EndedAgents: cutoff})
	require.NoError(t, err)
	assert.EqualValues(t, 1, r.Agents)

	_, err = f.Agents.Get(ctx, "ended")
	assert.ErrorIs(t, err, state.ErrNotFound)
	hist, err := f.Agents.History(ctx, "ended")
	require.NoError(t, err)
	assert.Empty(t, hist, "its history went with it")
	for _, id := range []string{"kept-worktree", "live", "recent"} {
		_, err := f.Agents.Get(ctx, ident.AgentID(id))
		assert.NoError(t, err, id)
	}

	// Once its worktree is cleaned up, the next sweep takes it.
	require.NoError(t, f.Agents.RemoveWorktree(ctx, "kept-worktree", "r"))
	r, err = f.Pruner.Prune(ctx, &state.PruneCutoffs{EndedAgents: cutoff})
	require.NoError(t, err)
	assert.EqualValues(t, 1, r.Agents)
}
