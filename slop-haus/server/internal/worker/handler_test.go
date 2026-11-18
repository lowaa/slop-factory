package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reviewreport"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/supervisorwatch"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/wakeup"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/worker"
)

var handlerTenant = uuid.New()

type identity struct{ id uuid.UUID }

func (i identity) AgentID() uuid.UUID  { return i.id }
func (i identity) AgentName() string   { return "test-agent" }
func (i identity) TenantID() uuid.UUID { return handlerTenant }
func (i identity) ReadOnly() bool      { return false }

type supervisorIdentity struct{ identity }

func (supervisorIdentity) IsSupervisor() bool { return true }

// fakeStates and fakePoller return what the test gives them and record what
// they were asked. What the state is and when a poll ends are tested against
// the real thing; these tests are of the wire.
type fakeStates struct {
	state    *worker.State
	err      error
	tenant   uuid.UUID
	agent    uuid.UUID
	askCount int
}

func (f *fakeStates) State(_ context.Context, tenant, agentID uuid.UUID) (*worker.State, error) {
	f.askCount++
	f.tenant, f.agent = tenant, agentID
	return f.state, f.err
}

type fakePoller struct {
	polled *wakeup.WorkerPoll
	err    error
	agent  uuid.UUID
	req    *wakeup.WorkerPollRequest
	// supervised is whether the poll was the supervisor's.
	supervised bool
}

func (f *fakePoller) SupervisorPoll(_ context.Context, agentID uuid.UUID, req *wakeup.WorkerPollRequest) (*wakeup.WorkerPoll, error) {
	f.supervised = true
	return f.WorkerPoll(context.Background(), agentID, req)
}

func (f *fakePoller) WorkerPoll(_ context.Context, agentID uuid.UUID, req *wakeup.WorkerPollRequest) (*wakeup.WorkerPoll, error) {
	f.agent, f.req = agentID, req
	return f.polled, f.err
}

func (f *fakePoller) WorkerPollTimeout() time.Duration { return time.Second }

type fakeSlots struct {
	err    error
	agent  uuid.UUID
	report *supervisorwatch.SlotReport
	calls  int
}

func (f *fakeSlots) ReportSlot(_ context.Context, agentID uuid.UUID, r *supervisorwatch.SlotReport) error {
	f.calls++
	f.agent, f.report = agentID, r
	return f.err
}

func newMux(states *fakeStates, poller *fakePoller) http.Handler {
	return newMuxWithSlots(states, poller, &fakeSlots{})
}

func newMuxWithSlots(states *fakeStates, poller *fakePoller, slots *fakeSlots) http.Handler {
	return newMuxWithLayouts(states, poller, slots, nil)
}

type fakeLayouts struct {
	agent  uuid.UUID
	layout *agent.Layout
	err    error
}

func (f *fakeLayouts) SaveLayout(_ context.Context, agentID uuid.UUID, l *agent.Layout) error {
	f.agent, f.layout = agentID, l
	return f.err
}

func newMuxWithLayouts(states *fakeStates, poller *fakePoller, slots *fakeSlots, layouts worker.LayoutSaver) http.Handler {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.HumaMiddleware(api,
		func(_ context.Context, token string) (authn.Identity, error) {
			if raw, ok := strings.CutPrefix(token, "supervisor-"); ok {
				id, err := uuid.Parse(raw)
				if err != nil {
					return nil, errors.New("unknown token")
				}
				return supervisorIdentity{identity{id: id}}, nil
			}
			raw, ok := strings.CutPrefix(token, "token-")
			if !ok {
				return nil, errors.New("unknown token")
			}
			id, err := uuid.Parse(raw)
			if err != nil {
				return nil, errors.New("unknown token")
			}
			return identity{id: id}, nil
		}, audit.New(slog.DiscardHandler, nil), slog.New(slog.DiscardHandler)))
	worker.RegisterRoutes(api, worker.NewHandler(states, poller, slots, layouts, slog.DiscardHandler))
	return mux
}

func send(mux http.Handler, method, path string, agent *uuid.UUID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if agent != nil {
		req.Header.Set("Authorization", "Bearer token-"+agent.String())
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestRoutes_RequireAnAgent(t *testing.T) {
	mux := newMux(&fakeStates{state: &worker.State{}}, &fakePoller{polled: &wakeup.WorkerPoll{}})

	assert.Equal(t, http.StatusUnauthorized, send(mux, http.MethodGet, "/api/worker/state", nil, "").Code)
	assert.Equal(t, http.StatusUnauthorized, send(mux, http.MethodPost, "/api/worker/poll", nil, `{}`).Code)
}

func TestGetState_IsTheCallersOwnInItsTenant(t *testing.T) {
	states := &fakeStates{state: &worker.State{}}
	agent := uuid.New()

	rec := send(newMux(states, &fakePoller{}), http.MethodGet, "/api/worker/state", &agent, "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, agent, states.agent)
	assert.Equal(t, handlerTenant, states.tenant)
}

func TestGetState_SaysWhereTheCallerStands(t *testing.T) {
	expires := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	states := &fakeStates{state: &worker.State{
		Lease:   &worker.LeaseState{ID: 7, TicketID: 12, Attempt: 2, ExpiresAt: expires},
		Ticket:  &worker.TicketState{ID: 12, Status: ticket.StatusInProgress, Paths: []string{"repo/server/"}},
		Attempt: &worker.AttemptState{Number: 2, Status: "in_progress", Promoted: true, Session: &ticket.AttemptSession{Harness: "pi", SessionID: "sess-1"}},
		Lock:    worker.LockPlace{Position: 1, Offered: true},
		Checks:  &worker.ChecksState{URL: "https://example.test/pr/1", HeadSHA: "abc", State: "pending"},
	}}
	agent := uuid.New()

	rec := send(newMux(states, &fakePoller{}), http.MethodGet, "/api/worker/state", &agent, "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got worker.WorkerStateResponseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, &worker.WorkerLeaseBody{ID: 7, TicketID: 12, Attempt: 2, ExpiresAt: expires}, got.Lease)
	assert.Equal(t, &worker.WorkerTicketBody{ID: 12, Status: ticket.StatusInProgress, Paths: []string{"repo/server/"}}, got.Ticket)
	assert.Equal(t, &worker.WorkerAttemptBody{Number: 2, Status: "in_progress", Promoted: true, Harness: "pi", HarnessSessionID: "sess-1"}, got.Attempt)
	assert.Nil(t, got.Ended)
	assert.Equal(t, worker.WorkerLockBody{Position: 1, Offered: true}, got.Lock)
	assert.Equal(t, &worker.WorkerChecksBody{URL: "https://example.test/pr/1", HeadSHA: "abc", State: "pending"}, got.Checks)
}

func TestGetState_AnEndedLeaseIsReportedByHowItEnded(t *testing.T) {
	states := &fakeStates{state: &worker.State{
		Ticket:  &worker.TicketState{ID: 12, Status: ticket.StatusReview},
		Attempt: &worker.AttemptState{Number: 1, Status: "review", Promoted: true},
		Ended:   &worker.EndState{Kind: ticket.LeaseFinished, Exit: ticket.ExitReview},
	}}
	agent := uuid.New()

	rec := send(newMux(states, &fakePoller{}), http.MethodGet, "/api/worker/state", &agent, "")

	var got worker.WorkerStateResponseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Nil(t, got.Lease)
	assert.Equal(t, &worker.WorkerEndedBody{Kind: "finished", Exit: "review"}, got.Ended)
}

func TestGetState_NullsAreExplicitForAnAgentWithNoHistory(t *testing.T) {
	agent := uuid.New()

	rec := send(newMux(&fakeStates{state: &worker.State{}}, &fakePoller{}), http.MethodGet, "/api/worker/state", &agent, "")

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	for _, field := range []string{"lease", "ticket", "attempt", "ended", "checks", "review_request"} {
		assert.JSONEq(t, "null", string(raw[field]), field)
	}
}

// The supervisor's contract: the request's head, the change's description
// and when it was made, under review_request.
func TestGetState_SaysTheOpenReviewRequest(t *testing.T) {
	at := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	head := "89abcdef0123456789abcdef0123456789abcdef"
	states := &fakeStates{state: &worker.State{
		Lease:         &worker.LeaseState{ID: 7, TicketID: 12, Attempt: 1},
		ReviewRequest: &reviewreport.Request{TicketID: 12, Attempt: 1, HeadSHA: head, Intent: "Spin the widget.", RequestedAt: at},
	}}
	agent := uuid.New()

	rec := send(newMux(states, &fakePoller{}), http.MethodGet, "/api/worker/state", &agent, "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	assert.JSONEq(t, `{"head_sha": "`+head+`", "intent": "Spin the widget.", "requested_at": "2026-10-19T12:00:00Z"}`, string(raw["review_request"]))
}

func TestGetState_AFailureIsAServerErrorThatLeaksNothing(t *testing.T) {
	agent := uuid.New()

	rec := send(newMux(&fakeStates{err: errors.New("pq: secret detail")}, &fakePoller{}), http.MethodGet, "/api/worker/state", &agent, "")

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "secret detail")
}

func TestPoll_PassesTheRequestForTheCallerAndAnswersItsEvents(t *testing.T) {
	poller := &fakePoller{polled: &wakeup.WorkerPoll{Events: []*wakeup.Wake{
		{Reason: wakeup.ReasonMessages, TicketID: 12, Messages: 2},
		{Reason: wakeup.ReasonFinished, TicketID: 12, LeaseID: 7, Ended: "review"},
	}}}
	agent := uuid.New()

	rec := send(newMux(&fakeStates{}, poller), http.MethodPost, "/api/worker/poll", &agent,
		`{"version":"v1","lease_id":7,"wait_seconds":25}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, agent, poller.agent)
	assert.Equal(t, &wakeup.WorkerPollRequest{Version: "v1", LeaseID: 7, Wait: 25 * time.Second}, poller.req)
	var got worker.WorkerPollResponseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, []worker.WorkerEventBody{
		{Kind: "messages", TicketID: 12, Messages: 2},
		{Kind: "finished", TicketID: 12, LeaseID: 7, Ended: "review"},
	}, got.Events)
}

// A client that decodes `events` as a list must get one when the poll ran out.
func TestPoll_NoEventsIsAnEmptyListNotNull(t *testing.T) {
	agent := uuid.New()

	rec := send(newMux(&fakeStates{}, &fakePoller{polled: &wakeup.WorkerPoll{}}), http.MethodPost, "/api/worker/poll", &agent, `{}`)

	require.Equal(t, http.StatusOK, rec.Code)
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	assert.JSONEq(t, `[]`, string(raw["events"]))
}

func TestPoll_RefusesNegativeNumbers(t *testing.T) {
	agent := uuid.New()
	mux := newMux(&fakeStates{}, &fakePoller{polled: &wakeup.WorkerPoll{}})

	assert.Equal(t, http.StatusUnprocessableEntity, send(mux, http.MethodPost, "/api/worker/poll", &agent, `{"wait_seconds":-1}`).Code)
	assert.Equal(t, http.StatusUnprocessableEntity, send(mux, http.MethodPost, "/api/worker/poll", &agent, `{"lease_id":-1}`).Code)
}

func TestPoll_AFailureIsAServerErrorThatLeaksNothing(t *testing.T) {
	agent := uuid.New()

	rec := send(newMux(&fakeStates{}, &fakePoller{err: errors.New("pq: secret detail")}), http.MethodPost, "/api/worker/poll", &agent, `{}`)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "secret detail")
}

func TestReportSlot_StoresTheCallersOwnReport(t *testing.T) {
	slots := &fakeSlots{}
	agent := uuid.New()

	rec := send(newMuxWithSlots(&fakeStates{}, &fakePoller{}, slots), http.MethodPost, "/api/worker/slot-state", &agent,
		`{"phase":"backoff","phase_age_seconds":12.5,"ticket_id":7,"error_class":"claim_failed","error_streak":3}`)

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Equal(t, agent, slots.agent)
	assert.Equal(t, &supervisorwatch.SlotReport{
		Phase: "backoff", PhaseAge: 12500 * time.Millisecond, TicketID: ptr(int64(7)), ErrorClass: "claim_failed", ErrorStreak: 3,
	}, slots.report)
}

func TestReportSlot_RecordsTheLayoutWithTheCredentialStripped(t *testing.T) {
	layouts := &fakeLayouts{}
	id := uuid.New()

	rec := send(newMuxWithLayouts(&fakeStates{}, &fakePoller{}, &fakeSlots{}, layouts), http.MethodPost, "/api/worker/slot-state", &id,
		`{"phase":"idle","phase_age_seconds":1,"layout":{"root":{"path":".","git":false},"repos":[{"path":"slop-factory","git":true,"origin":"https://u:tok@github.com/o/r"}]}}`)

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Equal(t, id, layouts.agent)
	assert.Equal(t, &agent.Layout{
		Root:  agent.LayoutEntry{Path: ".", Git: false},
		Repos: []agent.LayoutEntry{{Path: "slop-factory", Git: true, Origin: "github.com/o/r"}},
	}, layouts.layout)
}

func TestReportSlot_ALayoutFailureDoesNotFailTheReport(t *testing.T) {
	layouts := &fakeLayouts{err: errors.New("db down")}
	id := uuid.New()

	rec := send(newMuxWithLayouts(&fakeStates{}, &fakePoller{}, &fakeSlots{}, layouts), http.MethodPost, "/api/worker/slot-state", &id,
		`{"phase":"idle","phase_age_seconds":1,"layout":{"root":{"path":".","git":true},"repos":[]}}`)

	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestReportSlot_WithoutALayoutSavesNone(t *testing.T) {
	layouts := &fakeLayouts{}
	id := uuid.New()

	send(newMuxWithLayouts(&fakeStates{}, &fakePoller{}, &fakeSlots{}, layouts), http.MethodPost, "/api/worker/slot-state", &id,
		`{"phase":"idle","phase_age_seconds":1}`)

	assert.Nil(t, layouts.layout)
}

func TestReportSlot_PassesThePauseReasonOn(t *testing.T) {
	slots := &fakeSlots{}
	agent := uuid.New()

	rec := send(newMuxWithSlots(&fakeStates{}, &fakePoller{}, slots), http.MethodPost, "/api/worker/slot-state", &agent,
		`{"phase":"paused","phase_age_seconds":1,"pause_reason":"git push refused"}`)

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Equal(t, "git push refused", slots.report.PauseReason)
}

func TestReportSlot_RequiresAnAgent(t *testing.T) {
	slots := &fakeSlots{}

	rec := send(newMuxWithSlots(&fakeStates{}, &fakePoller{}, slots), http.MethodPost, "/api/worker/slot-state", nil, `{"phase":"idle","phase_age_seconds":1}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Zero(t, slots.calls)
}

func TestReportSlot_RefusesAReportThatIsNotWellFormed(t *testing.T) {
	agent := uuid.New()
	for name, body := range map[string]string{
		"unknown phase":    `{"phase":"sleeping","phase_age_seconds":1}`,
		"no phase":         `{"phase_age_seconds":1}`,
		"negative age":     `{"phase":"idle","phase_age_seconds":-1}`,
		"error as a class": `{"phase":"idle","phase_age_seconds":1,"error_class":"boom: token=abc"}`,
		"overlong class":   `{"phase":"idle","phase_age_seconds":1,"error_class":"` + strings.Repeat("a", 65) + `"}`,
		"ticket zero":      `{"phase":"idle","phase_age_seconds":1,"ticket_id":0}`,
		"negative streak":  `{"phase":"idle","phase_age_seconds":1,"error_streak":-1}`,
	} {
		t.Run(name, func(t *testing.T) {
			slots := &fakeSlots{}
			rec := send(newMuxWithSlots(&fakeStates{}, &fakePoller{}, slots), http.MethodPost, "/api/worker/slot-state", &agent, body)
			assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
			assert.Zero(t, slots.calls)
		})
	}
}

func TestReportSlot_MapsTheServicesVerdicts(t *testing.T) {
	agent := uuid.New()
	body := `{"phase":"idle","phase_age_seconds":1}`

	rec := send(newMuxWithSlots(&fakeStates{}, &fakePoller{}, &fakeSlots{err: supervisorwatch.ErrInvalidReport}), http.MethodPost, "/api/worker/slot-state", &agent, body)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	rec = send(newMuxWithSlots(&fakeStates{}, &fakePoller{}, &fakeSlots{err: errors.New("pq: secret detail")}), http.MethodPost, "/api/worker/slot-state", &agent, body)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "secret detail")
}

func ptr[T any](v T) *T { return &v }

func sendAsSupervisor(mux http.Handler, method, path string, supervisor uuid.UUID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer supervisor-"+supervisor.String())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestPoll_ASupervisorPollsOnceForAllItsSlots(t *testing.T) {
	supervisor, slot := uuid.New(), uuid.New()
	poller := &fakePoller{polled: &wakeup.WorkerPoll{Events: []*wakeup.Wake{{Reason: wakeup.ReasonClaimable, TicketID: 4, SlotID: slot}}}}

	rec := sendAsSupervisor(newMux(&fakeStates{}, poller), http.MethodPost, "/api/worker/poll", supervisor,
		`{"version":"v7","lease_id":4,"slot_leases":{"`+slot.String()+`":9},"wait_seconds":3}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, poller.supervised)
	assert.Equal(t, supervisor, poller.agent)
	assert.Equal(t, &wakeup.WorkerPollRequest{Version: "v7", LeaseID: 4, SlotLeases: map[uuid.UUID]int64{slot: 9}, Wait: 3 * time.Second}, poller.req)
	var body struct{ Events []map[string]any }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, []map[string]any{{"kind": "claimable", "slot_id": slot.String(), "ticket_id": float64(4)}}, body.Events)
}

func TestPoll_AWorkersPollIsNotASupervisors(t *testing.T) {
	agent := uuid.New()
	poller := &fakePoller{polled: &wakeup.WorkerPoll{}}
	mux := newMux(&fakeStates{}, poller)

	rec := send(mux, http.MethodPost, "/api/worker/poll", &agent, `{}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.False(t, poller.supervised)

	poller.req = nil
	rec = send(mux, http.MethodPost, "/api/worker/poll", &agent, `{"slot_leases":{"`+uuid.NewString()+`":1}}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Nil(t, poller.req)
}

func TestPoll_ASupervisorsSlotLeasesAreChecked(t *testing.T) {
	poller := &fakePoller{polled: &wakeup.WorkerPoll{}}
	mux := newMux(&fakeStates{}, poller)
	supervisor := uuid.New()

	for name, body := range map[string]string{
		"not an agent id": `{"slot_leases":{"x":1}}`,
		"negative lease":  `{"slot_leases":{"` + uuid.NewString() + `":-1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, http.StatusUnprocessableEntity, sendAsSupervisor(mux, http.MethodPost, "/api/worker/poll", supervisor, body).Code)
			assert.Nil(t, poller.req, "refused before anything was held open")
		})
	}
}

// A supervisor runs no ticket itself, so the routes about one run's state are
// its slots' and not its own.
func TestASupervisorIsRefusedTheSlotsOwnRoutes(t *testing.T) {
	states, slots := &fakeStates{state: &worker.State{}}, &fakeSlots{}
	mux := newMuxWithSlots(states, &fakePoller{}, slots)
	supervisor := uuid.New()

	assert.Equal(t, http.StatusForbidden, sendAsSupervisor(mux, http.MethodGet, "/api/worker/state", supervisor, "").Code)
	assert.Equal(t, http.StatusForbidden, sendAsSupervisor(mux, http.MethodPost, "/api/worker/slot-state", supervisor,
		`{"phase":"idle","phase_age_seconds":1}`).Code)
	assert.Zero(t, states.askCount)
	assert.Zero(t, slots.calls)
}

// The protocol is in the body of a poll and in X-Supervisor-Version on every
// supervisor request; the body wins, and a header that is not a whole number is
// ignored rather than refusing the poll.
func TestPoll_TheProtocolComesFromTheBodyOrTheHeader(t *testing.T) {
	for name, tc := range map[string]struct {
		body, header string
		want         int
	}{
		"body":                       {`{"protocol":3}`, "", 3},
		"header":                     {`{}`, "2", 2},
		"the body wins":              {`{"protocol":3}`, "2", 3},
		"neither":                    {`{}`, "", 0},
		"a header that is no number": {`{}`, "two", 0},
		"a negative header":          {`{}`, "-4", 0},
	} {
		t.Run(name, func(t *testing.T) {
			poller := &fakePoller{polled: &wakeup.WorkerPoll{}}
			req := httptest.NewRequest(http.MethodPost, "/api/worker/poll", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer supervisor-"+uuid.NewString())
			if tc.header != "" {
				req.Header.Set("X-Supervisor-Version", tc.header)
			}
			rec := httptest.NewRecorder()
			newMux(&fakeStates{}, poller).ServeHTTP(rec, req)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, tc.want, poller.req.Protocol)
		})
	}
}

func TestPoll_AnUnsupportedProtocolIsAnEventWithAReadableMessage(t *testing.T) {
	poller := &fakePoller{polled: &wakeup.WorkerPoll{Events: []*wakeup.Wake{{Reason: wakeup.ReasonProtocolUnsupported, Message: "run `slop-haus update`"}}}}

	rec := sendAsSupervisor(newMux(&fakeStates{}, poller), http.MethodPost, "/api/worker/poll", uuid.New(), `{"protocol":1}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct{ Events []map[string]any }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, []map[string]any{{"kind": "protocol_unsupported", "message": "run `slop-haus update`"}}, body.Events)
}

func TestReportSlot_PassesTheWorkspaceBundleHash(t *testing.T) {
	slots := &fakeSlots{}
	agent := uuid.New()

	rec := send(newMuxWithSlots(&fakeStates{}, &fakePoller{}, slots), http.MethodPost, "/api/worker/slot-state", &agent,
		`{"phase":"idle","phase_age_seconds":1,"bundle_hash":"c1ff58edfd3c9bb6"}`)

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Equal(t, "c1ff58edfd3c9bb6", slots.report.BundleHash)
}

func TestReportSlot_PassesIdleAndDefaultsToNotIdle(t *testing.T) {
	slots := &fakeSlots{}
	agent := uuid.New()
	mux := newMuxWithSlots(&fakeStates{}, &fakePoller{}, slots)

	rec := send(mux, http.MethodPost, "/api/worker/slot-state", &agent, `{"phase":"idle","phase_age_seconds":1,"idle":true}`)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.True(t, slots.report.Idle)

	rec = send(mux, http.MethodPost, "/api/worker/slot-state", &agent, `{"phase":"idle","phase_age_seconds":1}`)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.False(t, slots.report.Idle)
}
