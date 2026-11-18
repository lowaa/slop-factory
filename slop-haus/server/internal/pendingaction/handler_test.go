package pendingaction_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

const (
	board        = "https://slop.example"
	userToken    = "user-token"
	strangerTok  = "stranger-token"
	consoleToken = "console-agent-token"
	workerToken  = "worker-agent-token"
	sessionID    = "session-a"
)

func csrfFor(session string) string { return "csrf-of-" + session }

// wire is the approvals routes over HTTP, behind the auth middlewares cmd/api
// installs, in the same order, against the real stores. A person of the
// fixture's tenant, a person of another, and two agents of the fixture's
// tenant -- a console and a worker -- each have a bearer token.
type wire struct {
	*decider
	mux      *http.ServeMux
	worker   uuid.UUID
	stranger *authn.User
	controls *fakeControls
	drafts   *fakeDraftApplier
}

func newWire(t *testing.T) *wire {
	t.Helper()
	d := newDecider(t)
	w := &wire{decider: d, worker: d.newAgent(t, "worker", false), controls: &fakeControls{}, drafts: &fakeDraftApplier{}}
	w.stranger = &authn.User{ID: dbtest.NewUser(t, d.database), TenantID: dbtest.NewTenant(t, d.database), SessionID: "session-b"}
	d.approver.SessionID = sessionID

	agents := map[string]*agent.Agent{}
	for token, id := range map[string]uuid.UUID{consoleToken: d.console, workerToken: w.worker} {
		a, err := d.agents.Get(d.ctx, id)
		require.NoError(t, err)
		agents[token] = a
	}
	guard, err := authn.NewBrowserGuard(board, func(session, token string) bool {
		return session != "" && token == csrfFor(session)
	}, audit.New(slog.DiscardHandler, nil))
	require.NoError(t, err)

	w.mux = http.NewServeMux()
	api := humago.New(w.mux, huma.DefaultConfig("test", "0.0.0"))
	logger := slog.New(slog.DiscardHandler)
	api.UseMiddleware(authn.DefaultDeny(api, logger))
	api.UseMiddleware(authn.HumaMiddleware(api, func(_ context.Context, token string) (authn.Identity, error) {
		if a := agents[token]; a != nil {
			return a, nil
		}
		return nil, errors.New("unknown agent token")
	}, audit.New(slog.DiscardHandler, nil), logger))
	api.UseMiddleware(authn.UserMiddleware(api, func(_ context.Context, token string) (*authn.User, error) {
		switch token {
		case userToken:
			return d.approver, nil
		case strangerTok:
			return w.stranger, nil
		}
		return nil, errors.New("unknown user token")
	}, guard, logger))
	pendingaction.RegisterRoutes(api, pendingaction.NewHandler(d.svc, slog.DiscardHandler).WithStewardControls(w.controls).WithDraftApplier(w.drafts))
	return w
}

type request struct {
	method, path, body string
	bearer             string
	cookie             string
	origin             string
	csrf               string
}

func (w *wire) do(t *testing.T, r request) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
	req.Header.Set("Content-Type", "application/json")
	if r.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+r.bearer)
	}
	if r.cookie != "" {
		req.AddCookie(&http.Cookie{Name: authn.SessionCookie, Value: r.cookie})
	}
	if r.origin != "" {
		req.Header.Set("Origin", r.origin)
	}
	if r.csrf != "" {
		req.Header.Set(authn.CSRFHeader, r.csrf)
	}
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, req)
	return rec
}

func (w *wire) as(t *testing.T, token, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return w.do(t, request{method: method, path: path, body: body, bearer: token})
}

// route is every route, against a real pending proposal so that a request
// which got past auth would succeed rather than fail for some other reason.
type route struct{ method, path, body string }

func (w *wire) routes(id uuid.UUID) []route {
	p := "/api/proposals"
	return []route{
		{http.MethodGet, p, ""},
		{http.MethodGet, p + "/" + id.String(), ""},
		{http.MethodPost, p + "/" + id.String() + "/approve", ""},
		{http.MethodPost, p + "/" + id.String() + "/reject", `{"reason": "no"}`},
		{http.MethodPost, p + "/" + id.String() + "/steward/veto", ""},
		{http.MethodPost, p + "/" + id.String() + "/steward/undo", ""},
		{http.MethodPost, p + "/approve_all", `{"kind": "ready"}`},
		{http.MethodPost, p + "/reject_all", `{}`},
	}
}

func (w *wire) assertUntouched(t *testing.T, p *pendingaction.Action) {
	t.Helper()
	assert.Equal(t, pendingaction.StatePending, w.stateOf(t, p).State)
	assert.Equal(t, ticket.StatusPreparing, w.ticketStatus(t, *p.TicketID))
	assert.Empty(t, w.comments(t, *p.TicketID))
}

// No agent can decide a proposal, the console included: its token, which
// the agent routes accept, is refused on every route, and nothing changed.
func TestNoAgentTokenReachesAnyProposalRoute(t *testing.T) {
	w := newWire(t)
	p := w.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})

	for _, token := range []string{consoleToken, workerToken, "", "garbage"} {
		for _, r := range w.routes(p.ID) {
			rec := w.as(t, token, r.method, r.path, r.body)
			assert.Equal(t, http.StatusUnauthorized, rec.Code, "%q on %s %s: %s", token, r.method, r.path, rec.Body.String())
		}
	}
	w.assertUntouched(t, p)
}

func TestAPersonDecidesOnProposalsThroughTheRoutes(t *testing.T) {
	w := newWire(t)
	p := w.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
	rejected := w.proposeIn(t, ticket.StatusDiscussion, pendingaction.KindDiscussionAnswer, pendingaction.Payload{Body: "x"})

	rec := w.as(t, userToken, http.MethodGet, "/api/proposals?state=pending", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list pendingaction.ProposalListBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Proposals, 2)
	for _, got := range list.Proposals {
		assert.Equal(t, "console-one", got.AgentName)
		assert.NotEmpty(t, got.ActionLine)
	}

	rec = w.as(t, userToken, http.MethodPost, "/api/proposals/"+p.ID.String()+"/approve", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got pendingaction.ProposalBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "approved", got.State)
	assert.Equal(t, ticket.StatusOpen, w.ticketStatus(t, *p.TicketID))
	change, who := w.lastChange(t, *p.TicketID)
	require.NotNil(t, who)
	assert.Equal(t, w.approver.ID, *who)
	assert.Equal(t, "proposal:"+p.ID.String(), change.Cause)

	rec = w.as(t, userToken, http.MethodPost, "/api/proposals/"+rejected.ID.String()+"/reject", `{"reason": "ask me first"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "rejected: ask me first", w.stateOf(t, rejected).Outcome)
	assert.Equal(t, ticket.StatusDiscussion, w.ticketStatus(t, *rejected.TicketID))
}

// A session cookie is how the browser signs in, and so what a forged
// cross-site request rides on: its writes are checked.
func TestACookieSessionMustCarryTheCSRFTokenAndTheBoardsOrigin(t *testing.T) {
	w := newWire(t)
	p := w.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
	approve := "/api/proposals/" + p.ID.String() + "/approve"
	cookie := userToken

	for name, r := range map[string]request{
		"no CSRF token":      {origin: board},
		"wrong CSRF token":   {origin: board, csrf: "forged"},
		"another site":       {origin: "https://evil.example", csrf: csrfFor(sessionID)},
		"no origin at all":   {csrf: csrfFor(sessionID)},
		"another's CSRF key": {origin: board, csrf: csrfFor("session-b")},
	} {
		r.method, r.path, r.cookie = http.MethodPost, approve, cookie
		rec := w.do(t, r)
		assert.Equal(t, http.StatusForbidden, rec.Code, "%s: %s", name, rec.Body.String())
	}
	w.assertUntouched(t, p)

	rec := w.do(t, request{method: http.MethodPost, path: approve, cookie: cookie, origin: board, csrf: csrfFor(sessionID)})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, ticket.StatusOpen, w.ticketStatus(t, *p.TicketID))
}

// Approving applies what was stored. Nothing a request carries can alter it.
func TestApprovingAppliesOnlyTheStoredPayload(t *testing.T) {
	w := newWire(t)
	p := w.proposeIn(t, ticket.StatusDiscussion, pendingaction.KindDiscussionAnswer, pendingaction.Payload{Body: "the stored answer"})

	rec := w.as(t, userToken, http.MethodPost, "/api/proposals/"+p.ID.String()+"/approve?body=injected",
		`{"body": "injected", "payload": {"body": "injected"}, "kind": "ready", "ticket_id": 99999}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	comments := w.comments(t, *p.TicketID)
	require.Len(t, comments, 1)
	assert.Contains(t, comments[0].Body, "the stored answer")
	assert.NotContains(t, comments[0].Body, "injected")
}

func TestReplayedAndConcurrentApprovalsOverHTTPApplyOnce(t *testing.T) {
	w := newWire(t)
	p := w.proposeIn(t, ticket.StatusDiscussion, pendingaction.KindDiscussionAnswer, pendingaction.Payload{Body: "yes"})
	path := "/api/proposals/" + p.ID.String() + "/approve"

	const racers = 6
	codes := make([]int, racers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes[i] = w.as(t, userToken, http.MethodPost, path, "").Code
		}()
	}
	close(start)
	wg.Wait()

	ok, conflict := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		}
	}
	assert.Equal(t, 1, ok, "codes: %v", codes)
	assert.Equal(t, racers-1, conflict, "codes: %v", codes)
	assert.Len(t, w.comments(t, *p.TicketID), 1)

	assert.Equal(t, http.StatusConflict, w.as(t, userToken, http.MethodPost, path, "").Code, "a later replay too")
}

func TestAnotherTenantsPersonSeesNoProposalAndChangesNone(t *testing.T) {
	w := newWire(t)
	p := w.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
	id := p.ID.String()

	for _, r := range []route{
		{http.MethodGet, "/api/proposals/" + id, ""},
		{http.MethodPost, "/api/proposals/" + id + "/approve", ""},
		{http.MethodPost, "/api/proposals/" + id + "/reject", `{}`},
	} {
		rec := w.as(t, strangerTok, r.method, r.path, r.body)
		assert.Equal(t, http.StatusNotFound, rec.Code, "%s %s: %s", r.method, r.path, rec.Body.String())
	}
	rec := w.as(t, strangerTok, http.MethodGet, "/api/proposals", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var list pendingaction.ProposalListBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	assert.Empty(t, list.Proposals)
	assert.Equal(t, http.StatusOK, w.as(t, strangerTok, http.MethodPost, "/api/proposals/reject_all", `{}`).Code)
	w.assertUntouched(t, p)
}

func TestARefusedApprovalIsA409AndTheProposalIsSettled(t *testing.T) {
	w := newWire(t)
	p := w.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
	w.exec(t, `UPDATE tickets SET revision = revision + 1 WHERE id = $1`, p.TicketID)

	rec := w.as(t, userToken, http.MethodPost, "/api/proposals/"+p.ID.String()+"/approve", "")
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, pendingaction.StateStale, w.stateOf(t, p).State)
	assert.Equal(t, ticket.StatusPreparing, w.ticketStatus(t, *p.TicketID))
}

func TestApproveAllOverHTTPTakesOnlyReadyAndAnswers(t *testing.T) {
	w := newWire(t)
	ready := w.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
	cancel := w.proposeIn(t, ticket.StatusOpen, pendingaction.KindStatus, pendingaction.Payload{Status: ticket.StatusCancelled, Body: "x"})

	for _, kind := range []string{"status", "review_decision", "bogus"} {
		rec := w.as(t, userToken, http.MethodPost, "/api/proposals/approve_all", `{"kind": "`+kind+`"}`)
		assert.GreaterOrEqual(t, rec.Code, 400, "%s: %s", kind, rec.Body.String())
		assert.Less(t, rec.Code, 500, kind)
	}
	assert.Equal(t, pendingaction.StatePending, w.stateOf(t, cancel).State)

	rec := w.as(t, userToken, http.MethodPost, "/api/proposals/approve_all", `{"kind": "ready"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var batch pendingaction.BatchBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &batch))
	require.Len(t, batch.Results, 1)
	assert.Equal(t, ready.ID.String(), batch.Results[0].ID)
	assert.Equal(t, "approved", batch.Results[0].State)
	assert.Equal(t, ticket.StatusOpen, w.ticketStatus(t, *ready.TicketID))
	assert.Equal(t, pendingaction.StatePending, w.stateOf(t, cancel).State, "a cancellation is never batched")
}

func TestListOverHTTPTakesTheTicketFilterParameters(t *testing.T) {
	w := newWire(t)
	a := w.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
	b := w.proposeIn(t, ticket.StatusDiscussion, pendingaction.KindDiscussionAnswer, pendingaction.Payload{Body: "x"})
	w.exec(t, `UPDATE tickets SET priority = 'urgent' WHERE id = $1`, b.TicketID)

	listed := func(query string) []string {
		rec := w.as(t, userToken, http.MethodGet, "/api/proposals?"+query, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var list pendingaction.ProposalListBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
		ids := []string{}
		for _, p := range list.Proposals {
			ids = append(ids, p.ID)
		}
		return ids
	}
	assert.ElementsMatch(t, []string{a.ID.String(), b.ID.String()}, listed(""))
	assert.Equal(t, []string{b.ID.String()}, listed("priority=urgent"))
	assert.Equal(t, []string{a.ID.String()}, listed("status=preparing&status=blocked"))

	rec := w.as(t, userToken, http.MethodGet, "/api/proposals?created_from=not-a-date", "")
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	for _, bad := range []string{"status=nonsense", "priority=nonsense", "q=" + strings.Repeat("x", ticket.MaxSearchTextLen+1)} {
		rec = w.as(t, userToken, http.MethodGet, "/api/proposals?"+bad, "")
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "%.40s: %s", bad, rec.Body.String())
	}
}

func TestBatchesOverHTTPDecideOnlyTheNamedProposals(t *testing.T) {
	w := newWire(t)
	named := w.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
	other := w.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})

	rec := w.as(t, userToken, http.MethodPost, "/api/proposals/approve_all", `{"kind": "ready", "ids": ["not-an-id"]}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())

	rec = w.as(t, userToken, http.MethodPost, "/api/proposals/approve_all", `{"kind": "ready", "ids": ["`+named.ID.String()+`"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, pendingaction.StateApproved, w.stateOf(t, named).State)
	assert.Equal(t, pendingaction.StatePending, w.stateOf(t, other).State)

	rec = w.as(t, userToken, http.MethodPost, "/api/proposals/reject_all", `{"ids": []}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, pendingaction.StatePending, w.stateOf(t, other).State, "naming none rejects none")

	rec = w.as(t, userToken, http.MethodPost, "/api/proposals/reject_all", `{"ids": ["`+other.ID.String()+`"], "reason": "no"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, pendingaction.StateRejected, w.stateOf(t, other).State)
}

// The person who decided a proposal may later delete their account; the
// proposal stays decided, by "a deleted user", and the id is gone.
func TestAProposalDecidedByADeletedPersonIsFlaggedAndNamesNoId(t *testing.T) {
	w := newWire(t)
	p := w.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
	rec := w.as(t, userToken, http.MethodPost, "/api/proposals/"+p.ID.String()+"/approve", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var decided pendingaction.ProposalBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decided))
	require.NotNil(t, decided.DecidedByUserID)
	assert.Equal(t, w.approver.ID.String(), *decided.DecidedByUserID)
	assert.False(t, decided.DecidedByDeleted)

	_, err := w.database.Global().Exec(context.Background(), `DELETE FROM global_users WHERE id = $1`, w.approver.ID)
	require.NoError(t, err)

	rec = w.as(t, userToken, http.MethodGet, "/api/proposals?state=approved", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), w.approver.ID.String())
	var list pendingaction.ProposalListBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Proposals, 1)
	assert.Nil(t, list.Proposals[0].DecidedByUserID)
	assert.True(t, list.Proposals[0].DecidedByDeleted)
}
