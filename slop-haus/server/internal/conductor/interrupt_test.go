package conductor_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/conductor"
)

// replying is a ready conversation whose turn 1 has begun, with the host's
// work acknowledged so the queue shows only what follows.
func (f *fixture) replying(t *testing.T, owned *agent.OwnedConductor) (*conductor.Conversation, *host, int64) {
	t.Helper()
	c := f.start(t, owned)
	h := f.polling(t, owned)
	turn := int64(1)
	f.report(t, h, c,
		conductor.HostEvent{HostSeq: 1, Kind: conductor.EventReady, WorkSeq: 1},
		conductor.HostEvent{HostSeq: 2, Kind: conductor.EventAssistant, Turn: &turn, Body: "thinking"})
	res := f.poll(t, f.svc, h, 0)
	f.poll(t, f.svc, h, res.Items[len(res.Items)-1].Seq)
	return c, h, turn
}

func TestInterruptRecordsTheRequestAndQueuesAnInterruptForTheHost(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c, h, turn := f.replying(t, owned)

	got, err := f.svc.Interrupt(f.ctx, owned, c.ID, turn)

	require.NoError(t, err)
	assert.Equal(t, conductor.StateReady, got.State, "the process is kept, unlike a disconnect")
	events := f.transcript(t, owned, c)
	last := events[len(events)-1]
	assert.Equal(t, conductor.EventInterruptRequested, last.Kind)
	require.NotNil(t, last.Turn)
	assert.Equal(t, turn, *last.Turn)
	res := f.poll(t, f.svc, h, 0)
	var interrupts []conductor.WorkItem
	for _, it := range res.Items {
		if it.Kind == conductor.WorkInterrupt {
			interrupts = append(interrupts, it)
		}
	}
	require.Len(t, interrupts, 1)
	assert.Equal(t, c.ID, interrupts[0].Conversation)
	require.NotNil(t, interrupts[0].Turn)
	assert.Equal(t, turn, *interrupts[0].Turn)
}

func TestARepeatedInterruptOfTheSameTurnQueuesNothingMore(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c, _, turn := f.replying(t, owned)
	_, err := f.svc.Interrupt(f.ctx, owned, c.ID, turn)
	require.NoError(t, err)
	work := f.count(t, `SELECT count(*) FROM conductor_work WHERE kind = 'interrupt'`)
	seq := f.transcript(t, owned, c)

	_, err = f.svc.Interrupt(f.ctx, owned, c.ID, turn)

	require.NoError(t, err)
	assert.Equal(t, work, f.count(t, `SELECT count(*) FROM conductor_work WHERE kind = 'interrupt'`))
	assert.Len(t, f.transcript(t, owned, c), len(seq), "and no second request is recorded")
}

func TestInterruptingATurnThatHasEndedOrAConversationWithoutAProcessIsRefused(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c, h, turn := f.replying(t, owned)
	f.report(t, h, c, conductor.HostEvent{HostSeq: 3, Kind: conductor.EventTurnEnd, Turn: &turn})

	_, err := f.svc.Interrupt(f.ctx, owned, c.ID, turn)
	assert.ErrorIs(t, err, conductor.ErrNotReplying, "the turn has ended")
	_, err = f.svc.Interrupt(f.ctx, owned, c.ID, 0)
	assert.ErrorIs(t, err, conductor.ErrInvalid)

	_, err = f.svc.Disconnect(f.ctx, owned, c.ID)
	require.NoError(t, err)
	_, err = f.svc.Interrupt(f.ctx, owned, c.ID, 2)
	assert.ErrorIs(t, err, conductor.ErrNotReplying, "no process")
	assert.Zero(t, f.count(t, `SELECT count(*) FROM conductor_work WHERE kind = 'interrupt'`))
}

func TestInterruptIsTheOwnersAndQueuesWithoutAHostPresent(t *testing.T) {
	f := newFixture(t)
	mine := f.owned(t, f.owner, "mine")
	theirs := f.owned(t, f.other, "theirs")
	c := f.start(t, mine)

	_, err := f.svc.Interrupt(f.ctx, theirs, c.ID, 1)
	assert.ErrorIs(t, err, conductor.ErrNotFound)

	// As Disconnect, with the host gone the request is still taken: the host
	// catches up when it returns, and has said it can.
	h := f.polling(t, mine)
	require.NoError(t, f.svc.Release(f.ctx, h.proof, h.id))
	_, err = f.svc.Interrupt(f.ctx, mine, c.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM conductor_work WHERE kind = 'interrupt'`))
}

func TestAnInterruptedTurnEndIsRecordedAfterTheTurnEnd(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c, h, turn := f.replying(t, owned)

	f.report(t, h, c, conductor.HostEvent{HostSeq: 3, Kind: conductor.EventTurnEnd, Turn: &turn, Interrupted: true})

	events := f.transcript(t, owned, c)
	require.GreaterOrEqual(t, len(events), 2)
	end, interrupted := events[len(events)-2], events[len(events)-1]
	assert.Equal(t, conductor.EventTurnEnd, end.Kind)
	assert.Equal(t, conductor.EventInterrupted, interrupted.Kind)
	require.NotNil(t, interrupted.Turn)
	assert.Equal(t, turn, *interrupted.Turn)
}

func TestATurnEndNotInterruptedRecordsNoInterruptedEvent(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c, h, turn := f.replying(t, owned)

	f.report(t, h, c, conductor.HostEvent{HostSeq: 3, Kind: conductor.EventTurnEnd, Turn: &turn})

	assert.Zero(t, f.count(t, `SELECT count(*) FROM conductor_conversation_events WHERE kind = 'interrupted'`))
}

func TestInterruptedMustBeOnATurnEndWithATurn(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c, h, _ := f.replying(t, owned)

	for _, e := range []conductor.HostEvent{
		{HostSeq: 3, Kind: conductor.EventTurnEnd, Interrupted: true},
		{HostSeq: 3, Kind: conductor.EventAssistant, Body: "x", Interrupted: true},
	} {
		_, err := f.svc.Report(f.ctx, h.proof, c.ID, &conductor.ReportRequest{HostID: h.id, Events: []conductor.HostEvent{e}})
		assert.ErrorIs(t, err, conductor.ErrInvalid, "%v", e.Kind)
	}
}

func TestInterruptOverHTTP(t *testing.T) {
	r := newRest(t)
	c := r.start(t, map[string]any{})
	hostID := uuid.NewString()
	url := r.base + "/" + c.ID + "/interrupt"
	hostPoll := func(body map[string]any) *httptest.ResponseRecorder {
		body["host_id"] = hostID
		return r.api.do(r.hostToken, http.MethodPost, "/api/conductor/poll", body)
	}

	rec := hostPoll(map[string]any{})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = r.api.do("owner", http.MethodPost, url, map[string]any{"turn": 1})
	assert.Equal(t, http.StatusConflict, rec.Code, "a host on older code reports no capabilities: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "restart it to enable Stop")
	require.Equal(t, http.StatusOK, hostPoll(map[string]any{"capabilities": []string{"interrupt"}, "version": "abc123"}).Code)
	host := decode[struct {
		Capabilities []string `json:"capabilities"`
		Version      string   `json:"version"`
	}](t, r.api.do("owner", http.MethodGet, "/api/conductors/"+r.mine.Agent().ID.String()+"/host", nil))
	assert.Equal(t, []string{"interrupt"}, host.Capabilities)
	assert.Equal(t, "abc123", host.Version)

	rec = r.api.do("owner", http.MethodPost, url, map[string]any{"turn": 1})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, http.StatusOK, r.api.do("owner", http.MethodPost, url, map[string]any{"turn": 1}).Code, "a repeat")
	assert.Equal(t, http.StatusUnprocessableEntity, r.api.do("owner", http.MethodPost, url, map[string]any{"turn": 0}).Code)

	poll := decode[pollJSON](t, hostPoll(map[string]any{"capabilities": []string{"interrupt"}}))
	var types []string
	for _, it := range poll.Items {
		types = append(types, it.Type)
	}
	assert.Equal(t, []string{"start", "interrupt"}, types, "delivered like the other kinds, once")
	require.NotNil(t, poll.Items[1].Turn)
	assert.Equal(t, int64(1), *poll.Items[1].Turn)

	report := func(events ...map[string]any) int {
		return r.api.do(r.hostToken, http.MethodPost, "/api/conductor/conversations/"+c.ID+"/events",
			map[string]any{"host_id": hostID, "events": events}).Code
	}
	require.Equal(t, http.StatusOK, report(map[string]any{"host_seq": 1, "kind": "ready", "work_seq": 1}))
	assert.Equal(t, http.StatusOK, report(map[string]any{"host_seq": 2, "kind": "turn_end", "turn": 1, "interrupted": true}))
	ev := decode[eventsJSON](t, r.api.do("owner", http.MethodGet, r.base+"/"+c.ID+"/events", nil))
	var got []string
	for _, e := range ev.Events {
		got = append(got, e.Kind)
	}
	assert.Equal(t, []string{"interrupt_requested", "ready", "turn_end", "interrupted"}, got)
}

// pollAsOldHost is a poll from a host whose code predates the capability report.
func (f *fixture) pollAsOldHost(t *testing.T, h *host) {
	t.Helper()
	_, err := f.svc.Poll(f.ctx, h.proof, &conductor.PollRequest{HostID: h.id})
	require.NoError(t, err)
}

func TestInterruptIsRefusedWhenTheHostDoesNotReportTheCapability(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c, h, turn := f.replying(t, owned)
	f.pollAsOldHost(t, h)
	work := f.count(t, `SELECT count(*) FROM conductor_work WHERE kind = 'interrupt'`)

	_, err := f.svc.Interrupt(f.ctx, owned, c.ID, turn)

	assert.ErrorIs(t, err, conductor.ErrHostLacksInterrupt)
	assert.Equal(t, work, f.count(t, `SELECT count(*) FROM conductor_work WHERE kind = 'interrupt'`), "nothing is queued for a host that would ignore it")
	assert.Empty(t, f.describe(t, owned, c).HostCapabilities)
}

func TestInterruptIsRefusedForAConductorWhoseHostHasNeverPolled(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)

	_, err := f.svc.Interrupt(f.ctx, owned, c.ID, 1)

	assert.ErrorIs(t, err, conductor.ErrHostLacksInterrupt)
}

func TestTheNewestHostsCapabilitiesReplaceTheOldOnes(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c, h, _ := f.replying(t, owned)
	d := f.describe(t, owned, c)
	assert.Equal(t, []string{conductor.CapabilityInterrupt}, d.HostCapabilities)
	assert.Equal(t, "test", d.HostVersion)

	f.pollAsOldHost(t, h)

	d = f.describe(t, owned, c)
	assert.Empty(t, d.HostCapabilities)
	assert.Empty(t, d.HostVersion)
}

func TestAPollWithAnOversizedSelfReportIsRefused(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	h := f.host(t, owned)

	_, err := f.svc.Poll(f.ctx, h.proof, &conductor.PollRequest{HostID: h.id, Capabilities: []string{strings.Repeat("x", conductor.MaxHostCapabilityBytes+1)}})

	assert.ErrorIs(t, err, conductor.ErrInvalid)
}

func TestAHostReportingAnUnsupportedWorkKindIsRecordedInTheTranscript(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c, h, _ := f.replying(t, owned)

	f.report(t, h, c, conductor.HostEvent{HostSeq: 3, Kind: conductor.EventUnsupported, Body: "frobnicate"})

	events := f.transcript(t, owned, c)
	last := events[len(events)-1]
	assert.Equal(t, conductor.EventUnsupported, last.Kind)
	assert.Equal(t, "frobnicate", last.Body)
	assert.Equal(t, conductor.StateReady, f.describe(t, owned, c).State, "the conversation is otherwise unaffected")
}

func TestAnUnsupportedReportNeedsTheWorkKind(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c, h, _ := f.replying(t, owned)

	_, err := f.svc.Report(f.ctx, h.proof, c.ID, &conductor.ReportRequest{HostID: h.id, Events: []conductor.HostEvent{{HostSeq: 3, Kind: conductor.EventUnsupported}}})

	assert.ErrorIs(t, err, conductor.ErrInvalid)
}

func TestHostRouteReturnsTheBuildIdTheServerShips(t *testing.T) {
	base := newRest(t)
	api := base.fixture.replica(t, &conductor.Options{CurrentHostVersion: "bbbbbbbbbbbb"}, nil)
	api.users["owner"] = base.fixture.owner
	api.agents[base.hostToken] = base.mine.Agent()

	require.Equal(t, http.StatusOK, api.do(base.hostToken, http.MethodPost, "/api/conductor/poll",
		map[string]any{"host_id": uuid.NewString(), "version": "aaaaaaaaaaaa"}).Code)
	host := decode[struct {
		Version        string `json:"version"`
		CurrentVersion string `json:"current_version"`
	}](t, api.do("owner", http.MethodGet, "/api/conductors/"+base.mine.Agent().ID.String()+"/host", nil))
	assert.Equal(t, "aaaaaaaaaaaa", host.Version)
	assert.Equal(t, "bbbbbbbbbbbb", host.CurrentVersion)
}
