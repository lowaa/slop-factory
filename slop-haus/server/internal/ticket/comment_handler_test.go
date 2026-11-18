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

// The board's comment route against the real store: a person's comment is a
// transition (it may answer a discussion), and transitions are tested
// against PostgresStore, not a fake of it.

func TestCreateTicketComment_ReturnsComment(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "one")

	rec := send(t, f.boardMux(), http.MethodPost, fmt.Sprintf("/api/tickets/%d/comments", created.ID),
		`{"body": "looking into it"}`)

	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	var body ticket.CommentBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "looking into it", body.Body)
	assert.Nil(t, body.AuthorAgentID)
	require.NotNil(t, body.AuthorUserID, "the comment is attributed to the signed-in user")
	assert.Equal(t, f.human.String(), *body.AuthorUserID)
}

// A comment from the board carries no author, and that is what makes it
// the human's answer: it puts a ticket waiting on one back in the queue.
func TestTicketComment_FromTheBoardAnswersADiscussion(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Loose", "server/")
	f.claim(t, a)
	f.discuss(t, a)

	rec := send(t, f.boardMux(), http.MethodPost, fmt.Sprintf("/api/tickets/%d/comments", created.ID), `{"body": "Go flat."}`)
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	assert.Equal(t, ticket.StatusOpen, f.status(t, created.ID))
}
