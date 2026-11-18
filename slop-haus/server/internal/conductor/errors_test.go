package conductor_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/conductor"
)

// failing is Conversations whose every method fails with err, and decides
// nothing else: the handlers' answers to each refusal are what is tested.
type failing struct{ err error }

func (f failing) Start(context.Context, *agent.OwnedConductor, *conductor.StartRequest) (*conductor.Conversation, error) {
	return nil, f.err
}
func (f failing) Send(context.Context, *agent.OwnedConductor, uuid.UUID, *conductor.SendRequest) (*conductor.Event, error) {
	return nil, f.err
}
func (f failing) Disconnect(context.Context, *agent.OwnedConductor, uuid.UUID) (*conductor.Conversation, error) {
	return nil, f.err
}
func (f failing) Interrupt(context.Context, *agent.OwnedConductor, uuid.UUID, int64) (*conductor.Conversation, error) {
	return nil, f.err
}
func (f failing) Clear(context.Context, *agent.OwnedConductor, uuid.UUID, bool) (*conductor.Conversation, error) {
	return nil, f.err
}
func (f failing) Compact(context.Context, *agent.OwnedConductor, uuid.UUID, bool) (*conductor.Conversation, error) {
	return nil, f.err
}
func (f failing) Delete(context.Context, *agent.OwnedConductor, uuid.UUID) error { return f.err }
func (f failing) Rename(context.Context, *agent.OwnedConductor, uuid.UUID, *string) (*conductor.Conversation, error) {
	return nil, f.err
}
func (f failing) Get(context.Context, *agent.OwnedConductor, uuid.UUID) (*conductor.Conversation, error) {
	return nil, f.err
}
func (f failing) Describe(context.Context, *agent.OwnedConductor, uuid.UUID) (*conductor.Detail, error) {
	return nil, f.err
}
func (f failing) HostInfo(context.Context, *agent.OwnedConductor) (conductor.HostStatus, error) {
	return conductor.HostStatus{}, f.err
}
func (f failing) List(context.Context, *agent.OwnedConductor, int) ([]conductor.Conversation, error) {
	return nil, f.err
}
func (f failing) Events(context.Context, *agent.OwnedConductor, uuid.UUID, int64, int) ([]conductor.Event, error) {
	return nil, f.err
}
func (f failing) EventsByTurns(context.Context, *agent.OwnedConductor, uuid.UUID, int64, int) (*conductor.TranscriptPage, error) {
	return nil, f.err
}
func (f failing) AuthenticateHost(context.Context) (*conductor.Host, error) { return nil, f.err }
func (f failing) Poll(context.Context, *conductor.Host, *conductor.PollRequest) (*conductor.PollResult, error) {
	return nil, f.err
}
func (f failing) Report(context.Context, *conductor.Host, uuid.UUID, *conductor.ReportRequest) (int64, error) {
	return 0, f.err
}
func (f failing) ReportLayout(context.Context, *conductor.Host, *agent.Layout) error { return f.err }
func (f failing) Release(context.Context, *conductor.Host, uuid.UUID) error          { return f.err }
func (f failing) MaxWait() time.Duration                                             { return time.Second }
func (f failing) Closed() <-chan struct{}                                            { return nil }

func TestEachRefusalHasItsOwnStatusAndTheUnexpectedOnesLeakNothing(t *testing.T) {
	f := newFixture(t)
	mine := f.owned(t, f.owner, "c1")
	base := "/api/conductors/" + mine.Agent().ID.String() + "/conversations"
	conv := uuid.NewString()

	for _, tc := range []struct {
		err    error
		status int
	}{
		{conductor.ErrNotFound, http.StatusNotFound},
		{conductor.ErrBusy, http.StatusConflict},
		{conductor.ErrHostBusy, http.StatusConflict},
		{conductor.ErrWorkPending, http.StatusConflict},
		{&conductor.GapError{Acked: 3}, http.StatusConflict},
		{agent.ErrIsDisabled, http.StatusConflict},
		{conductor.ErrNotReplying, http.StatusConflict},
		{conductor.ErrTurnRunning, http.StatusConflict},
		{conductor.ErrCannotCompact, http.StatusConflict},
		{conductor.ErrNoProcess, http.StatusConflict},
		{conductor.ErrQueueFull, http.StatusTooManyRequests},
		{conductor.ErrTranscriptFull, http.StatusRequestEntityTooLarge},
		{conductor.ErrInvalid, http.StatusUnprocessableEntity},
		{errors.New("pq: password authentication failed for user slophaus_app"), http.StatusInternalServerError},
	} {
		h := conductor.NewHandler(failing{fmt.Errorf("wrapped: %w", tc.err)}, f.conductors, nil, slog.DiscardHandler)
		api := f.replicaWith(t, h)
		api.users["owner"] = f.owner
		api.agents["host"] = mine.Agent()

		for name, req := range map[string][3]string{
			"start":      {http.MethodPost, base, `{}`},
			"send":       {http.MethodPost, base + "/" + conv + "/messages", `{"text":"x"}`},
			"disconnect": {http.MethodPost, base + "/" + conv + "/disconnect", ``},
			"interrupt":  {http.MethodPost, base + "/" + conv + "/interrupt", `{"turn":1}`},
			"clear":      {http.MethodPost, base + "/" + conv + "/clear", `{}`},
			"compact":    {http.MethodPost, base + "/" + conv + "/compact", `{"interrupt":true}`},
			"delete":     {http.MethodDelete, base + "/" + conv, ``},
			"get":        {http.MethodGet, base + "/" + conv, ``},
			"events":     {http.MethodGet, base + "/" + conv + "/events", ``},
			"host":       {http.MethodGet, "/api/conductors/" + mine.Agent().ID.String() + "/host", ``},
		} {
			rec := api.do("owner", req[0], req[1], req[2])
			assert.Equal(t, tc.status, rec.Code, "%s with %v: %s", name, tc.err, rec.Body.String())
			assertNoLeak(t, rec)
		}
	}
}

func assertNoLeak(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	assert.NotContains(t, rec.Body.String(), "password", "an unexpected error's text is logged, never answered")
	assert.NotContains(t, rec.Body.String(), "wrapped")
}

func TestAHostRouteWithAFailingServiceAnswersLikeTheRest(t *testing.T) {
	f := newFixture(t)
	mine := f.owned(t, f.owner, "c1")
	h := conductor.NewHandler(failing{conductor.ErrNotConductor}, f.conductors, nil, slog.DiscardHandler)
	api := f.replicaWith(t, h)
	api.agents["host"] = mine.Agent()

	rec := api.do("host", http.MethodPost, "/api/conductor/poll", map[string]any{"host_id": uuid.NewString()})

	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}

func TestALayoutThatCannotBeStoredIsA500ThatLeaksNothing(t *testing.T) {
	f := newFixture(t)
	mine := f.owned(t, f.owner, "c1")
	h := conductor.NewHandler(failing{errors.New("pq: secret detail")}, f.conductors, nil, slog.DiscardHandler)
	api := f.replicaWith(t, h)
	api.agents["host"] = mine.Agent()

	rec := api.do("host", http.MethodPost, "/api/conductor/layout", map[string]any{"root": map[string]any{"path": ".", "git": false}, "repos": []any{}})

	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "secret detail")
}
