package ticket_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// testTenantID is the fixed tenant newTestMux's fake
// verifier hands back for every request, whatever credential (or none) it
// carries. These handler tests are about wire behavior -- routing, JSON
// shape, status codes -- not about who can sign in, so one fixed user is
// enough; authn.UserMiddleware itself is exercised for real in
// internal/authn.
var (
	testTenantID = uuid.New()
)

var errNoCredential = errors.New("no credential in this test")

// The permissive verifier in newTestMux authenticates every request, which
// is what makes the rest of this file's tests convenient to write -- but it
// would also let a route that lost its authn.RequireUser() metadata pass
// unnoticed. This drives every ticket/comment route with a verifier that
// refuses everyone, so each one's 401 has to come from the middleware
// actually being wired to it, not from the fixture being generous.
func TestTicketRoutes_RequireUser_NoCredentialIs401(t *testing.T) {
	t.Parallel()
	mux := newUnauthenticatedTestMux()

	cases := []struct{ method, path, body string }{
		{http.MethodGet, "/api/tickets", ""},
		{http.MethodPost, "/api/tickets", `{"title": "x"}`},
		{http.MethodGet, "/api/tickets/1", ""},
		{http.MethodPatch, "/api/tickets/1", `{"status": "open"}`},
		{http.MethodGet, "/api/tickets/1/revisions", ""},
		{http.MethodPost, "/api/tickets/1/revisions", `{"title": "y"}`},
		{http.MethodGet, "/api/tickets/1/status_history", ""},
		{http.MethodPost, "/api/tickets/1/ready", ""},
		{http.MethodPost, "/api/tickets/1/attempts", `{}`},
		{http.MethodPost, "/api/tickets/1/approve", ""},
		{http.MethodPost, "/api/tickets/1/request_changes", `{"comment": "x"}`},
		{http.MethodGet, "/api/tickets/1/comments", ""},
		{http.MethodPost, "/api/tickets/1/comments", `{"body": "x"}`},
		{http.MethodGet, "/api/tickets/1/contexts", ""},
		// RegisterRoutes mounts RegisterGraphRoutes's operations too; these
		// pin that the same middleware actually reaches them; a route
		// registered outside RegisterRoutes entirely would fall through
		// every case above without this.
		{http.MethodGet, "/api/tickets/graph", ""},
		{http.MethodGet, "/api/tickets/1/blockers", ""},
		{http.MethodGet, "/api/tickets/1/blocker_candidates", ""},
		{http.MethodPost, "/api/tickets/1/blockers", `{"blocker_id": 2}`},
		{http.MethodDelete, "/api/tickets/1/blockers/2", ""},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := send(t, mux, tc.method, tc.path, tc.body)
			assert.Equal(t, http.StatusUnauthorized, rec.Code, "body: %s", rec.Body.String())
		})
	}
}

func TestCreateTicket_Returns201WithTicket(t *testing.T) {
	t.Parallel()
	stack := newTestStack(t)
	mux := stack.mux

	req := httptest.NewRequest(http.MethodPost, "/api/tickets",
		strings.NewReader(`{"title": "Fix the thing", "paths": [{"path": "server/"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	var body ticket.TicketBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.NotZero(t, body.ID)
	assert.Equal(t, "Fix the thing", body.Title)
	assert.Equal(t, model.PriorityNormal, body.Priority, "Service.Create defaults an empty priority to normal")
	assert.Equal(t, ticket.StatusOpen, body.Status)
	require.Len(t, body.Paths, 1)
	assert.Equal(t, "server/", body.Paths[0].Path)
	assert.Nil(t, body.CreatedByAgentID, "board-filed ticket")
	require.NotNil(t, body.CreatedByUserID, "the ticket is attributed to the signed-in user")
	assert.Equal(t, stack.user.String(), *body.CreatedByUserID)
}

// created_by_agent_id used to be a body field a caller could set to
// attribute a ticket to any agent id it liked -- an impersonation hole,
// since this route authenticates a user, not an agent. It no longer exists
// on the wire at all, so supplying it does nothing: the creator is always
// the signed-in user.
func TestCreateTicket_BodySuppliedCreatorFieldDoesNothing(t *testing.T) {
	t.Parallel()
	mux := newTestStack(t).mux

	impersonated := uuid.New().String()
	rec := send(t, mux, http.MethodPost, "/api/tickets",
		fmt.Sprintf(`{"title": "Fix the thing", "created_by_agent_id": %q}`, impersonated))

	// huma's schema doesn't merely ignore a field the struct no longer has
	// -- it rejects the whole request. Stronger than "does nothing": there
	// is no way to even send this field and have it silently dropped.
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "created_by_agent_id")
}

func TestCreateTicket_EmptyTitle_Returns422(t *testing.T) {
	t.Parallel()
	mux := newTestStack(t).mux

	req := httptest.NewRequest(http.MethodPost, "/api/tickets", strings.NewReader(`{"title": ""}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
}

// A scope is filed with just a path -- `source` is the server's to assign.
// Sharing one DTO between request and response made it a required input
// field, so the obvious `{"path": "..."}` was rejected 422, and naming a
// source let a client claim a brand-new ticket's scope was already
// "extended".
func TestCreateTicket_PathScopeNeedsNoSource(t *testing.T) {
	t.Parallel()
	mux := newTestStack(t).mux

	req := httptest.NewRequest(http.MethodPost, "/api/tickets",
		strings.NewReader(`{"title": "Scoped", "paths": [{"path": "server/internal/lease/"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	var body ticket.TicketBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, []ticket.PathBody{{Path: "server/internal/lease/", Source: "filed"}}, body.Paths)
}

func TestGetTicket_NotFound_Returns404(t *testing.T) {
	t.Parallel()
	mux := newTestStack(t).mux

	req := httptest.NewRequest(http.MethodGet, "/api/tickets/999", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())
}

func TestGetTicket_ReturnsCreatedTicket(t *testing.T) {
	t.Parallel()
	mux := newTestStack(t).mux

	createReq := httptest.NewRequest(http.MethodPost, "/api/tickets", strings.NewReader(`{"title": "Fix the thing"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	mux.ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusCreated, createRec.Code)

	var created ticket.TicketBody
	require.NoError(t, json.Unmarshal(createRec.Body.Bytes(), &created))

	getReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/tickets/%d", created.ID), nil)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, getReq)
	require.Equal(t, http.StatusOK, getRec.Code, "body: %s", getRec.Body.String())

	var fetched ticket.TicketBody
	require.NoError(t, json.Unmarshal(getRec.Body.Bytes(), &fetched))
	assert.Equal(t, created.ID, fetched.ID)
	assert.Equal(t, created.Title, fetched.Title)
}

// The collection endpoints return an object wrapping the array, not a bare
// top-level array -- asserting the wrapper explicitly means a change back to
// a bare array fails here rather than silently at the frontend.
func TestListTickets_ReturnsWrappedArray(t *testing.T) {
	t.Parallel()
	stack := newTestStack(t)
	mux := stack.mux
	stack.create(t, &ticket.CreateParams{Title: "one", Priority: model.PriorityNormal})
	stack.create(t, &ticket.CreateParams{Title: "two", Priority: model.PriorityNormal})

	req := httptest.NewRequest(http.MethodGet, "/api/tickets", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var body struct {
		Tickets []ticket.TicketBody `json:"tickets"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Len(t, body.Tickets, 2)
}

func TestListTickets_PassesTheTextFilterDown(t *testing.T) {
	t.Parallel()
	stack := newTestStack(t)
	mux := stack.mux

	rec := send(t, mux, http.MethodGet, "/api/tickets?status=open&q="+url.QueryEscape("board_view 100%"), "")

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.NotNil(t, stack.store.listedWith())
	assert.Equal(t, "board_view 100%", stack.store.listedWith().Text)
	assert.Equal(t, []ticket.Status{ticket.StatusOpen}, stack.store.listedWith().Statuses)
}

func TestListTickets_OverlongTextFilter_Returns422(t *testing.T) {
	t.Parallel()
	mux := newTestStack(t).mux

	rec := send(t, mux, http.MethodGet, "/api/tickets?q="+strings.Repeat("a", ticket.MaxSearchTextLen+1), "")

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
}

// author_agent_id used to be a body field a caller could set to attribute a
// comment to any agent id it liked -- an impersonation hole, since this
// route authenticates a user, not an agent. It no longer exists on the
// wire at all, so supplying it does nothing: the author is always the
// signed-in user.
func TestCreateTicketComment_BodySuppliedAuthorFieldDoesNothing(t *testing.T) {
	t.Parallel()
	stack := newTestStack(t)
	mux := stack.mux
	created := stack.create(t, &ticket.CreateParams{Title: "one", Priority: model.PriorityNormal})

	impersonated := uuid.New().String()
	rec := send(t, mux, http.MethodPost, fmt.Sprintf("/api/tickets/%d/comments", created.ID),
		fmt.Sprintf(`{"body": "looking into it", "author_agent_id": %q}`, impersonated))

	// huma's schema doesn't merely ignore a field the struct no longer has
	// -- it rejects the whole request. Stronger than "does nothing": there
	// is no way to even send this field and have it silently dropped.
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "author_agent_id")
}

func TestCreateTicketComment_EmptyBody_Returns422(t *testing.T) {
	t.Parallel()
	stack := newTestStack(t)
	mux := stack.mux
	created := stack.create(t, &ticket.CreateParams{Title: "one", Priority: model.PriorityNormal})

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/tickets/%d/comments", created.ID),
		strings.NewReader(`{"body": ""}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
}

func TestListTicketComments_ReturnsWrappedArray(t *testing.T) {
	t.Parallel()
	stack := newTestStack(t)
	mux := stack.mux
	created := stack.create(t, &ticket.CreateParams{Title: "one", Priority: model.PriorityNormal})
	_, err := stack.svc.AddComment(stack.ctx, created.ID, ticket.UserAuthor(stack.user), "first")
	require.NoError(t, err)
	_, err = stack.svc.AddComment(stack.ctx, created.ID, ticket.UserAuthor(stack.user), "second")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/tickets/%d/comments", created.ID), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var body struct {
		Comments []ticket.CommentBody `json:"comments"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Len(t, body.Comments, 2)
}

// A ticket on the wire says which moves its status allows, on get and on
// list alike, so the board needs no request per ticket to know them.
func TestTicketBody_CarriesTheMovesItsStatusAllows(t *testing.T) {
	t.Parallel()
	stack := newTestStack(t)
	mux := stack.mux
	preparing := stack.create(t, &ticket.CreateParams{Title: "draft", Status: ticket.StatusPreparing})
	open := stack.create(t, &ticket.CreateParams{Title: "queued", Status: ticket.StatusOpen})

	type moves struct {
		ID           int64    `json:"id"`
		AllowedMoves []string `json:"allowed_moves"`
		CanReady     bool     `json:"can_ready"`
	}
	want := func(id int64, status ticket.Status) moves {
		return moves{ID: id, AllowedMoves: wireStatuses(ticket.AllowedMoves(status)), CanReady: ticket.CanReady(status)}
	}

	for _, tc := range []struct {
		id     int64
		status ticket.Status
	}{{preparing.ID, ticket.StatusPreparing}, {open.ID, ticket.StatusOpen}} {
		rec := send(t, mux, http.MethodGet, fmt.Sprintf("/api/tickets/%d", tc.id), "")
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		var got moves
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		assert.Equal(t, want(tc.id, tc.status), got)
	}

	rec := send(t, mux, http.MethodGet, "/api/tickets", "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var listed struct {
		Tickets []moves `json:"tickets"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listed))
	assert.ElementsMatch(t, []moves{want(preparing.ID, ticket.StatusPreparing), want(open.ID, ticket.StatusOpen)},
		listed.Tickets)
	// Not vacuous: the two statuses differ in what they allow.
	assert.NotEqual(t, want(0, ticket.StatusPreparing), want(0, ticket.StatusOpen))
}

func wireStatuses(statuses []ticket.Status) []string {
	out := make([]string, len(statuses))
	for i, s := range statuses {
		out[i] = string(s)
	}
	return out
}

func send(t *testing.T, mux http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// The checkbox on the new-ticket form: filed, read back, and editable, so
// the human can send a ticket back for another planning pass.
//
// Against PostgresStore: an edit is a transition, which the fake does not
// implement.
func TestTicket_NeedsDiscussionRoundTrips(t *testing.T) {
	t.Parallel()
	mux := newFixture(t).boardMux()

	rec := send(t, mux, http.MethodPost, "/api/tickets", `{"title": "Rough idea", "needs_discussion": true}`)
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	var created ticket.TicketBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	assert.True(t, created.NeedsDiscussion)

	rec = send(t, mux, http.MethodPatch, fmt.Sprintf("/api/tickets/%d", created.ID), `{"needs_discussion": false}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var updated ticket.TicketBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &updated))
	assert.False(t, updated.NeedsDiscussion)
}

func TestListAgentContexts_ServesTheHandoffs(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	agentID := f.agent(t, "builder-1")
	created := f.ticket(t, "Loose")
	require.Equal(t, created.ID, f.claim(t, agentID).ID)
	f.discuss(t, agentID)

	rec := send(t, mux, http.MethodGet, fmt.Sprintf("/api/tickets/%d/contexts", created.ID), "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var body struct {
		Contexts []ticket.AgentContextBody `json:"contexts"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Contexts, 1)
	assert.Equal(t, "Handler half-done on branch t-1.", body.Contexts[0].Body)
	assert.Equal(t, agentID.String(), body.Contexts[0].AgentID)
}
