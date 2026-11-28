package state_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

func reminder(body string) *state.NewMessage {
	return &state.NewMessage{From: "a1", To: state.UserParty, Kind: state.KindSystem, Body: body}
}

func TestAsksToRemindAreOpenUnremindedAndOldEnough(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	old, err := f.Messages.Ask(ctx, &state.NewAsk{From: "a1", Question: "old?"})
	require.NoError(t, err)
	_, err = f.Messages.Ask(ctx, &state.NewAsk{From: "a2", Question: "answered?"})
	require.NoError(t, err)
	_, _, err = f.Messages.Answer(ctx, &state.NewMessage{From: state.UserParty, To: "a2", Body: "yes"})
	require.NoError(t, err)
	f.clock.Advance(time.Hour)
	_, err = f.Messages.Ask(ctx, &state.NewAsk{From: "a3", Question: "new?"})
	require.NoError(t, err)

	due, err := f.Messages.AsksToRemind(ctx, f.clock.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, old.ID, due[0].ID)
}

func TestRemindQueuesTheReminderOncePerAsk(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	ask, err := f.Messages.Ask(ctx, &state.NewAsk{From: "a1", Question: "?"})
	require.NoError(t, err)

	ok, err := f.Messages.Remind(ctx, ask.ID, reminder("still waiting"))
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = f.Messages.Remind(ctx, ask.ID, reminder("again"))
	require.NoError(t, err)
	assert.False(t, ok, "once per ask")

	pending, err := f.Messages.Pending(ctx, state.UserParty)
	require.NoError(t, err)
	assert.Equal(t, []string{"?", "still waiting"}, bodies(pending))
	due, err := f.Messages.AsksToRemind(ctx, f.clock.Now())
	require.NoError(t, err)
	assert.Empty(t, due)
}

func TestRemindSkipsAClosedAsk(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	ask, err := f.Messages.Ask(ctx, &state.NewAsk{From: "a1", Question: "?"})
	require.NoError(t, err)
	_, _, err = f.Messages.Answer(ctx, &state.NewMessage{From: state.UserParty, To: "a1", Body: "done"})
	require.NoError(t, err)

	ok, err := f.Messages.Remind(ctx, ask.ID, reminder("too late"))
	require.NoError(t, err)
	assert.False(t, ok)
	pending, err := f.Messages.Pending(ctx, state.UserParty)
	require.NoError(t, err)
	assert.Equal(t, []string{"?"}, bodies(pending), "nothing queued for a closed ask")
}

func TestSetParkedRecordsWhenAndClears(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.createAgent(t, "a1", "1")
	require.NoError(t, f.Agents.SetParked(ctx, "a1", true))
	a, err := f.Agents.Get(ctx, "a1")
	require.NoError(t, err)
	require.NotNil(t, a.ParkedAt)
	assert.Equal(t, t0, *a.ParkedAt)

	require.NoError(t, f.Agents.SetParked(ctx, "a1", false))
	a, err = f.Agents.Get(ctx, "a1")
	require.NoError(t, err)
	assert.Nil(t, a.ParkedAt)
}
