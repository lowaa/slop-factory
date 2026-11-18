package ticket_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// PR review over the wire, against the real store: a fake here would only be
// testing itself.

func TestCreateTicket_RequiresApprovalRoundTrips(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()

	rec := send(t, mux, http.MethodPost, "/api/tickets", `{"title": "Gated", "requires_approval": true}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	created := decodeTicket(t, rec.Body.Bytes())
	assert.True(t, created.RequiresApproval)

	rec = send(t, mux, http.MethodPost, "/api/tickets", `{"title": "Not asked"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.False(t, decodeTicket(t, rec.Body.Bytes()).RequiresApproval, "off unless asked for")

	rec = send(t, mux, http.MethodPatch, fmt.Sprintf("/api/tickets/%d", created.ID), `{"requires_approval": false}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.False(t, decodeTicket(t, rec.Body.Bytes()).RequiresApproval)
}

func TestApproveTicket_Wire(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	a := f.agent(t, "builder-1")
	gated := f.gated(t, "Gated", "server/")
	f.claim(t, a)
	f.review(t, a)
	path := fmt.Sprintf("/api/tickets/%d/approve", gated.ID)

	rec := send(t, mux, http.MethodPost, path, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeTicket(t, rec.Body.Bytes())
	assert.Equal(t, ticket.StatusOpen, body.Status)
	assert.NotNil(t, body.ApprovedAt)
	require.NotNil(t, body.ApprovedByUserID)
	assert.Equal(t, f.human.String(), *body.ApprovedByUserID, "approved by the signed-in user")
	assert.Nil(t, body.ChangesRequestedAt)
	require.NotNil(t, body.ResumeAgentID)
	assert.Equal(t, a.String(), *body.ResumeAgentID)

	rec = send(t, mux, http.MethodPost, path, "")
	assert.Equal(t, http.StatusConflict, rec.Code, "a second click: %s", rec.Body.String())

	rec = send(t, mux, http.MethodPost, "/api/tickets/9999/approve", "")
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

func TestRequestTicketChanges_Wire(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	a := f.agent(t, "builder-1")
	gated := f.gated(t, "Gated", "server/")
	f.claim(t, a)
	f.review(t, a)
	path := fmt.Sprintf("/api/tickets/%d/request_changes", gated.ID)

	rec := send(t, mux, http.MethodPost, path, `{"comment": ""}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "a request with nothing to address: %s", rec.Body.String())

	rec = send(t, mux, http.MethodPost, path, `{"comment": "Split the handler."}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeTicket(t, rec.Body.Bytes())
	assert.Equal(t, ticket.StatusOpen, body.Status)
	assert.NotNil(t, body.ChangesRequestedAt)
	assert.Nil(t, body.ApprovedAt)

	rec = send(t, mux, http.MethodPost, path, `{"comment": "again"}`)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
}

func TestUpdateTicket_LeavingReviewForTheQueueIsAConflict(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	a := f.agent(t, "builder-1")
	gated := f.gated(t, "Gated", "server/")
	f.claim(t, a)
	f.review(t, a)
	path := fmt.Sprintf("/api/tickets/%d", gated.ID)

	rec := send(t, mux, http.MethodPatch, path, `{"status": "open"}`)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "approved", "the refusal says what to do instead")

	rec = send(t, mux, http.MethodPatch, path, `{"status": "done"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, ticket.StatusDone, decodeTicket(t, rec.Body.Bytes()).Status)
}

// -- The agent's side, over the claim routes --

func claimRoute(agentID uuid.UUID, path, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	authorize(req, agentID)
	return req
}

// An agent's way out of a held ticket is the MCP tools. These REST routes
// skipped their rules (promotion, the landing queue, the secret scan), so
// they must stay gone.
func TestRemovedAgentClaimRoutes_AreNotServed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.agentMux()
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "gated", "server/")
	f.claim(t, a)

	for path, body := range map[string]string{
		"/api/claims/done":         `{"summary": "merged", "pr_url": "https://example.test/pull/1"}`,
		"/api/claims/review":       `{"pr_url": "https://example.test/pull/1", "handoff": "h", "reason": "r"}`,
		"/api/claims/scope":        `{"paths": [{"path": "web/"}]}`,
		"/api/claims/scope/narrow": `{"remove": ["server/"]}`,
	} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, claimRoute(a, path, body))
			assert.Contains(t, []int{http.StatusNotFound, http.StatusMethodNotAllowed}, rec.Code, rec.Body.String())
		})
	}
	assert.Equal(t, ticket.StatusInProgress, f.status(t, created.ID), "the ticket is still held")
}
