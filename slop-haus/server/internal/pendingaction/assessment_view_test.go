package pendingaction_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// assessmentsFake returns what the test gives it.
type assessmentsFake struct {
	byID map[uuid.UUID]*pendingaction.AssessmentView
	err  error
}

func (f *assessmentsFake) Assessments(_ context.Context, _ []uuid.UUID) (map[uuid.UUID]*pendingaction.AssessmentView, error) {
	return f.byID, f.err
}

func listProposals(t *testing.T, w *wire) map[string]pendingaction.ProposalBody {
	t.Helper()
	rec := w.as(t, userToken, http.MethodGet, "/api/proposals?state=pending", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list pendingaction.ProposalListBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	out := map[string]pendingaction.ProposalBody{}
	for _, p := range list.Proposals {
		out[p.ID] = p
	}
	return out
}

func TestTheStewardsAssessmentIsOnTheProposalOnlyWhenThereIsOne(t *testing.T) {
	w := newWire(t)
	assessed := w.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
	plain := w.proposeIn(t, ticket.StatusDiscussion, pendingaction.KindDiscussionAnswer, pendingaction.Payload{Body: "x"})
	w.svc.SetAssessments(&assessmentsFake{byID: map[uuid.UUID]*pendingaction.AssessmentView{
		assessed.ID: {Category: "merge_continue", Tier: "veto", Verdict: "would_veto", Mode: "shadow", Reason: "green", VetoMinutes: 30,
			Evidence: []pendingaction.AssessmentEvidence{{Check: "ci_green_on_head", OK: true, Ref: "run-1"}}},
	}})

	got := listProposals(t, w)
	s := got[assessed.ID.String()].Steward
	require.NotNil(t, s)
	assert.Equal(t, "merge_continue", s.Category)
	assert.Equal(t, "would_veto", s.Verdict)
	assert.Equal(t, 30, s.VetoMinutes)
	assert.Equal(t, []pendingaction.StewardEvidenceBody{{Check: "ci_green_on_head", OK: true, Ref: "run-1"}}, s.Evidence)
	assert.Nil(t, got[plain.ID.String()].Steward)

	rec := w.as(t, userToken, http.MethodGet, "/api/proposals/"+plain.ID.String(), "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), `"steward"`)
}

func TestAnUnreadableAssessmentLeavesTheProposalsAsTheyWere(t *testing.T) {
	w := newWire(t)
	p := w.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
	w.svc.SetAssessments(&assessmentsFake{err: errors.New("db down")})

	got := listProposals(t, w)
	require.Contains(t, got, p.ID.String())
	assert.Nil(t, got[p.ID.String()].Steward)
}
