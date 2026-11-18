package ticket_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestMarkTicketPostReviewed_Wire(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	a := f.agent(t, "builder-1")
	open := f.ticket(t, "Not merged yet")
	done := f.merge(t, a, f.ticket(t, "Self-merged").ID)
	path := fmt.Sprintf("/api/tickets/%d/post_review", done.ID)

	rec := send(t, mux, http.MethodPost, fmt.Sprintf("/api/tickets/%d/post_review", open.ID), `{}`)
	assert.Equal(t, http.StatusConflict, rec.Code, "nothing merged to look at: %s", rec.Body.String())

	rec = send(t, mux, http.MethodPost, path, `{"comment": "Looks fine."}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeTicket(t, rec.Body.Bytes())
	assert.Equal(t, ticket.StatusDone, body.Status)
	assert.NotNil(t, body.PostReviewedAt)
	require.NotNil(t, body.PostReviewedByUserID)
	assert.Equal(t, f.human.String(), *body.PostReviewedByUserID, "reviewed by the signed-in user")

	comments, err := f.svc.ListComments(f.ctx, done.ID)
	require.NoError(t, err)
	assert.Equal(t, "Looks fine.", comments[len(comments)-1].Body)

	rec = send(t, mux, http.MethodPost, path, `{}`)
	assert.Equal(t, http.StatusConflict, rec.Code, "a second click: %s", rec.Body.String())

	rec = send(t, mux, http.MethodPost, "/api/tickets/9999/post_review", `{}`)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// The comment is optional, down to the body itself.
func TestMarkTicketPostReviewed_NoBody(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	a := f.agent(t, "builder-1")
	done := f.merge(t, a, f.ticket(t, "Self-merged").ID)

	rec := send(t, mux, http.MethodPost, fmt.Sprintf("/api/tickets/%d/post_review", done.ID), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotNil(t, decodeTicket(t, rec.Body.Bytes()).PostReviewedAt)
}

func TestListTickets_AwaitingPostReview_Wire(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	a := f.agent(t, "builder-1")
	f.ticket(t, "Still open")
	reviewed := f.merge(t, a, f.ticket(t, "Looked at").ID)
	_, err := f.svc.MarkPostReviewed(f.ctx, reviewed.ID, f.human, "")
	require.NoError(t, err)
	waiting := f.merge(t, a, f.ticket(t, "Not looked at").ID)

	rec := send(t, mux, http.MethodGet, "/api/tickets?awaiting_post_review=true", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		Tickets []ticket.TicketBody `json:"tickets"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Tickets, 1)
	assert.Equal(t, waiting.ID, got.Tickets[0].ID)
	require.NotNil(t, got.Tickets[0].PRURL, "each with the PR to look at")
	assert.Equal(t, mergedPR, *got.Tickets[0].PRURL)

	rec = send(t, mux, http.MethodGet, "/api/tickets", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Len(t, got.Tickets, 3, "unfiltered without the parameter")
}

func TestListTickets_AwaitingPostReview_PagedWire(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	a := f.agent(t, "builder-1")
	first := f.merge(t, a, f.ticket(t, "Fix widget").ID)
	second := f.merge(t, a, f.ticket(t, "Fix widget again").ID)
	f.merge(t, a, f.ticket(t, "Unrelated").ID)
	f.setClosedAt(t, first.ID, "2026-03-01T10:00:00Z")
	f.setClosedAt(t, second.ID, "2026-03-02T10:00:00Z")

	type listed struct {
		Tickets    []ticket.TicketBody `json:"tickets"`
		NextCursor string              `json:"next_cursor"`
	}
	get := func(query string) listed {
		rec := send(t, mux, http.MethodGet, "/api/tickets?awaiting_post_review=true&"+query, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var got listed
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		return got
	}

	one := get("q=widget&limit=1")
	require.Len(t, one.Tickets, 1)
	assert.Equal(t, second.ID, one.Tickets[0].ID)
	require.NotEmpty(t, one.NextCursor)
	two := get("q=widget&limit=1&cursor=" + one.NextCursor)
	require.Len(t, two.Tickets, 1)
	assert.Equal(t, first.ID, two.Tickets[0].ID)
	assert.Empty(t, two.NextCursor)

	ranged := get("closed_from=2026-03-02&closed_to=2026-03-02&limit=10")
	require.Len(t, ranged.Tickets, 1, "closed_to is inclusive of that whole day")
	assert.Equal(t, second.ID, ranged.Tickets[0].ID)

	for _, q := range []string{"cursor=garbage", "limit=1000", "limit=0", "limit=-1", "closed_from=yesterday"} {
		rec := send(t, mux, http.MethodGet, "/api/tickets?awaiting_post_review=true&"+q, "")
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "%s: %s", q, rec.Body.String())
	}
	// A cursor is bound to the list it came from.
	rec := send(t, mux, http.MethodGet, "/api/tickets?limit=5&cursor="+one.NextCursor, "")
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "%s", rec.Body.String())
}
