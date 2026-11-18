package ticket_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/harness"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

func (f *fixture) agentPost(t *testing.T, agentID uuid.UUID, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	authorize(req, agentID)
	rec := httptest.NewRecorder()
	f.agentMux().ServeHTTP(rec, req)
	return rec
}

func TestRecordSession_SetsAndOverwritesTheHeldAttemptsSession(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	f.ticket(t, "Work", "server/")
	f.heldLease(t, a)

	require.NoError(t, f.svc.RecordSession(f.ctx, a, &ticket.AttemptSession{Harness: "claude", SessionID: "sess-1"}))
	snap, err := f.svc.WorkerSnapshot(f.ctx, a)
	require.NoError(t, err)
	assert.Equal(t, &ticket.AttemptSession{Harness: "claude", SessionID: "sess-1"}, snap.Session)

	require.NoError(t, f.svc.RecordSession(f.ctx, a, &ticket.AttemptSession{Harness: "pi", SessionID: "sess-2"}))
	snap, err = f.svc.WorkerSnapshot(f.ctx, a)
	require.NoError(t, err)
	assert.Equal(t, &ticket.AttemptSession{Harness: "pi", SessionID: "sess-2"}, snap.Session, "a new run replaces the old session")
}

func TestRecordSession_NeedsAHeldLease(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")

	err := f.svc.RecordSession(f.ctx, a, &ticket.AttemptSession{Harness: "claude", SessionID: "s"})

	assert.ErrorIs(t, err, ticket.ErrNoLease)
}

func TestRecordSession_RefusesWhatCannotBeResumed(t *testing.T) {
	t.Parallel()
	cases := map[string]*ticket.AttemptSession{
		"unknown harness": {Harness: "gpt", SessionID: "s"},
		"no session id":   {Harness: "claude"},
		"whitespace":      {Harness: "claude", SessionID: "a b"},
		"control":         {Harness: "claude", SessionID: "a\nb"},
		"too long":        {Harness: "claude", SessionID: strings.Repeat("x", 201)},
	}
	for name, session := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			a := f.agent(t, "builder-1")
			f.ticket(t, "Work", "server/")
			f.heldLease(t, a)

			err := f.svc.RecordSession(f.ctx, a, session)

			require.Error(t, err)
			snap, err := f.svc.WorkerSnapshot(f.ctx, a)
			require.NoError(t, err)
			assert.Nil(t, snap.Session)
		})
	}
}

func TestClaim_ReturnsTheSessionAnEarlierRunLeft(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Work", "server/")
	f.heldLease(t, a)
	require.NoError(t, f.svc.RecordSession(f.ctx, a, &ticket.AttemptSession{Harness: "claude", SessionID: "sess-1"}))
	f.discuss(t, a)
	f.humanReply(t, created.ID)

	rec := f.agentPost(t, a, "/api/claims", `{}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body ticket.ClaimResponseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotNil(t, body.Attempt)
	assert.Equal(t, "claude", body.Attempt.Harness)
	assert.Equal(t, "sess-1", body.Attempt.HarnessSessionID)
}

func TestRecordSessionRoute(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")

	t.Run("no lease is a conflict", func(t *testing.T) {
		rec := f.agentPost(t, a, "/api/claims/session", `{"harness":"claude","session_id":"s"}`)
		assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	})

	f.ticket(t, "Work", "server/")
	f.heldLease(t, a)

	t.Run("records", func(t *testing.T) {
		rec := f.agentPost(t, a, "/api/claims/session", `{"harness":"pi","session_id":"s-9"}`)
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		snap, err := f.svc.WorkerSnapshot(f.ctx, a)
		require.NoError(t, err)
		assert.Equal(t, &ticket.AttemptSession{Harness: "pi", SessionID: "s-9"}, snap.Session)
	})

	t.Run("an unknown harness is refused", func(t *testing.T) {
		rec := f.agentPost(t, a, "/api/claims/session", `{"harness":"gpt","session_id":"s"}`)
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	})
}

func TestEscalate_SendsTheTicketToDiscussionWithTheSupervisorsNote(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, other := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	created := f.ticket(t, "Work", "server/")
	f.heldLease(t, a)

	got, err := f.svc.Escalate(f.ctx, a, "gave up after 3 crashes")

	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDiscussion, got.Status)
	_, err = f.svc.LeaseFor(f.ctx, a)
	assert.ErrorIs(t, err, ticket.ErrNoLease, "the lease is released")

	comments, err := f.svc.ListComments(f.ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, "[supervisor] gave up after 3 crashes", comments[0].Body)

	contexts, err := f.svc.ListAgentContexts(f.ctx, created.ID)
	require.NoError(t, err)
	require.NotEmpty(t, contexts)
	assert.Equal(t, "gave up after 3 crashes", contexts[len(contexts)-1].Body)

	f.humanReply(t, created.ID)
	assert.Nil(t, f.claim(t, other), "first claim is owed to the escalating agent")
	assert.NotNil(t, f.claim(t, a))
}

func TestEscalate_DoesNotSatisfyANeedsDiscussionTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	_, err := f.svc.Create(f.ctx, &ticket.CreateParams{
		Title: "Rough idea", NeedsDiscussion: true, Paths: []model.Path{{Path: "server/"}},
	})
	require.NoError(t, err)
	f.heldLease(t, a)

	got, err := f.svc.Escalate(f.ctx, a, "crashed")

	require.NoError(t, err)
	assert.True(t, got.NeedsDiscussion, "no model asked the question")
}

func TestEscalate_Refusals(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")

	_, err := f.svc.Escalate(f.ctx, a, "note")
	assert.ErrorIs(t, err, ticket.ErrNoLease)

	f.ticket(t, "Work", "server/")
	f.heldLease(t, a)
	_, err = f.svc.Escalate(f.ctx, a, "  ")
	assert.ErrorIs(t, err, ticket.ErrEmptyNote)
}

func TestEscalateRoute(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	assert.Equal(t, http.StatusConflict, f.agentPost(t, a, "/api/claims/escalate", `{"note":"x"}`).Code)

	f.ticket(t, "Work", "server/")
	f.heldLease(t, a)
	assert.Equal(t, http.StatusUnprocessableEntity, f.agentPost(t, a, "/api/claims/escalate", `{"note":""}`).Code)

	rec := f.agentPost(t, a, "/api/claims/escalate", `{"note":"auth failed"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body ticket.TicketBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, ticket.StatusDiscussion, body.Status)
}

func TestRecordSession_MustBeOnTheHarnessOfTheLeasesVariant(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	version := f.variantVersion(t, "pi")
	f.with(ticket.WithConfigResolver(&servedStub{served: &ticket.ServedConfig{
		Settings: map[string]map[string]string{"pi": {}},
		Variant:  &ticket.ServedVariant{VersionID: version, Name: "fast", Version: 1, Harness: "pi"},
	}}))
	routed := f.ticket(t, "work", "a/")
	f.routeTicket(t, routed.ID, version)
	a := f.agentOnVariant(t, "on-pi", version)
	_, err := f.svc.ClaimWith(f.ctx, a, &ticket.ClaimOptions{Harness: "pi"})
	require.NoError(t, err)

	err = f.svc.RecordSession(f.ctx, a, &ticket.AttemptSession{Harness: "claude", SessionID: "s"})
	require.ErrorIs(t, err, ticket.ErrSessionHarnessMismatch)
	assert.Equal(t, http.StatusUnprocessableEntity,
		f.agentPost(t, a, "/api/claims/session", `{"harness":"claude","session_id":"s"}`).Code)

	require.NoError(t, f.svc.RecordSession(f.ctx, a, &ticket.AttemptSession{Harness: "pi", SessionID: "s"}))
}

func TestClaimNext_DeclaredHarnessReachesTheVariantCheck(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	version := f.variantVersion(t, "pi")
	f.with(ticket.WithConfigResolver(&servedStub{served: &ticket.ServedConfig{
		Settings: map[string]map[string]string{"pi": {}},
		Variant:  &ticket.ServedVariant{VersionID: version, Name: "fast", Version: 1, Harness: "pi"},
	}}))
	routed := f.ticket(t, "work", "a/")
	f.routeTicket(t, routed.ID, version)
	a := f.agentOnVariant(t, "on-pi", version)

	assert.Equal(t, http.StatusConflict, f.agentPost(t, a, "/api/claims", `{"harness":"claude"}`).Code)
	assert.Equal(t, http.StatusUnprocessableEntity, f.agentPost(t, a, "/api/claims", `{"harness":"gpt"}`).Code)
	rec := f.agentPost(t, a, "/api/claims", `{"harness":"pi"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body ticket.ClaimResponseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotNil(t, body.Ticket)
}

func TestRecordSession_AcceptsEveryRegisteredHarness(t *testing.T) {
	t.Parallel()
	for _, name := range harness.Names() {
		t.Run(name, func(t *testing.T) {
			app := dbtest.WithDB(t)
			f := newFixtureOn(t, app)
			a := f.agent(t, "builder-1")
			f.ticket(t, "Work", "server/")
			f.heldLease(t, a)
			err := f.svc.RecordSession(f.ctx, a, &ticket.AttemptSession{Harness: name, SessionID: "s"})
			// A variant on another harness may refuse it, but never as an unknown one.
			if err != nil {
				assert.ErrorIs(t, err, ticket.ErrSessionHarnessMismatch)
			}
		})
	}
}
