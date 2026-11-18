package ticket_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// A person whose account is deleted is not "nobody": every ticket response
// that named them says deleted, and none still carries their id.
func TestWire_ADeletedPersonIsFlaggedAndTheirIDIsGone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	a := f.agent(t, "builder-1")

	created, err := f.svc.Create(f.ctx, &ticket.CreateParams{Title: "Filed by a person", CreatedByUserID: &f.human})
	require.NoError(t, err)
	id := created.ID
	f.setAttempt(t, id, "approved_at = now(), approved_by_user_id = $2, review_started_at = now(), promoted_by_user_id = $2", f.human)

	rec := send(t, mux, http.MethodPost, fmt.Sprintf("/api/tickets/%d/comments", id), `{"body": "from a person"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	rec = send(t, mux, http.MethodPost, fmt.Sprintf("/api/tickets/%d/revisions", id), `{"title": "Edited by a person", "base_revision": 0}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	merged := f.merge(t, a, f.ticket(t, "Merged").ID)
	_, err = f.svc.MarkPostReviewed(f.ctx, merged.ID, f.human, "")
	require.NoError(t, err)

	_, err = f.database.Global().Exec(context.Background(), `DELETE FROM global_users WHERE id = $1`, f.human)
	require.NoError(t, err)

	get := func(path string) []byte {
		rec := send(t, mux, http.MethodGet, path, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.NotContains(t, rec.Body.String(), f.human.String(), "%s names the deleted person's id", path)
		return rec.Body.Bytes()
	}

	t.Run("ticket and its attempts", func(t *testing.T) {
		got := decodeTicket(t, get(fmt.Sprintf("/api/tickets/%d", id)))
		assert.Nil(t, got.CreatedByUserID)
		assert.True(t, got.CreatedByUserDeleted)
		require.Len(t, got.Attempts, 1)
		assert.Nil(t, got.Attempts[0].ApprovedByUserID)
		assert.True(t, got.Attempts[0].ApprovedByDeleted)
		assert.Nil(t, got.Attempts[0].PromotedByUserID)
		assert.True(t, got.Attempts[0].PromotedByDeleted)
		assert.Nil(t, got.ApprovedByUserID)
		assert.True(t, got.ApprovedByDeleted, "the ticket's own approval is the promoted attempt's")
	})

	t.Run("post review", func(t *testing.T) {
		got := decodeTicket(t, get(fmt.Sprintf("/api/tickets/%d", merged.ID)))
		assert.NotNil(t, got.PostReviewedAt)
		assert.Nil(t, got.PostReviewedByUserID)
		assert.True(t, got.PostReviewedByDeleted)
	})

	t.Run("list", func(t *testing.T) {
		var listed struct {
			Tickets []ticket.TicketBody `json:"tickets"`
		}
		require.NoError(t, json.Unmarshal(get("/api/tickets"), &listed))
		for _, tk := range listed.Tickets {
			if tk.ID == id {
				assert.True(t, tk.CreatedByUserDeleted)
				return
			}
		}
		require.Fail(t, "ticket not listed")
	})

	t.Run("comments", func(t *testing.T) {
		var listed struct {
			Comments []ticket.CommentBody `json:"comments"`
		}
		require.NoError(t, json.Unmarshal(get(fmt.Sprintf("/api/tickets/%d/comments", id)), &listed))
		require.Len(t, listed.Comments, 1)
		assert.Nil(t, listed.Comments[0].AuthorUserID)
		assert.Nil(t, listed.Comments[0].AuthorAgentID)
		assert.True(t, listed.Comments[0].AuthorUserDeleted)
	})

	t.Run("revisions", func(t *testing.T) {
		var listed struct {
			Revisions []ticket.RevisionBody `json:"revisions"`
		}
		require.NoError(t, json.Unmarshal(get(fmt.Sprintf("/api/tickets/%d/revisions", id)), &listed))
		require.Len(t, listed.Revisions, 1)
		assert.Nil(t, listed.Revisions[0].EditorUserID)
		assert.True(t, listed.Revisions[0].EditorUserDeleted)
	})
}

// Approving a ticket whose earlier approver is gone must not trip the
// schema: the new approver replaces the flag.
func TestWire_AFreshApprovalReplacesADeletedApprover(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Approved twice", "a/")
	f.setAttempt(t, created.ID, "approved_at = now(), approved_by_user_id = $2", f.human)
	_, err := f.database.Global().Exec(context.Background(), `DELETE FROM global_users WHERE id = $1`, f.human)
	require.NoError(t, err)
	assert.True(t, f.detail(t, created.ID).ApprovedByDeleted)

	other := dbtest.NewUser(t, f.database)
	f.setAttempt(t, created.ID, "approved_by_user_id = $2", other)
	got := f.detail(t, created.ID)
	assert.False(t, got.ApprovedByDeleted)
	require.NotNil(t, got.ApprovedByUserID)
	assert.Equal(t, other.String(), *got.ApprovedByUserID)
}

// Leaving done wipes the post-review. When the reviewer's account was already
// deleted, that has to clear the flag too, or the schema refuses the move.
func TestWire_ReopeningAfterADeletedPersonsPostReviewClearsIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	done := f.merge(t, a, f.ticket(t, "Self-merged").ID)
	_, err := f.svc.MarkPostReviewed(f.ctx, done.ID, f.human, "")
	require.NoError(t, err)
	_, err = f.database.Global().Exec(context.Background(), `DELETE FROM global_users WHERE id = $1`, f.human)
	require.NoError(t, err)

	reopened, err := f.setStatus(t, done.ID, ticket.StatusOpen)
	require.NoError(t, err)

	assert.Nil(t, reopened.PostReviewedAt)
	assert.False(t, reopened.PostReviewedByDeleted)
}

// The service's own review path, not a raw update: going back into review
// wipes a deleted approver's outcome, and approving again names the new person.
func TestWire_ReviewingAgainAfterADeletedApproverNamesTheNewOne(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.gated(t, "Gated", "server/")
	f.claim(t, a)
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, a))
	f.review(t, a)
	f.approve(t, created.ID)
	_, err := f.database.Global().Exec(context.Background(), `DELETE FROM global_users WHERE id = $1`, f.human)
	require.NoError(t, err)
	approved := f.get(t, created.ID)
	require.Nil(t, approved.ApprovedByUserID)
	require.True(t, approved.ApprovedByDeleted)

	other := dbtest.NewUser(t, f.database)

	f.claim(t, a)
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, a))
	f.review(t, a)
	restarted := f.get(t, created.ID)
	assert.False(t, restarted.ApprovedByDeleted, "the approval it replaces is gone with its flag")
	again, err := f.svc.Approve(f.ctx, created.ID, other)
	require.NoError(t, err)
	require.NotNil(t, again.ApprovedByUserID)
	assert.Equal(t, other, *again.ApprovedByUserID)
	assert.False(t, again.ApprovedByDeleted)
}
