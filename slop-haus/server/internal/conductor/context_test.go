package conductor_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/conductor"
)

func ptr[T any](v T) *T { return &v }

// idle is a ready conversation whose first turn has ended, with the host's
// work acknowledged. nextHostSeq is the host_seq to report next.
func (f *fixture) idle(t *testing.T, owned *agent.OwnedConductor) (*conductor.Conversation, *host, int64) {
	t.Helper()
	c, h, turn := f.replying(t, owned)
	f.report(t, h, c, conductor.HostEvent{HostSeq: 3, Kind: conductor.EventTurnEnd, Turn: &turn})
	return c, h, 4
}

func (f *fixture) workKinds(t *testing.T, h *host, ack int64) []conductor.WorkKind {
	t.Helper()
	var kinds []conductor.WorkKind
	for _, it := range f.poll(t, f.svc, h, ack).Items {
		kinds = append(kinds, it.Kind)
	}
	return kinds
}

func TestAHostReportsContextTokensAndCompactCapability(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c, h, seq := f.idle(t, owned)

	f.report(t, h, c, conductor.HostEvent{HostSeq: seq, Kind: conductor.EventContext, ContextTokens: ptr(int64(1234)), CanCompact: ptr(true)})
	got, err := f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1234, got.ContextTokens)
	assert.True(t, got.CanCompact)

	f.report(t, h, c, conductor.HostEvent{HostSeq: seq + 1, Kind: conductor.EventContext, ContextTokens: ptr(int64(99))})
	got, err = f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 99, got.ContextTokens)
	assert.True(t, got.CanCompact, "a field left out is kept")
	assert.Len(t, f.transcript(t, owned, c), 3, "the report is state, not a transcript event")
}

func TestAContextReportThatSaysNothingOrIsNegativeIsRefused(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c, h, seq := f.idle(t, owned)

	for _, e := range []conductor.HostEvent{
		{HostSeq: seq, Kind: conductor.EventContext},
		{HostSeq: seq, Kind: conductor.EventContext, ContextTokens: ptr(int64(-1))},
		{HostSeq: seq, Kind: conductor.EventContextCleared, Body: "x"},
	} {
		_, err := f.svc.Report(f.ctx, h.proof, c.ID, &conductor.ReportRequest{HostID: h.id, Events: []conductor.HostEvent{e}})
		assert.ErrorIs(t, err, conductor.ErrInvalid)
	}
}

func TestClearQueuesWorkForTheOwningHostOnly(t *testing.T) {
	f := newFixture(t)
	mine := f.owned(t, f.owner, "mine")
	theirs := f.owned(t, f.other, "theirs")
	c, h, _ := f.idle(t, mine)
	otherHost := f.polling(t, theirs)

	got, err := f.svc.Clear(f.ctx, mine, c.ID, false)

	require.NoError(t, err)
	assert.Equal(t, c.ID, got.ID)
	assert.Equal(t, []conductor.WorkKind{conductor.WorkClear}, f.workKinds(t, h, 0))
	assert.Empty(t, f.poll(t, f.svc, otherHost, 0).Items, "another conductor's host is not handed it")
}

func TestClearAndCompactAreRefusedForANonOwner(t *testing.T) {
	f := newFixture(t)
	mine := f.owned(t, f.owner, "mine")
	theirs := f.owned(t, f.other, "theirs")
	c, _, _ := f.idle(t, mine)

	_, err := f.svc.Clear(f.ctx, theirs, c.ID, false)
	assert.ErrorIs(t, err, conductor.ErrNotFound)
	_, err = f.svc.Compact(f.ctx, theirs, c.ID, true)
	assert.ErrorIs(t, err, conductor.ErrNotFound)
	assert.Zero(t, f.count(t, `SELECT count(*) FROM conductor_work WHERE kind IN ('clear', 'compact')`))
}

func TestClearAndCompactOverHTTPRefuseANonOwner(t *testing.T) {
	r := newRest(t)
	c := r.start(t, map[string]any{})

	for _, action := range []string{"clear", "compact"} {
		rec := r.api.do("other", http.MethodPost, r.base+"/"+c.ID+"/"+action, map[string]any{})
		assert.Equal(t, http.StatusForbidden, rec.Code, "%s: %s", action, rec.Body.String())
		rec = r.api.do("owner", http.MethodPost, r.base+"/"+c.ID+"/"+action, map[string]any{"interrupt": true})
		assert.NotEqual(t, http.StatusForbidden, rec.Code, "%s: %s", action, rec.Body.String())
	}
}

func TestClearWhileATurnRunsIsRefusedUnlessItInterrupts(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c, h, turn := f.replying(t, owned)

	_, err := f.svc.Clear(f.ctx, owned, c.ID, false)
	assert.ErrorIs(t, err, conductor.ErrTurnRunning)
	assert.Zero(t, f.count(t, `SELECT count(*) FROM conductor_work WHERE kind IN ('clear', 'interrupt')`), "a refusal queues nothing")

	_, err = f.svc.Clear(f.ctx, owned, c.ID, true)

	require.NoError(t, err)
	res := f.poll(t, f.svc, h, 0)
	require.Len(t, res.Items, 2)
	assert.Equal(t, conductor.WorkInterrupt, res.Items[0].Kind, "the turn is stopped, then the context cleared")
	require.NotNil(t, res.Items[0].Turn)
	assert.Equal(t, turn, *res.Items[0].Turn)
	assert.Equal(t, conductor.WorkClear, res.Items[1].Kind)
	events := f.transcript(t, owned, c)
	assert.Equal(t, conductor.EventInterruptRequested, events[len(events)-1].Kind)
}

func TestAnAlreadyRequestedInterruptIsNotQueuedAgainByAClear(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c, _, turn := f.replying(t, owned)
	_, err := f.svc.Interrupt(f.ctx, owned, c.ID, turn)
	require.NoError(t, err)

	_, err = f.svc.Clear(f.ctx, owned, c.ID, true)

	require.NoError(t, err)
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM conductor_work WHERE kind = 'interrupt'`))
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM conductor_work WHERE kind = 'clear'`), "the clear itself is queued")
}

func TestClearAndCompactAreRefusedWithoutAProcess(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c, _ := f.stopped(t, owned)

	_, err := f.svc.Clear(f.ctx, owned, c.ID, true)
	assert.ErrorIs(t, err, conductor.ErrNoProcess)
	_, err = f.svc.Compact(f.ctx, owned, c.ID, true)
	assert.ErrorIs(t, err, conductor.ErrNoProcess)
	assert.Zero(t, f.count(t, `SELECT count(*) FROM conductor_work WHERE kind IN ('clear', 'compact')`))
}

func TestContextFlowsOverHTTPBothWays(t *testing.T) {
	r := newRest(t)
	hostID := uuid.NewString()
	started := r.start(t, map[string]any{})
	r.api.do(r.hostToken, http.MethodPost, "/api/conductor/poll", map[string]any{"host_id": hostID})
	events := "/api/conductor/conversations/" + started.ID + "/events"
	post := func(evs ...map[string]any) {
		rec := r.api.do(r.hostToken, http.MethodPost, events, map[string]any{"host_id": hostID, "events": evs})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}

	post(map[string]any{"host_seq": 1, "kind": "ready", "work_seq": 1},
		map[string]any{"host_seq": 2, "kind": "context", "context_tokens": 4321, "can_compact": true})
	got := decode[conversationJSON](t, r.api.do("owner", http.MethodGet, r.base+"/"+started.ID, nil))
	assert.EqualValues(t, 4321, got.ContextTokens)
	assert.True(t, got.CanCompact)

	rec := r.api.do("owner", http.MethodPost, r.base+"/"+started.ID+"/compact", map[string]any{})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = r.api.do("owner", http.MethodPost, r.base+"/"+started.ID+"/clear", map[string]any{})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	poll := decode[pollJSON](t, r.api.do(r.hostToken, http.MethodPost, "/api/conductor/poll", map[string]any{"host_id": hostID, "ack": 1}))
	var types []string
	for _, it := range poll.Items {
		types = append(types, it.Type)
	}
	assert.Equal(t, []string{"compact", "clear"}, types)

	post(map[string]any{"host_seq": 3, "kind": "context_compacted"}, map[string]any{"host_seq": 4, "kind": "context_cleared"})
	got = decode[conversationJSON](t, r.api.do("owner", http.MethodGet, r.base+"/"+started.ID, nil))
	assert.Zero(t, got.ContextTokens)
	list := decode[eventsJSON](t, r.api.do("owner", http.MethodGet, r.base+"/"+started.ID+"/events?after=1", nil))
	require.Len(t, list.Events, 2)
	assert.Equal(t, "context_compacted", list.Events[0].Kind)
	assert.Equal(t, "context_cleared", list.Events[1].Kind)
}

func TestCompactIsRefusedWhenTheHarnessCannot(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c, h, seq := f.idle(t, owned)

	_, err := f.svc.Compact(f.ctx, owned, c.ID, false)
	assert.ErrorIs(t, err, conductor.ErrCannotCompact)
	_, err = f.svc.Compact(f.ctx, owned, c.ID, true)
	assert.ErrorIs(t, err, conductor.ErrCannotCompact, "interrupt does not lift it")
	assert.Zero(t, f.count(t, `SELECT count(*) FROM conductor_work WHERE kind = 'compact'`))

	f.report(t, h, c, conductor.HostEvent{HostSeq: seq, Kind: conductor.EventContext, CanCompact: ptr(true)})
	_, err = f.svc.Compact(f.ctx, owned, c.ID, false)
	require.NoError(t, err)
	assert.Equal(t, []conductor.WorkKind{conductor.WorkCompact}, f.workKinds(t, h, 0))
}

func TestTheHostsDividersAreStoredAndAClearResetsTheContext(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c, h, seq := f.idle(t, owned)
	f.report(t, h, c, conductor.HostEvent{HostSeq: seq, Kind: conductor.EventContext, ContextTokens: ptr(int64(5000))})
	before := f.transcript(t, owned, c)

	f.report(t, h, c, conductor.HostEvent{HostSeq: seq + 1, Kind: conductor.EventContextCompacted})
	got, err := f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 5000, got.ContextTokens, "a compact leaves the figure for the host's next report")

	f.report(t, h, c, conductor.HostEvent{HostSeq: seq + 2, Kind: conductor.EventContextCleared})
	got, err = f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	assert.Zero(t, got.ContextTokens)
	assert.Equal(t, conductor.StateReady, got.State, "the conversation is kept")

	after := f.transcript(t, owned, c)
	require.Len(t, after, len(before)+2, "and so is its transcript")
	assert.Equal(t, conductor.EventContextCompacted, after[len(after)-2].Kind)
	assert.Equal(t, conductor.EventContextCleared, after[len(after)-1].Kind)
}
