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

func tell(from, to state.Party, body string) *state.NewMessage {
	return &state.NewMessage{From: from, To: to, Kind: state.KindTell, Body: body}
}

func (f *fixture) enqueue(t *testing.T, n *state.NewMessage) *state.Message {
	t.Helper()
	m, err := f.Messages.Enqueue(context.Background(), n)
	require.NoError(t, err)
	return m
}

func bodies(msgs []state.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Body
	}
	return out
}

func TestPartyOf(t *testing.T) {
	t.Parallel()
	assert.Equal(t, state.UserParty, state.PartyOf(ident.UserPrincipal()))
	assert.Equal(t, state.Party("a1"), state.PartyOf(ident.AgentPrincipal("a1", "1")))
}

func TestEnqueueAndPendingAreOldestFirstPerRecipient(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	m := f.enqueue(t, tell(state.UserParty, "a1", "one"))
	assert.Equal(t, state.Message{
		ID: m.ID, From: state.UserParty, To: "a1", Kind: state.KindTell, Body: "one",
		CreatedAt: t0, Status: state.StatusQueued,
	}, *m)
	f.enqueue(t, tell("a2", "a1", "two"))
	f.enqueue(t, tell("a1", state.UserParty, "for the user"))
	f.enqueue(t, &state.NewMessage{From: state.UserParty, To: "a1", Kind: state.KindNudge, Body: "three"})

	pending, err := f.Messages.Pending(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, []string{"one", "two", "three"}, bodies(pending))
	pending, err = f.Messages.Pending(ctx, state.UserParty)
	require.NoError(t, err)
	assert.Equal(t, []string{"for the user"}, bodies(pending))
}

func TestEnqueueRefusesAsksAndAnswers(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	for _, k := range []state.MessageKind{state.KindAsk, state.KindAnswer} {
		_, err := f.Messages.Enqueue(ctx, &state.NewMessage{From: "a1", To: state.UserParty, Kind: k, Body: "x"})
		assert.Error(t, err, k)
	}
	_, err := f.Messages.Enqueue(ctx, &state.NewMessage{From: "a1", To: state.UserParty, Kind: "gossip", Body: "x"})
	assert.ErrorContains(t, err, "CHECK")
	assert.EqualValues(t, 0, f.count(t, "messages"))
}

func TestMarkDeliveredClaimsAQueuedMessageOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	m := f.enqueue(t, tell(state.UserParty, "a1", "rebase"))

	f.clock.Advance(time.Second)
	require.NoError(t, f.Messages.MarkDelivered(ctx, m.ID, state.DeliverySteer))
	assert.ErrorIs(t, f.Messages.MarkDelivered(ctx, m.ID, state.DeliveryTurn), state.ErrConflict)
	assert.ErrorIs(t, f.Messages.MarkDelivered(ctx, m.ID+100, state.DeliveryTurn), state.ErrNotFound)

	pending, err := f.Messages.Pending(ctx, "a1")
	require.NoError(t, err)
	assert.Empty(t, pending)
	unread, err := f.Messages.Unread(ctx, "a1")
	require.NoError(t, err)
	require.Len(t, unread, 1)
	assert.Equal(t, state.StatusDelivered, unread[0].Status)
	assert.Equal(t, state.DeliverySteer, unread[0].DeliveryPath)
	assert.Equal(t, ptr(t0.Add(time.Second)), unread[0].DeliveredAt)
}

func TestMarkReadIsScopedToTheRecipient(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	queued := f.enqueue(t, tell(state.UserParty, "a1", "queued"))
	pushed := f.enqueue(t, tell(state.UserParty, "a1", "pushed"))
	others := f.enqueue(t, tell(state.UserParty, "a2", "a2's"))
	require.NoError(t, f.Messages.MarkDelivered(ctx, pushed.ID, state.DeliveryHook))

	f.clock.Advance(time.Second)
	n, err := f.Messages.MarkRead(ctx, "a1", []int64{queued.ID, pushed.ID, others.ID})
	require.NoError(t, err)
	assert.EqualValues(t, 2, n, "a2's message isn't a1's to mark")
	n, err = f.Messages.MarkRead(ctx, "a1", []int64{queued.ID})
	require.NoError(t, err)
	assert.EqualValues(t, 0, n, "already read")

	log, err := f.Messages.Log(ctx, &state.LogQuery{})
	require.NoError(t, err)
	require.Len(t, log, 3)
	read := ptr(t0.Add(time.Second))
	assert.Equal(t, state.StatusRead, log[0].Status)
	assert.Equal(t, state.DeliveryInbox, log[0].DeliveryPath, "read before any push: pulled from the inbox")
	assert.Equal(t, read, log[0].DeliveredAt)
	assert.Equal(t, read, log[0].ReadAt)
	assert.Equal(t, state.DeliveryHook, log[1].DeliveryPath, "keeps the path it was pushed by")
	assert.Equal(t, ptr(t0), log[1].DeliveredAt)
	assert.Equal(t, state.StatusQueued, log[2].Status)

	unread, err := f.Messages.Unread(ctx, "a1")
	require.NoError(t, err)
	assert.Empty(t, unread)
}

func TestAsksAndAnswers(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	first, err := f.Messages.Ask(ctx, &state.NewAsk{From: "a1", Question: "archived rows?", Options: []string{"A", "B"}, Recommendation: "A"})
	require.NoError(t, err)
	f.clock.Advance(time.Second)
	_, err = f.Messages.Ask(ctx, &state.NewAsk{From: "a2", Question: "v2?"})
	require.NoError(t, err)
	f.clock.Advance(time.Second)
	_, err = f.Messages.Ask(ctx, &state.NewAsk{From: "a1", Question: "and deleted ones?"})
	require.NoError(t, err)

	open, err := f.Messages.OpenAsks(ctx)
	require.NoError(t, err)
	require.Len(t, open, 3)
	assert.Equal(t, []string{"archived rows?", "v2?", "and deleted ones?"}, []string{open[0].Body, open[1].Body, open[2].Body})
	assert.Equal(t, first, open[0])
	assert.Equal(t, state.KindAsk, open[0].Kind)
	assert.Equal(t, state.UserParty, open[0].To)
	assert.Equal(t, []string{"A", "B"}, open[0].Options)
	assert.Equal(t, []string{}, open[1].Options)

	// The user's reply answers every ask a1 has open, and only a1's.
	answer, n, err := f.Messages.Answer(ctx, &state.NewMessage{From: state.UserParty, To: "a1", Body: "include them"})
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
	assert.Equal(t, state.KindAnswer, answer.Kind)
	open, err = f.Messages.OpenAsks(ctx)
	require.NoError(t, err)
	require.Len(t, open, 1)
	assert.Equal(t, state.Party("a2"), open[0].From)
	mine, err := f.Messages.OpenAsksFrom(ctx, "a1")
	require.NoError(t, err)
	assert.Empty(t, mine)

	var answeredBy []int64
	require.NoError(t, f.db.NewRaw("SELECT answer_message_id FROM asks WHERE closed_at IS NOT NULL ORDER BY message_id").Scan(ctx, &answeredBy))
	assert.Equal(t, []int64{answer.ID, answer.ID}, answeredBy)

	// The answer is delivered like any message.
	pending, err := f.Messages.Pending(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, []string{"include them"}, bodies(pending))

	_, n, err = f.Messages.Answer(ctx, &state.NewMessage{From: state.UserParty, To: "a1", Body: "again"})
	require.NoError(t, err)
	assert.EqualValues(t, 0, n, "nothing left open")
	_, _, err = f.Messages.Answer(ctx, &state.NewMessage{From: "a1", To: state.UserParty, Body: "x"})
	assert.Error(t, err)
}

func TestLogFiltersByPartyAndPagesBack(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.enqueue(t, tell(state.UserParty, "a1", "1"))
	f.enqueue(t, tell("a1", "a2", "2"))
	f.enqueue(t, tell("a2", state.UserParty, "3"))
	f.enqueue(t, tell("a2", "a1", "4"))
	f.enqueue(t, tell(state.UserParty, "a1", "5"))

	all, err := f.Messages.Log(ctx, &state.LogQuery{})
	require.NoError(t, err)
	assert.Equal(t, []string{"1", "2", "3", "4", "5"}, bodies(all))

	a1, err := f.Messages.Log(ctx, &state.LogQuery{Party: "a1"})
	require.NoError(t, err)
	assert.Equal(t, []string{"1", "2", "4", "5"}, bodies(a1), "sent and received")

	page, err := f.Messages.Log(ctx, &state.LogQuery{Party: "a1", Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, []string{"4", "5"}, bodies(page), "the newest page, oldest first")
	page, err = f.Messages.Log(ctx, &state.LogQuery{Party: "a1", Limit: 2, BeforeID: page[0].ID})
	require.NoError(t, err)
	assert.Equal(t, []string{"1", "2"}, bodies(page))
}

func TestLoopGuardCounts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.enqueue(t, tell("a1", "a2", "old"))
	f.clock.Advance(time.Minute)
	since := f.clock.Now()
	f.enqueue(t, tell("a1", "a2", "ping"))
	f.enqueue(t, tell("a2", "a1", "pong"))
	f.enqueue(t, tell("a1", "a3", "elsewhere"))
	f.enqueue(t, tell(state.UserParty, "a1", "from the user"))

	n, err := f.Messages.CountBetween(ctx, "a1", "a2", since)
	require.NoError(t, err)
	assert.EqualValues(t, 2, n, "both directions, inside the window")
	n, err = f.Messages.CountBetween(ctx, "a2", "a1", since.Add(-time.Minute))
	require.NoError(t, err)
	assert.EqualValues(t, 3, n)
	n, err = f.Messages.CountSentBy(ctx, "a1", since)
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
}

func TestOnlyTheUserAnswers(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.Messages.Ask(ctx, &state.NewAsk{From: "a1", Question: "q?"})
	require.NoError(t, err)

	_, _, err = f.Messages.Answer(ctx, &state.NewMessage{From: "a2", To: "a1", Body: "not mine to answer"})
	assert.Error(t, err)
	open, err := f.Messages.OpenAsks(ctx)
	require.NoError(t, err)
	assert.Len(t, open, 1)
}

func TestEndingAnAgentClosesItsAsks(t *testing.T) {
	t.Parallel()
	for _, end := range []state.AgentState{state.StateDone, state.StateReleased} {
		t.Run(string(end), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			ctx := context.Background()
			f.createAgent(t, "a1", "1")
			_, err := f.Messages.Ask(ctx, &state.NewAsk{From: "a1", Question: "mine"})
			require.NoError(t, err)
			_, err = f.Messages.Ask(ctx, &state.NewAsk{From: "a2", Question: "another agent's"})
			require.NoError(t, err)

			f.clock.Advance(time.Minute)
			f.setState(t, "a1", end)
			open, err := f.Messages.OpenAsks(ctx)
			require.NoError(t, err)
			require.Len(t, open, 1)
			assert.Equal(t, "another agent's", open[0].Body)

			var closed []int64
			require.NoError(t, f.db.NewRaw(
				"SELECT closed_at FROM asks WHERE answer_message_id IS NULL AND closed_at IS NOT NULL").Scan(ctx, &closed))
			assert.Equal(t, []int64{t0.Add(time.Minute).UnixMilli()}, closed, "closed unanswered, when it ended")
		})
	}
}
