package pendingaction_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// fakeDraftApplier answers what the test set and records the call. Whether a
// draft is stale or its caller an owner is tested where that lives.
type fakeDraftApplier struct {
	err   error
	calls []uuid.UUID
	by    uuid.UUID
}

func (f *fakeDraftApplier) ApplyDraft(_ context.Context, owner *authn.User, id uuid.UUID) error {
	f.calls, f.by = append(f.calls, id), owner.ID
	return f.err
}

type draftsFake struct {
	byID map[uuid.UUID]*pendingaction.DraftView
	err  error
}

func (f *draftsFake) Drafts(context.Context, []uuid.UUID) (map[uuid.UUID]*pendingaction.DraftView, error) {
	return f.byID, f.err
}

func TestTheStewardsDraftIsOnTheProposalOnlyWhenThereIsOne(t *testing.T) {
	w := newWire(t)
	drafted := w.proposeIn(t, ticket.StatusDiscussion, pendingaction.KindDiscussionAnswer, pendingaction.Payload{Body: "yes"})
	plain := w.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
	w.svc.SetDrafts(&draftsFake{byID: map[uuid.UUID]*pendingaction.DraftView{
		drafted.ID: {Category: "discussion_pick", Body: "yes", Reason: "the brief says so", TicketRevision: 4, HeadSHA: "abc",
			Evidence: []pendingaction.AssessmentEvidence{{Check: "ticket_rev_unchanged", OK: true}}},
	}})

	got := listProposals(t, w)
	d := got[drafted.ID.String()].Draft
	require.NotNil(t, d)
	assert.Equal(t, "yes", d.Body)
	assert.Equal(t, 4, d.Revision)
	assert.Equal(t, "abc", d.HeadSHA)
	assert.False(t, d.WouldSendBack)
	assert.Equal(t, []pendingaction.StewardEvidenceBody{{Check: "ticket_rev_unchanged", OK: true}}, d.Evidence)
	assert.Nil(t, got[plain.ID.String()].Draft)
}

func TestAnUnreadableDraftLeavesTheProposalsAsTheyWere(t *testing.T) {
	w := newWire(t)
	p := w.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
	w.svc.SetDrafts(&draftsFake{err: errors.New("db down")})

	got := listProposals(t, w)
	require.Contains(t, got, p.ID.String())
	assert.Nil(t, got[p.ID.String()].Draft)
}

func TestApplyStewardDraftPassesTheSignedInPersonAndMapsRefusals(t *testing.T) {
	w := newWire(t)
	p := w.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
	path := "/api/proposals/" + p.ID.String() + "/steward/apply"

	rec := w.as(t, userToken, http.MethodPost, path, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"id":"`+p.ID.String()+`"`)
	assert.Equal(t, w.approver.ID, w.drafts.by)
	assert.Equal(t, []uuid.UUID{p.ID}, w.drafts.calls)

	for err, want := range map[error]int{
		pendingaction.ErrNotApprover:     http.StatusForbidden,
		pendingaction.ErrNotFound:        http.StatusNotFound,
		pendingaction.ErrNotPending:      http.StatusConflict,
		pendingaction.ErrRefused:         http.StatusConflict,
		pendingaction.ErrInvalid:         http.StatusUnprocessableEntity,
		errors.New("the database broke"): http.StatusInternalServerError,
	} {
		w.drafts.err = err
		assert.Equal(t, want, w.as(t, userToken, http.MethodPost, path, "").Code, "%v", err)
	}
}
