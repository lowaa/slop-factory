package ticket_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestGetTicket_ListsScopeOverlapsOnPreparingAndOpenTickets(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	live := f.ticket(t, "Live", "app/src/panel/")
	preparing := f.preparing(t, "Shaping", "app/src/")
	open := f.ticket(t, "Open", "app/src/")
	f.forceStatus(t, live.ID, ticket.StatusInProgress)

	for _, tk := range []*ticket.Ticket{preparing, open} {
		rec := send(t, mux, http.MethodGet, fmt.Sprintf("/api/tickets/%d", tk.ID), "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		got := decodeTicket(t, rec.Body.Bytes()).ScopeOverlaps
		require.NotEmpty(t, got, tk.Title)
		assert.Equal(t, ticket.ScopeOverlapBody{
			TicketID: live.ID, Title: "Live", Status: ticket.StatusInProgress, Path: "app/src/", Other: "app/src/panel/",
		}, got[0])
	}
}

func TestGetTicket_NoScopeOverlapsOutsidePreparingAndOpen(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	f.ticket(t, "Other", "app/src/")
	held := f.ticket(t, "Held", "app/src/x.go")
	f.forceStatus(t, held.ID, ticket.StatusInProgress)

	rec := send(t, mux, http.MethodGet, fmt.Sprintf("/api/tickets/%d", held.ID), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Empty(t, decodeTicket(t, rec.Body.Bytes()).ScopeOverlaps)
}

func TestCreateAndEditTicket_ReportScopeOverlapsWithoutRefusing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	existing := f.ticket(t, "Existing", "app/src/")

	rec := send(t, mux, http.MethodPost, "/api/tickets",
		`{"title": "New", "status": "preparing", "paths": [{"path": "app/src/a.go"}]}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	created := decodeTicket(t, rec.Body.Bytes())
	require.Len(t, created.ScopeOverlaps, 1)
	assert.Equal(t, existing.ID, created.ScopeOverlaps[0].TicketID)

	rec = send(t, mux, http.MethodPatch, fmt.Sprintf("/api/tickets/%d", created.ID), `{"paths": [{"path": "other/"}]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Empty(t, decodeTicket(t, rec.Body.Bytes()).ScopeOverlaps, "narrowed away from the overlap")

	rec = send(t, mux, http.MethodPatch, fmt.Sprintf("/api/tickets/%d", created.ID), `{"paths": [{"path": "app/"}]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Len(t, decodeTicket(t, rec.Body.Bytes()).ScopeOverlaps, 1)
}

// The overlaps are feedback: a read or an edit that went through must not
// fail because the feedback could not be worked out.
func TestScopeOverlaps_AFailedComputationDoesNotFailTheTicketRead(t *testing.T) {
	t.Parallel()
	stack := newTestStack(t)
	mux := stack.mux

	req := httptest.NewRequest(http.MethodPost, "/api/tickets", strings.NewReader(`{"title": "Filed"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	filed := decodeTicket(t, rec.Body.Bytes())

	// The real list read is the one the overlaps are computed from; it is
	// the one thing the database can't be made to fail on cue.
	stack.store.failLists(errors.New("list is down"))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/tickets/%d", filed.ID), nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Empty(t, decodeTicket(t, rec.Body.Bytes()).ScopeOverlaps)
}
