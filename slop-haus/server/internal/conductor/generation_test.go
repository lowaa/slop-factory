package conductor_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/conductor"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

func ready(seq, workSeq int64) conductor.HostEvent {
	return conductor.HostEvent{HostSeq: seq, Kind: conductor.EventReady, WorkSeq: workSeq}
}

// The owner disconnects a conversation and a message reconnects it; the host's
// answer to the first start arrives afterwards. It is the stopped session's word, and must
// not mark the new session ready or failed.
func TestAnAnswerToAnEarlierStartDoesNotSpeakForTheResumedSession(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned) // start is queue item 1
	h := f.polling(t, owned)
	_, err := f.svc.Disconnect(f.ctx, owned, c.ID) // item 2
	require.NoError(t, err)
	_, err = f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: "back"}) // items 3, the resume, and 4
	require.NoError(t, err)

	acked := f.report(t, h, c,
		ready(1, 1),
		conductor.HostEvent{HostSeq: 2, Kind: conductor.EventFailed, Body: "from the old session", WorkSeq: 1})

	assert.Equal(t, int64(2), acked, "consumed, so the host does not send them again")
	got, err := f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	assert.Equal(t, conductor.StateStarting, got.State)
	assert.Nil(t, got.Failure)

	f.report(t, h, c, ready(3, 3))
	got, err = f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	assert.Equal(t, conductor.StateReady, got.State, "the answer to the current start is taken")
}

func (f *fixture) setNextSeq(t *testing.T, c *conductor.Conversation, n int64) {
	t.Helper()
	_, err := f.database.Exec(f.ctx, `UPDATE conductor_conversations SET next_seq = $2 WHERE id = $1`, c.ID, n)
	require.NoError(t, err)
}

// A host with a valid credential can send events with no text, which the byte
// cap does not count. The event cap does.
func TestEventsPastTheEventCapAreDroppedAndMarkedOnce(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	h := f.polling(t, owned)
	f.setNextSeq(t, c, conductor.MaxEventsPerConversation-2)
	var events []conductor.HostEvent
	for i := int64(1); i <= 8; i++ {
		if i%2 == 1 {
			events = append(events, text("x", i)) // a byte each: the byte cap never sees them
		} else {
			events = append(events, conductor.HostEvent{HostSeq: i, Kind: conductor.EventTurnEnd})
		}
	}

	acked := f.report(t, h, c, events...)

	assert.Equal(t, int64(8), acked, "all consumed")
	got, err := f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	assert.True(t, got.Truncated)
	assert.Equal(t, int64(conductor.MaxEventsPerConversation+1), got.LastSeq, "two kept, one marker, the rest dropped")
	var markers int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM conductor_conversation_events WHERE conversation_id = $1 AND kind = 'truncated'`, c.ID).Scan(&markers))
	assert.Equal(t, 1, markers)

	// The owner's own words are refused at the cap, not dropped.
	_, err = f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: "more"})
	assert.ErrorIs(t, err, conductor.ErrTranscriptFull)
}

func TestStateChangesPastTheCapAreAppliedAndRecordedOnlyWithinTheReserve(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	h := f.polling(t, owned)
	f.setNextSeq(t, c, conductor.MaxEventsPerConversation+conductor.EventReserve)

	f.report(t, h, c, ready(1, 1))

	got, err := f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	assert.Equal(t, conductor.StateReady, got.State, "the state is applied")
	assert.Equal(t, int64(conductor.MaxEventsPerConversation+conductor.EventReserve), got.LastSeq, "but a host cannot grow the transcript by repeating it")
}

// A poll that was waiting when another host took the conductor over must not
// be handed work beside the new host.
func TestASupersededHostsWaitingPollIsHandedNothing(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	clock := newManualClock()
	waiting, looks := f.newWatchedService(clock)
	t.Cleanup(waiting.Close)
	old := f.host(t, owned)
	answer := make(chan polled, 1)
	go func() {
		res, err := waiting.Poll(f.ctx, old.proof, &conductor.PollRequest{HostID: old.id, Wait: time.Hour})
		answer <- polled{res, err}
	}()
	// The poll must be parked, its last read of the queue done, before the
	// takeover: a read after it would see the takeover and end the poll
	// without the tick the test waits to give it.
	looks.tickAndLook(t, clock)
	impatient := f.newService(&conductor.Options{HostTTL: time.Nanosecond})
	t.Cleanup(impatient.Close)
	successor := f.host(t, owned)
	_, err := impatient.Poll(f.ctx, successor.proof, &conductor.PollRequest{HostID: successor.id})
	require.NoError(t, err)

	f.start(t, owned)
	clock.tick(t)

	got := awaitPoll(t, answer)
	assert.ErrorIs(t, got.err, conductor.ErrHostBusy)
	assert.Nil(t, got.res)
}

func TestWorkIsReadOnlyByTheCurrentHost(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	f.start(t, owned)
	h := f.polling(t, owned)
	store := conductor.NewPostgresStore(f.database)
	ctx := db.WithTenant(context.Background(), f.tenant)

	items, err := store.ReadWork(ctx, owned.Agent().ID, h.id, 10)
	require.NoError(t, err)
	assert.Len(t, items, 1)
	_, err = store.ReadWork(ctx, owned.Agent().ID, uuid.New(), 10)
	assert.ErrorIs(t, err, conductor.ErrHostBusy)
}

func TestTheServiceKnowsItsLongestPoll(t *testing.T) {
	f := newFixture(t)

	assert.Equal(t, conductor.DefaultMaxWait, f.svc.MaxWait())
	assert.Equal(t, 5*time.Second, f.newService(&conductor.Options{MaxWait: 5 * time.Second}).MaxWait())
}

// A report that answers a queue number the server has not issued is a host
// bug, and is refused whole rather than taken for an answer to work that has
// not been done.
func TestAnAnswerToWorkThatDoesNotExistYetIsRefused(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	h := f.polling(t, owned)

	_, err := f.svc.Report(f.ctx, h.proof, c.ID, &conductor.ReportRequest{HostID: h.id,
		Events: []conductor.HostEvent{text("said", 1), ready(2, 99)}})

	assert.ErrorIs(t, err, conductor.ErrInvalid)
	got, err := f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	assert.Equal(t, conductor.StateStarting, got.State)
	assert.Zero(t, got.LastSeq, "the whole batch was refused, the text before it too")
}
