package discussionanswer_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/authn/authntest"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionanswer"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionlabel"
)

// fakeAnswers returns what the test gives it and records what it was asked.
type fakeAnswers struct {
	draft    *discussionanswer.Draft
	decision *discussionanswer.Decision
	err      error
	calls    []string
	round    *discussionanswer.Round
	text     string
	actor    string
}

func (f *fakeAnswers) note(call string, r *discussionanswer.Round, text, actor string) {
	f.calls, f.round, f.text, f.actor = append(f.calls, call), r, text, actor
}

func (f *fakeAnswers) Draft(_ context.Context, r *discussionanswer.Round) (*discussionanswer.Draft, error) {
	f.note("draft", r, "", "")
	return f.draft, f.err
}

func (f *fakeAnswers) AssessNow(_ context.Context, r *discussionanswer.Round) (*discussionlabel.Record, error) {
	f.note("assess", r, "", "")
	return nil, f.err
}

func (f *fakeAnswers) Approve(_ context.Context, r *discussionanswer.Round, actor string) (*discussionanswer.Decision, error) {
	f.note("approve", r, "", actor)
	return f.decision, f.err
}

func (f *fakeAnswers) Edit(_ context.Context, r *discussionanswer.Round, text, actor string) (*discussionanswer.Decision, error) {
	f.note("edit", r, text, actor)
	return f.decision, f.err
}

func (f *fakeAnswers) Reject(_ context.Context, r *discussionanswer.Round, reason, actor string) (*discussionanswer.Decision, error) {
	f.note("reject", r, reason, actor)
	return f.decision, f.err
}

// fakeApplying returns what the test gives it and records the vetoes.
type fakeApplying struct {
	badge    *discussionanswer.ApplyBadge
	decision *discussionanswer.Decision
	err      error
	vetoes   []string
}

func (f *fakeApplying) DraftBadge(context.Context, *discussionlabel.Record) (*discussionanswer.ApplyBadge, error) {
	return f.badge, f.err
}

func (f *fakeApplying) Veto(_ context.Context, r *discussionanswer.Round, actor string) (*discussionanswer.Decision, error) {
	f.vetoes = append(f.vetoes, fmt.Sprintf("%d/%d/%d by %s", r.TicketID, r.Attempt, r.Round, actor))
	return f.decision, f.err
}

// fakeRoles knows who owns which tenant; err fails every lookup.
type fakeRoles struct {
	owners map[[2]uuid.UUID]bool
	err    error
}

func (f fakeRoles) IsTenantOwner(_ context.Context, id, tenant uuid.UUID) (bool, error) {
	return f.owners[[2]uuid.UUID{id, tenant}], f.err
}

type handlerRig struct {
	answers  *fakeAnswers
	applying *fakeApplying
	mux      *http.ServeMux
	owner    *authn.User
	roles    *fakeRoles
	member   *authn.User
}

const roundPath = "/api/tickets/7/attempts/2/discussions/3/steward"

func newHandlerRig(t *testing.T) *handlerRig {
	t.Helper()
	r := &handlerRig{answers: &fakeAnswers{}, applying: &fakeApplying{badge: &discussionanswer.ApplyBadge{}}, mux: http.NewServeMux(),
		owner: &authn.User{ID: uuid.New(), TenantID: uuid.New()}, member: &authn.User{ID: uuid.New(), TenantID: uuid.New()}}
	api := humago.New(r.mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.UserMiddleware(api, func(_ context.Context, token string) (*authn.User, error) {
		switch token {
		case "owner":
			return r.owner, nil
		case "member":
			return r.member, nil
		}
		return nil, errors.New("unknown token")
	}, authntest.Guard(), slog.New(slog.DiscardHandler)))
	r.roles = &fakeRoles{owners: map[[2]uuid.UUID]bool{{r.owner.ID, r.owner.TenantID}: true}}
	discussionanswer.RegisterRoutes(api, discussionanswer.NewHandler(r.answers, r.roles, slog.DiscardHandler).WithApplying(r.applying))
	return r
}

func (r *handlerRig) do(method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	return rec
}

func TestRoutesAreOwnerOnly(t *testing.T) {
	r := newHandlerRig(t)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, roundPath, ""},
		{http.MethodPost, roundPath + "/assess", ""},
		{http.MethodPost, roundPath + "/approve", ""},
		{http.MethodPost, roundPath + "/edit", `{"text":"x"}`},
		{http.MethodPost, roundPath + "/reject", `{"reason":"x"}`},
		{http.MethodPost, roundPath + "/veto", ""},
	} {
		assert.Equal(t, http.StatusUnauthorized, r.do(c.method, c.path, "", c.body).Code, c.path)
		assert.Equal(t, http.StatusForbidden, r.do(c.method, c.path, "member", c.body).Code, c.path)
	}
	assert.Empty(t, r.answers.calls, "the service is not asked for a non-owner")
	assert.Empty(t, r.applying.vetoes)
}

func TestARoleLookupFailureRefusesTheCaller(t *testing.T) {
	r := newHandlerRig(t)
	r.roles.err = errors.New("db down")

	assert.Equal(t, http.StatusInternalServerError, r.do(http.MethodPost, roundPath+"/approve", "owner", "").Code)
	assert.Empty(t, r.answers.calls)
}

func TestGetDraft(t *testing.T) {
	r := newHandlerRig(t)
	r.answers.draft = &discussionanswer.Draft{
		Label: &discussionlabel.Record{Label: discussionlabel.LabelRoutine, Category: "merge_continue"}, Body: "carry on",
		Decision: &discussionanswer.Decision{Action: discussionanswer.ActionApprove, FinalBody: "carry on"},
	}

	rec := r.do(http.MethodGet, roundPath, "owner", "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got discussionanswer.DiscussionDraftBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "carry on", got.Body)
	assert.Equal(t, "routine", got.Label.Label)
	require.NotNil(t, got.Decision)
	assert.Equal(t, "approve", got.Decision.Action)
	assert.Equal(t, &discussionanswer.Round{TicketID: 7, Attempt: 2, Round: 3}, r.answers.round)
}

func TestDecisionsPassTheOwnerAndTheirInput(t *testing.T) {
	r := newHandlerRig(t)
	r.answers.decision = &discussionanswer.Decision{Action: discussionanswer.ActionEdit, FinalBody: "mine"}

	rec := r.do(http.MethodPost, roundPath+"/edit", "owner", `{"text":"mine"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got discussionanswer.DiscussionDecisionBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, discussionanswer.DiscussionDecisionBody{Action: "edit", FinalBody: "mine"}, got)
	assert.Equal(t, "mine", r.answers.text)
	assert.Equal(t, r.owner.ID.String(), r.answers.actor)

	rec = r.do(http.MethodPost, roundPath+"/reject", "owner", `{"reason":"not now"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"edit", "reject"}, r.answers.calls)
	assert.Equal(t, "not now", r.answers.text)

	assert.Equal(t, http.StatusUnprocessableEntity, r.do(http.MethodPost, roundPath+"/edit", "owner", `{"text":""}`).Code)
}

func TestApproveAndAssessReturnWhatTheServiceDecided(t *testing.T) {
	r := newHandlerRig(t)
	r.answers.decision = &discussionanswer.Decision{Action: discussionanswer.ActionApprove, OriginalBody: "carry on", FinalBody: "carry on"}
	r.answers.draft = &discussionanswer.Draft{Label: &discussionlabel.Record{Label: discussionlabel.LabelRoutine}}

	rec := r.do(http.MethodPost, roundPath+"/approve", "owner", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got discussionanswer.DiscussionDecisionBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "approve", got.Action)
	assert.Equal(t, "carry on", got.FinalBody)
	assert.Equal(t, r.owner.ID.String(), r.answers.actor)

	rec = r.do(http.MethodPost, roundPath+"/assess", "owner", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"approve", "assess", "draft"}, r.answers.calls, "the fresh draft is read after assessing")
}

func TestErrorsMapLikeTheApprovalsRoutes(t *testing.T) {
	for err, want := range map[error]int{
		discussionanswer.ErrStale:                            http.StatusConflict,
		discussionanswer.ErrAlreadyDecided:                   http.StatusConflict,
		discussionanswer.ErrChecksFailed:                     http.StatusConflict,
		discussionanswer.ErrNoDraft:                          http.StatusConflict,
		discussionanswer.ErrOff:                              http.StatusConflict,
		discussionanswer.ErrNotRoutine:                       http.StatusUnprocessableEntity,
		discussionanswer.ErrEmpty:                            http.StatusUnprocessableEntity,
		discussionlabel.ErrNotFound:                          http.StatusNotFound,
		discussionlabel.ErrNotAssessable:                     http.StatusConflict,
		discussionanswer.ErrUnavailable:                      http.StatusServiceUnavailable,
		fmt.Errorf("wrapped: %w", discussionanswer.ErrStale): http.StatusConflict,
		errors.New("boom"):                                   http.StatusInternalServerError,
	} {
		r := newHandlerRig(t)
		r.answers.err = err
		for _, path := range []string{"/approve", "/assess"} {
			assert.Equal(t, want, r.do(http.MethodPost, roundPath+path, "owner", "").Code, "%v on %s", err, path)
		}
	}
}

func TestGetDraftCarriesTheStewardApplyBadge(t *testing.T) {
	r := newHandlerRig(t)
	r.answers.draft = &discussionanswer.Draft{Label: &discussionlabel.Record{Label: discussionlabel.LabelRoutine}}
	at := epoch.Add(time.Hour)
	r.applying.badge = &discussionanswer.ApplyBadge{State: "scheduled", At: &at, CanVeto: true}

	rec := r.do(http.MethodGet, roundPath, "owner", "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got discussionanswer.DiscussionDraftBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.NotNil(t, got.StewardApply)
	assert.Equal(t, "scheduled", got.StewardApply.State)
	assert.True(t, got.StewardApply.CanVeto)
	assert.False(t, got.StewardApply.CanUndo)
	assert.True(t, at.Equal(*got.StewardApply.At))

	r.applying.badge = &discussionanswer.ApplyBadge{}
	rec = r.do(http.MethodGet, roundPath, "owner", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "steward_apply", "no badge when the steward is not applying it")
}

func TestVetoPassesTheOwnerAndMapsRefusals(t *testing.T) {
	r := newHandlerRig(t)
	r.applying.decision = &discussionanswer.Decision{Action: discussionanswer.ActionReject, Reason: "vetoed"}

	rec := r.do(http.MethodPost, roundPath+"/veto", "owner", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"7/2/3 by " + r.owner.ID.String()}, r.applying.vetoes)

	for _, err := range []error{discussionanswer.ErrNotScheduled, discussionanswer.ErrWindowClosed} {
		r.applying.err = err
		assert.Equal(t, http.StatusConflict, r.do(http.MethodPost, roundPath+"/veto", "owner", "").Code, err.Error())
	}
}
