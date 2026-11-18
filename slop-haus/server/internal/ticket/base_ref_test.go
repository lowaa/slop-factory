package ticket_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/lowaa/slop-factory/go-auth/authn/authntest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

const commitX = "cccccccccccccccccccccccccccccccccccccccc"

func (r *pinRig) createWithRef(t *testing.T, ref string, mutate ...func(*ticket.CreateParams)) (*ticket.Ticket, error) {
	t.Helper()
	params := &ticket.CreateParams{
		Title: "Based", BaseRef: ref,
		Paths: []model.Path{{Path: "a/"}},
	}
	for _, m := range mutate {
		m(params)
	}
	return r.svc.Create(r.ctx, params)
}

func (r *pinRig) knownRefs() {
	r.host.RefOf = map[codehosttest.RefKey]codehost.ResolvedRef{
		{Repo: acmeApp, Ref: "cccccccc"}: {SHA: commitX, Kind: codehost.RefCommit},
		{Repo: acmeApp, Ref: commitX}:    {SHA: commitX, Kind: codehost.RefCommit},
		{Repo: acmeApp, Ref: "v1.2.0"}:   {SHA: commitX, Kind: codehost.RefTag},
	}
	r.host.BranchAt = map[codehosttest.BranchKey]string{{Repo: acmeApp, Branch: "feature"}: headTwo}
}

func TestCreate_ResolvesEachKindOfBaseRefAndStoresTheCommit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		ref    string
		tagged bool
	}{{commitX, true}, {"cccccccc", true}, {"v1.2.0", false}} {
		t.Run(tc.ref, func(t *testing.T) {
			r := newPinRig(t)
			r.knownRefs()

			tk, err := r.createWithRef(t, tc.ref)

			require.NoError(t, err)
			require.NotNil(t, tk.BaseCommit)
			assert.Equal(t, commitX, *tk.BaseCommit)
			require.NotNil(t, tk.BaseRef)
			assert.Equal(t, tc.ref, *tk.BaseRef)
			assert.Empty(t, tk.Warnings)
			if !tc.tagged {
				assert.Empty(t, r.host.TagCreates(), "a tag is already a name that keeps the commit")
				return
			}
			require.Len(t, r.host.TagCreates(), 1)
			call := r.host.TagCreates()[0]
			assert.Equal(t, fmt.Sprintf("slop-haus/base/%d", tk.ID), call.Tag)
			assert.Equal(t, commitX, call.SHA)
		})
	}
}

func TestCreate_NoBaseRefKeepsToday(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	tk, err := r.createWithRef(t, "")
	require.NoError(t, err)
	assert.Nil(t, tk.BaseRef)
	assert.Nil(t, tk.BaseCommit)
	assert.Empty(t, r.host.TagCreates())
}

func TestCreate_RefusesWhatCannotBeABase(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		ref    string
		setup  func(*pinRig)
		target error
		text   string
	}{
		"unknown":  {ref: "nope", target: ticket.ErrBaseRefInvalid, text: "not a commit or tag"},
		"a branch": {ref: "feature", target: ticket.ErrBaseRefInvalid, text: "is a branch"},
		"unplaced": {ref: "v1.2.0", setup: func(r *pinRig) { r.ws.repos[0].Remote = "gitlab.example/acme/app" }, target: ticket.ErrBaseRefInvalid, text: "exactly one repository"},
		"two repos": {ref: "v1.2.0", setup: func(r *pinRig) {
			r.ws.repos = append(r.ws.repos, scope.WorkspaceRepo{Path: "b", Remote: "github.com/acme/other"})
		}, target: ticket.ErrScopesSpanRepos},
		"host down": {ref: "v1.2.0", setup: func(r *pinRig) { r.host.RefErr = codehost.ErrUnreachable }, target: ticket.ErrBaseRefUnverifiable},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r := newPinRig(t)
			r.knownRefs()
			if tc.setup != nil {
				tc.setup(r)
			}

			_, err := r.createWithRef(t, tc.ref)

			require.Error(t, err)
			assert.True(t, errors.Is(err, tc.target), err.Error())
			assert.Contains(t, err.Error(), tc.text)
			var n int
			require.NoError(t, r.database.QueryRow(r.ctx, `SELECT count(*) FROM tickets`).Scan(&n))
			assert.Zero(t, n, "nothing was filed")
		})
	}
}

func TestCreate_AHostThatRefusesTheTagStillFilesTheTicketWithAWarning(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	r.knownRefs()
	r.host.TagErr = codehost.ErrRejected

	tk, err := r.createWithRef(t, commitX)

	require.NoError(t, err)
	assert.Equal(t, commitX, *tk.BaseCommit)
	require.Len(t, tk.Warnings, 1)
	assert.Contains(t, tk.Warnings[0], "slop-haus/base/")
}

func TestCreate_SkipBaseTagLeavesTheTagOff(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	r.knownRefs()
	_, err := r.createWithRef(t, commitX, func(p *ticket.CreateParams) { p.SkipBaseTag = true })
	require.NoError(t, err)
	assert.Empty(t, r.host.TagCreates())
}

func TestClaim_KeepsTheFiledBaseAndDoesNotReadTheHead(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	r.knownRefs()
	tk, err := r.createWithRef(t, "v1.2.0")
	require.NoError(t, err)

	granted := r.claim(t, r.agent(t, "worker"))

	require.Equal(t, tk.ID, granted.ID)
	require.NotNil(t, granted.BaseCommit)
	assert.Equal(t, commitX, *granted.BaseCommit, "the default branch's head did not replace the filed base")
	assert.Equal(t, commitX, *r.base(t, tk.ID))
	assert.Empty(t, r.host.HeadReads())
}

func TestUpdate_BaseRefIsEditableUntilTheFirstClaim(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	r.knownRefs()
	tk, err := r.createWithRef(t, "")
	require.NoError(t, err)
	set := func(ref string) (*ticket.Ticket, error) {
		return r.svc.Update(r.ctx, tk.ID, &ticket.UpdateParams{BaseRef: &ref})
	}

	got, err := set("v1.2.0")
	require.NoError(t, err)
	assert.Equal(t, commitX, *got.BaseCommit)
	assert.Equal(t, "v1.2.0", *got.BaseRef)

	_, err = set("feature")
	require.ErrorIs(t, err, ticket.ErrBaseRefInvalid)
	assert.Equal(t, commitX, *r.base(t, tk.ID), "a refused edit changes nothing")

	got, err = set("")
	require.NoError(t, err)
	assert.Nil(t, got.BaseRef)
	assert.Nil(t, got.BaseCommit)

	_, err = set("v1.2.0")
	require.NoError(t, err)
	r.claim(t, r.agent(t, "worker"))

	_, err = set("")
	require.ErrorIs(t, err, ticket.ErrBaseRefLocked)
	assert.Equal(t, commitX, *r.base(t, tk.ID), "the claimed ticket's base stayed")
}

func TestUpdate_ACommitBaseIsTaggedAndATagFailureIsAWarning(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	r.knownRefs()
	tk, err := r.createWithRef(t, "")
	require.NoError(t, err)
	ref := commitX

	got, err := r.svc.Update(r.ctx, tk.ID, &ticket.UpdateParams{BaseRef: &ref})

	require.NoError(t, err)
	assert.Empty(t, got.Warnings)
	require.Len(t, r.host.TagCreates(), 1)
	assert.Equal(t, fmt.Sprintf("slop-haus/base/%d", tk.ID), r.host.TagCreates()[0].Tag)

	r.host.TagErr = codehost.ErrRejected
	got, err = r.svc.Update(r.ctx, tk.ID, &ticket.UpdateParams{BaseRef: &ref})
	require.NoError(t, err)
	assert.Len(t, got.Warnings, 1)
}

func TestUpdate_BaseRefIsResolvedAgainstThePathsTheSameEditSets(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	r.knownRefs()
	tk, err := r.createWithRef(t, "")
	require.NoError(t, err)
	ref := "v1.2.0"

	_, err = r.svc.Update(r.ctx, tk.ID, &ticket.UpdateParams{BaseRef: &ref, Paths: []model.Path{{Path: "b/"}}})

	require.NoError(t, err)
	assert.Equal(t, commitX, *r.base(t, tk.ID))
}

// rest drives the create and edit routes over the rig's service.
func (r *pinRig) rest(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.UserMiddleware(api, func(context.Context, string) (*authn.User, error) {
		return &authn.User{ID: r.human, TenantID: r.tenant}, nil
	}, authntest.Guard(), slog.New(slog.DiscardHandler)))
	ticket.RegisterRoutes(api, ticket.NewHandler(r.svc, slog.DiscardHandler))
	return mux
}

func TestREST_BaseRefAndBaseTagReachTheServiceAndTheWarningsComeBack(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	r.knownRefs()
	mux := r.rest(t)

	rec := send(t, mux, http.MethodPost, "/api/tickets", `{"title": "T", "paths": [{"path": "a/"}], "base_ref": "v1.2.0"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var body ticket.TicketBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotNil(t, body.BaseRef)
	assert.Equal(t, "v1.2.0", *body.BaseRef)
	assert.Equal(t, commitX, *body.BaseCommit)

	rec = send(t, mux, http.MethodPost, "/api/tickets", fmt.Sprintf(`{"title": "T", "paths": [{"path": "a/"}], "base_ref": %q, "base_tag": false}`, commitX))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Empty(t, r.host.TagCreates(), "base_tag false leaves the tag off")

	r.host.TagErr = codehost.ErrRejected
	rec = send(t, mux, http.MethodPost, "/api/tickets", fmt.Sprintf(`{"title": "T", "paths": [{"path": "a/"}], "base_ref": %q}`, commitX))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Len(t, body.Warnings, 1)

	rec = send(t, mux, http.MethodPost, "/api/tickets", `{"title": "T", "paths": [{"path": "a/"}], "base_ref": "feature"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "is a branch")
}

func TestREST_PatchBaseRefIsEditableThenLocked(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	r.knownRefs()
	mux := r.rest(t)
	tk, err := r.createWithRef(t, "")
	require.NoError(t, err)
	path := fmt.Sprintf("/api/tickets/%d", tk.ID)

	rec := send(t, mux, http.MethodPatch, path, `{"base_ref": "v1.2.0"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body ticket.TicketBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "v1.2.0", *body.BaseRef)

	r.claim(t, r.agent(t, "worker"))
	rec = send(t, mux, http.MethodPatch, path, `{"base_ref": ""}`)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
}

// openIt opens a preparing ticket the way the board's Ready does.
func (r *pinRig) openIt(t *testing.T, id int64) *ticket.Ticket {
	t.Helper()
	got, err := r.svc.MarkReadyMany(r.ctx, []int64{id})
	require.NoError(t, err)
	require.Len(t, got, 1)
	return &got[0]
}

func (r *pinRig) preparingWithBase(t *testing.T, ref string) *ticket.Ticket {
	t.Helper()
	tk, err := r.createWithRef(t, ref, func(p *ticket.CreateParams) { p.Status = ticket.StatusPreparing })
	require.NoError(t, err)
	return tk
}

func TestOpening_ATicketBehindItsBaseBranchSaysHowFar(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	r.knownRefs()
	r.host.AheadOf = map[string]int{"main": 7}
	tk := r.preparingWithBase(t, "v1.2.0")

	got := r.openIt(t, tk.ID)

	require.Len(t, got.Warnings, 1)
	assert.Contains(t, got.Warnings[0], "starts from v1.2.0, 7 commits behind main")
	assert.Equal(t, []string{commitX}, r.host.AheadSinces(), "counted from the ticket's base commit")
}

func TestOpening_TheSingleReadyAndABoardMoveSayItToo(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	r.knownRefs()
	r.host.AheadOf = map[string]int{"main": 4}

	one, err := r.svc.MarkReady(r.ctx, r.preparingWithBase(t, "v1.2.0").ID)
	require.NoError(t, err)
	require.Len(t, one.Warnings, 1)
	assert.Contains(t, one.Warnings[0], "4 commits behind main")

	moved, err := r.svc.MoveMany(r.ctx, []int64{r.preparingWithBase(t, "v1.2.0").ID}, ticket.StatusParked)
	require.NoError(t, err)
	assert.Empty(t, moved[0].Warnings, "parking is not opening")
}

func TestOpening_NoNoticeWhenNotBehindWithoutAnExplicitBaseOrOnOtherEdits(t *testing.T) {
	t.Parallel()
	t.Run("base branch has not moved", func(t *testing.T) {
		r := newPinRig(t)
		r.knownRefs()
		r.host.AheadOf = map[string]int{"main": 0}
		assert.Empty(t, r.openIt(t, r.preparingWithBase(t, "v1.2.0").ID).Warnings)
	})
	t.Run("no explicit base", func(t *testing.T) {
		r := newPinRig(t)
		r.host.AheadOf = map[string]int{"main": 7}
		assert.Empty(t, r.openIt(t, r.preparingWithBase(t, "").ID).Warnings)
	})
	t.Run("an edit that does not open", func(t *testing.T) {
		r := newPinRig(t)
		r.knownRefs()
		r.host.AheadOf = map[string]int{"main": 7}
		tk := r.preparingWithBase(t, "v1.2.0")
		high := model.PriorityHigh
		got, err := r.svc.Update(r.ctx, tk.ID, &ticket.UpdateParams{Priority: &high})
		require.NoError(t, err)
		assert.Empty(t, got.Warnings)
	})
	t.Run("host cannot answer", func(t *testing.T) {
		r := newPinRig(t)
		r.knownRefs()
		r.host.AheadErr = codehost.ErrUnreachable
		assert.Empty(t, r.openIt(t, r.preparingWithBase(t, "v1.2.0").ID).Warnings, "the ticket opens without a notice")
	})
}
