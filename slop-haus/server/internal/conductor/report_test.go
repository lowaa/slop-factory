package conductor_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/conductor"
)

// polling makes h the conductor's host, as its first poll does.
func (f *fixture) polling(t *testing.T, owned *agent.OwnedConductor) *host {
	t.Helper()
	h := f.host(t, owned)
	f.poll(t, f.svc, h, 0)
	return h
}

func (f *fixture) transcript(t *testing.T, owned *agent.OwnedConductor, c *conductor.Conversation) []conductor.Event {
	t.Helper()
	events, err := f.svc.Events(f.ctx, owned, c.ID, 0, 0)
	require.NoError(t, err)
	return events
}

func TestHostEventsBuildTheTranscriptInOrder(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	h := f.polling(t, owned)
	turn := int64(1)

	acked := f.report(t, h, c,
		conductor.HostEvent{HostSeq: 1, Kind: conductor.EventReady, WorkSeq: 1},
		conductor.HostEvent{HostSeq: 2, Kind: conductor.EventAssistant, Turn: &turn, Body: "Hel"},
		conductor.HostEvent{HostSeq: 3, Kind: conductor.EventAssistant, Turn: &turn, Body: "lo"},
		conductor.HostEvent{HostSeq: 4, Kind: conductor.EventTurnEnd, Turn: &turn},
	)

	assert.Equal(t, int64(4), acked)
	got, err := f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	assert.Equal(t, conductor.StateReady, got.State)
	events := f.transcript(t, owned, c)
	require.Len(t, events, 4)
	for i, want := range []conductor.EventKind{conductor.EventReady, conductor.EventAssistant, conductor.EventAssistant, conductor.EventTurnEnd} {
		assert.Equal(t, want, events[i].Kind)
		assert.Equal(t, int64(i+1), events[i].Seq)
	}
	assert.Equal(t, "Hel", events[1].Body)
	assert.Equal(t, &turn, events[1].Turn)
	assert.Equal(t, int64(4), got.LastSeq)
}

// A reply lost on the way back makes the host send the batch again.
func TestAReportSentAgainIsStoredOnce(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	h := f.polling(t, owned)
	batch := []conductor.HostEvent{text("one", 1), text("two", 2)}

	f.report(t, h, c, batch...)
	acked := f.report(t, h, c, append(batch, text("three", 3))...)

	assert.Equal(t, int64(3), acked)
	bodies := []string{}
	for _, e := range f.transcript(t, owned, c) {
		bodies = append(bodies, e.Body)
	}
	assert.Equal(t, []string{"one", "two", "three"}, bodies)
}

func TestAReportThatSkipsAheadIsRefusedWithWhatTheServerHas(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	h := f.polling(t, owned)
	f.report(t, h, c, text("one", 1))

	_, err := f.svc.Report(f.ctx, h.proof, c.ID, &conductor.ReportRequest{HostID: h.id,
		Events: []conductor.HostEvent{text("two", 2), text("four", 4)}})

	var gap *conductor.GapError
	require.ErrorAs(t, err, &gap)
	assert.ErrorIs(t, err, conductor.ErrGap)
	assert.Equal(t, int64(1), gap.Acked)
	assert.Len(t, f.transcript(t, owned, c), 1, "the batch was refused whole, the in-order part of it too")
}

func TestOnlyTheCurrentHostMayReport(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	f.polling(t, owned)
	stranger := f.host(t, owned)

	_, err := f.svc.Report(f.ctx, stranger.proof, c.ID, &conductor.ReportRequest{HostID: stranger.id, Events: []conductor.HostEvent{text("x", 1)}})

	assert.ErrorIs(t, err, conductor.ErrHostBusy)
	assert.Empty(t, f.transcript(t, owned, c))
}

func TestAFailureEndsTheProcessAndTheNextMessageTriesAgain(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	h := f.polling(t, owned)

	f.report(t, h, c, conductor.HostEvent{HostSeq: 1, Kind: conductor.EventFailed, Body: "the working tree is dirty", WorkSeq: 1})

	got, err := f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	assert.Equal(t, conductor.StateFailed, got.State)
	require.NotNil(t, got.Failure)
	assert.Equal(t, "the working tree is dirty", *got.Failure)

	_, err = f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: "try again"})
	require.NoError(t, err)
	got, err = f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	assert.Equal(t, conductor.StateStarting, got.State)
	assert.Nil(t, got.Failure, "the old failure is cleared")
}

func TestALateReadyDoesNotReviveAStoppedConversation(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	h := f.polling(t, owned)
	_, err := f.svc.Disconnect(f.ctx, owned, c.ID)
	require.NoError(t, err)

	acked := f.report(t, h, c,
		conductor.HostEvent{HostSeq: 1, Kind: conductor.EventReady, WorkSeq: 1},
		text("said before it noticed", 2))

	assert.Equal(t, int64(2), acked, "consumed, so the host does not send it again")
	got, err := f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	assert.Equal(t, conductor.StateStopped, got.State)
	kinds := []conductor.EventKind{}
	for _, e := range f.transcript(t, owned, c) {
		kinds = append(kinds, e.Kind)
	}
	assert.Equal(t, []conductor.EventKind{conductor.EventStopped, conductor.EventAssistant}, kinds, "what the model said is kept, the ready is not")
}

func TestHostEventsAreValidated(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	h := f.polling(t, owned)
	tooMany := make([]conductor.HostEvent, conductor.MaxEventsPerBatch+1)

	for name, events := range map[string][]conductor.HostEvent{
		"seq zero":        {{HostSeq: 0, Kind: conductor.EventAssistant, Body: "x"}},
		"unknown kind":    {{HostSeq: 1, Kind: conductor.EventUser, Body: "x"}},
		"empty text":      {{HostSeq: 1, Kind: conductor.EventAssistant}},
		"huge text":       {{HostSeq: 1, Kind: conductor.EventAssistant, Body: strings.Repeat("x", conductor.MaxDeltaBytes+1)}},
		"NUL":             {{HostSeq: 1, Kind: conductor.EventAssistant, Body: "a\x00"}},
		"ready with text": {{HostSeq: 1, Kind: conductor.EventReady, Body: "main", WorkSeq: 1}},
		"ready no work":   {{HostSeq: 1, Kind: conductor.EventReady}},
		"failed no why":   {{HostSeq: 1, Kind: conductor.EventFailed, WorkSeq: 1}},
		"failed no work":  {{HostSeq: 1, Kind: conductor.EventFailed, Body: "why"}},
		"turn_end text":   {{HostSeq: 1, Kind: conductor.EventTurnEnd, Body: "x"}},
		"too many":        tooMany,
		"long block":      {{HostSeq: 1, Kind: conductor.EventAssistant, Body: "x", Block: strings.Repeat("b", conductor.MaxBlockBytes+1)}},
		"activity none":   {{HostSeq: 1, Kind: conductor.EventActivity}},
		"activity status": {{HostSeq: 1, Kind: conductor.EventActivity, Activity: &conductor.Activity{CallID: "c", Tool: "Bash", Status: "weird"}}},
		"activity no id":  {{HostSeq: 1, Kind: conductor.EventActivity, Activity: &conductor.Activity{Tool: "Bash", Status: "done"}}},
		"activity line":   {{HostSeq: 1, Kind: conductor.EventActivity, Activity: &conductor.Activity{CallID: "c", Tool: "Bash", Line: strings.Repeat("x", conductor.MaxActivityLineBytes+1), Status: "done"}}},
	} {
		_, err := f.svc.Report(f.ctx, h.proof, c.ID, &conductor.ReportRequest{HostID: h.id, Events: events})
		assert.ErrorIs(t, err, conductor.ErrInvalid, name)
	}
}

func TestAssistantTextPastTheTranscriptLimitIsDroppedAndMarkedOnce(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	h := f.polling(t, owned)
	chunk := strings.Repeat("x", conductor.MaxDeltaBytes)
	seq := int64(0)
	next := func() int64 { seq++; return seq }
	var batch []conductor.HostEvent
	for range conductor.MaxTranscriptBytes/conductor.MaxDeltaBytes + 3 {
		batch = append(batch, conductor.HostEvent{HostSeq: next(), Kind: conductor.EventAssistant, Body: chunk})
		if len(batch) == conductor.MaxEventsPerBatch {
			f.report(t, h, c, batch...)
			batch = nil
		}
	}
	f.report(t, h, c, batch...)
	acked := f.report(t, h, c, conductor.HostEvent{HostSeq: next(), Kind: conductor.EventTurnEnd})

	assert.Equal(t, seq, acked, "everything was consumed")
	got, err := f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	assert.True(t, got.Truncated)
	var truncated, assistant int
	for after := int64(0); ; {
		events, err := f.svc.Events(f.ctx, owned, c.ID, after, conductor.MaxEventsPerRead)
		require.NoError(t, err)
		if len(events) == 0 {
			break
		}
		for _, e := range events {
			switch e.Kind {
			case conductor.EventTruncated:
				truncated++
			case conductor.EventAssistant:
				assistant++
			}
			after = e.Seq
		}
	}
	assert.Equal(t, 1, truncated)
	assert.Equal(t, conductor.MaxTranscriptBytes/conductor.MaxDeltaBytes, assistant)
	_, err = f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: "and now?"})
	assert.ErrorIs(t, err, conductor.ErrTranscriptFull, "the owner's own words are refused, not dropped")
}

func TestDisconnectingQueuesAStopAndMarksTheTranscript(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)

	stopped, err := f.svc.Disconnect(f.ctx, owned, c.ID)
	require.NoError(t, err)
	again, err := f.svc.Disconnect(f.ctx, owned, c.ID)
	require.NoError(t, err)

	assert.Equal(t, conductor.StateStopped, stopped.State)
	assert.Equal(t, conductor.StateStopped, again.State)
	res := f.poll(t, f.svc, f.host(t, owned), 0)
	assert.Equal(t, []conductor.WorkKind{conductor.WorkStart, conductor.WorkStop}, kinds(res.Items), "a second stop queues nothing")
	events := f.transcript(t, owned, c)
	require.Len(t, events, 1)
	assert.Equal(t, conductor.EventStopped, events[0].Kind)
}

func TestAssistantBlocksAndActivityAreStored(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	h := f.polling(t, owned)
	run := &conductor.Activity{CallID: "toolu_1", Tool: "Bash", Line: "Check <the> pilot", Status: conductor.ActivityRunning}
	done := &conductor.Activity{CallID: "toolu_1", Tool: "Bash", Line: "Check <the> pilot", Status: conductor.ActivityDone}

	f.report(t, h, c,
		conductor.HostEvent{HostSeq: 1, Kind: conductor.EventAssistant, Body: "one", Block: "msg_1"},
		conductor.HostEvent{HostSeq: 2, Kind: conductor.EventActivity, Activity: run},
		conductor.HostEvent{HostSeq: 3, Kind: conductor.EventActivity, Activity: done},
		text("old", 4),
	)

	events := f.transcript(t, owned, c)
	require.Len(t, events, 4)
	require.NotNil(t, events[0].Block)
	assert.Equal(t, "msg_1", *events[0].Block)
	assert.Equal(t, conductor.EventActivity, events[1].Kind)
	assert.JSONEq(t, `{"call_id":"toolu_1","tool":"Bash","line":"Check <the> pilot","status":"running"}`, events[1].Body)
	assert.Contains(t, events[2].Body, `"status":"done"`)
	assert.Nil(t, events[3].Block)
	page, err := f.svc.EventsByTurns(f.ctx, owned, c.ID, 0, 5)
	require.NoError(t, err)
	require.NotNil(t, page.Events[0].Block)
	assert.Equal(t, "msg_1", *page.Events[0].Block)
}

func TestActivityPastThreeQuartersOfTheEventCapIsDroppedWithoutAMark(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	h := f.polling(t, owned)
	limit := int64(conductor.MaxEventsPerConversation * conductor.ActivityEventsShare)
	act := &conductor.Activity{CallID: "c", Tool: "Read", Line: "a.go", Status: conductor.ActivityDone}
	for seq := int64(1); seq <= limit; seq += conductor.MaxEventsPerBatch {
		var batch []conductor.HostEvent
		for s := seq; s < seq+conductor.MaxEventsPerBatch && s <= limit; s++ {
			batch = append(batch, conductor.HostEvent{HostSeq: s, Kind: conductor.EventActivity, Activity: act})
		}
		f.report(t, h, c, batch...)
	}

	f.report(t, h, c,
		conductor.HostEvent{HostSeq: limit + 1, Kind: conductor.EventActivity, Activity: act},
		text("still kept", limit+2),
	)

	got, err := f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	assert.False(t, got.Truncated)
	assert.Equal(t, limit+1, got.LastSeq)
}
