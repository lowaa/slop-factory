package ticket_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/authn/authntest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// muxFor is the ticket routes signed in as user of tenant.
func (r *mergedRig) muxFor(user, tenant uuid.UUID) http.Handler {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.UserMiddleware(api, func(_ context.Context, _ string) (*authn.User, error) {
		return &authn.User{ID: user, TenantID: tenant}, nil
	}, authntest.Guard(), slog.New(slog.DiscardHandler)))
	ticket.RegisterRoutes(api, ticket.NewHandler(r.svc, slog.DiscardHandler))
	return mux
}

func TestCloseAsMerged_Wire_FactsAndNotice(t *testing.T) {
	t.Parallel()
	r := newMergedRig(t)
	tk, _ := r.inReviewApproved(t, headOne)
	r.ignored(t)

	rec := send(t, r.muxFor(r.human, r.tenant), http.MethodGet, fmt.Sprintf("/api/tickets/%d/merge_facts", tk.ID), "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got ticket.MergeFactsBody
	decode(t, rec.Body.Bytes(), &got)
	assert.Equal(t, mergedPRLink, got.PRURL)
	assert.True(t, got.MergedIntoBase)
	assert.Equal(t, "main", got.Base)
	assert.True(t, got.HeadApproved)
	assert.Equal(t, "passed", got.Checks)
	assert.True(t, got.MergeOnBase)
	assert.Empty(t, got.Confirmations)
	require.NotNil(t, got.Ignored)
	assert.Equal(t, "merge-bot", got.Ignored.MergerLogin)
}

func TestCloseAsMerged_Wire_ConfirmFlow(t *testing.T) {
	t.Parallel()
	r := newMergedRig(t)
	tk, _ := r.inReviewApproved(t, headOne)
	r.host.ChecksOf[closeMergedPR] = codehost.NewChecks(headOne, "main", []codehost.Check{{Name: "ci", State: codehost.CheckFailure}})
	mux := r.muxFor(r.human, r.tenant)
	path := fmt.Sprintf("/api/tickets/%d/close_as_merged", tk.ID)

	rec := send(t, mux, http.MethodPost, path, `{}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var asked ticket.CloseTicketAsMergedResultBody
	decode(t, rec.Body.Bytes(), &asked)
	assert.True(t, asked.NeedsConfirmation)
	assert.False(t, asked.Closed)
	require.Len(t, asked.Confirmations, 1)
	assert.Contains(t, asked.Confirmations[0], "did not pass")
	assert.Equal(t, ticket.StatusReview, r.status(t, tk.ID))

	rec = send(t, mux, http.MethodPost, path, `{"confirm": true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var done ticket.CloseTicketAsMergedResultBody
	decode(t, rec.Body.Bytes(), &done)
	assert.True(t, done.Closed)
	require.NotNil(t, done.Ticket)
	assert.Equal(t, ticket.StatusDone, done.Ticket.Status)
	assert.Equal(t, ticket.StatusDone, r.status(t, tk.ID))
}

func TestCloseAsMerged_Wire_Refusals(t *testing.T) {
	t.Parallel()
	r := newMergedRig(t)
	tk, _ := r.inReviewApproved(t, headOne)
	member := dbtest.NewUser(t, r.database)
	otherTenant := dbtest.NewTenant(t, r.database)
	r.owners[[2]uuid.UUID{r.human, otherTenant}] = true
	facts := fmt.Sprintf("/api/tickets/%d/merge_facts", tk.ID)
	closeIt := fmt.Sprintf("/api/tickets/%d/close_as_merged", tk.ID)

	for _, path := range []string{facts, closeIt} {
		method := http.MethodGet
		if path == closeIt {
			method = http.MethodPost
		}
		rec := send(t, r.muxFor(member, r.tenant), method, path, `{"confirm": true}`)
		assert.Equal(t, http.StatusForbidden, rec.Code, "a member: %s", rec.Body.String())
		rec = send(t, r.muxFor(r.human, otherTenant), method, path, `{"confirm": true}`)
		assert.Equal(t, http.StatusNotFound, rec.Code, "another tenant: %s", rec.Body.String())
	}
	assert.Empty(t, r.host.StateReads(), "a refused request must not reach the code host")
	assert.Equal(t, ticket.StatusReview, r.status(t, tk.ID))

	r.host.StateOf[closeMergedPR] = &codehost.ChangeState{Merged: false, Base: "main", HeadSHA: headOne}
	rec := send(t, r.muxFor(r.human, r.tenant), http.MethodPost, closeIt, `{"confirm": true}`)
	assert.Equal(t, http.StatusConflict, rec.Code, "not merged: %s", rec.Body.String())
	assert.Equal(t, ticket.StatusReview, r.status(t, tk.ID))
}

func decode(t *testing.T, body []byte, into any) {
	t.Helper()
	require.NoError(t, json.Unmarshal(body, into))
}
