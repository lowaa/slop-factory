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

func TestBoardReads_NameTheAttemptOnEachCommentAndHandoff(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, _, a2 := f.running(t)
	mux := f.boardMux()
	f.personComments(t, id, "For everyone.")
	f.personCommentsOn(t, id, 2, "For two.")
	_, err := f.svc.Discuss(f.ctx, a2, "Two asks.", "handoff two")
	require.NoError(t, err)

	t.Run("comments: addressed lists its attempt, unaddressed lists null", func(t *testing.T) {
		rec := send(t, mux, http.MethodGet, fmt.Sprintf("/api/tickets/%d/comments", id), "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var got struct {
			Comments []ticket.CommentBody `json:"comments"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		byBody := map[string]*int64{}
		for _, c := range got.Comments {
			byBody[c.Body] = c.AttemptNumber
		}
		assert.Nil(t, byBody["For everyone."])
		require.NotNil(t, byBody["Two asks."], "the agent's own question is the attempt's too")
		assert.EqualValues(t, 2, *byBody["Two asks."])
		require.NotNil(t, byBody["For two."])
		assert.EqualValues(t, 2, *byBody["For two."])
		assert.Contains(t, rec.Body.String(), `"attempt_number":null`, "present on the wire even when null")
	})
	t.Run("a created comment carries it", func(t *testing.T) {
		code, body := f.postCommentBody(t, mux, id, "?attempt=2", "New for two.")
		require.Equal(t, http.StatusCreated, code, body)
		var got ticket.CommentBody
		require.NoError(t, json.Unmarshal([]byte(body), &got))
		require.NotNil(t, got.AttemptNumber)
		assert.EqualValues(t, 2, *got.AttemptNumber)
	})
	t.Run("handoffs: the writing agent's attempt", func(t *testing.T) {
		rec := send(t, mux, http.MethodGet, fmt.Sprintf("/api/tickets/%d/contexts", id), "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var got struct {
			Contexts []ticket.AgentContextBody `json:"contexts"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.Len(t, got.Contexts, 1)
		assert.Equal(t, "handoff two", got.Contexts[0].Body)
		assert.EqualValues(t, 2, got.Contexts[0].AttemptNumber)
	})
}
