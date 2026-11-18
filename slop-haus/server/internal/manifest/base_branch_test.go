package manifest_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

func TestNormaliseBaseBranch_AcceptsLegalNames(t *testing.T) {
	for _, name := range []string{"slop-haus-team-a", "team/a", "release-1.2", "a_b", " padded "} {
		got, err := manifest.Normalise([]manifest.Repo{{Path: "app", Remote: "github.com/acme/mono", BaseBranch: name}})
		require.NoError(t, err, name)
		assert.Equal(t, strings.TrimSpace(name), got[0].BaseBranch)
	}
	got, err := manifest.Normalise([]manifest.Repo{{Path: "app", Remote: "github.com/acme/mono"}})
	require.NoError(t, err)
	assert.Empty(t, got[0].BaseBranch, "no setting is the repo's default branch")
}

func TestNormaliseBaseBranch_RefusesIllegalNames(t *testing.T) {
	for _, name := range []string{
		"-lead", "a b", "a..b", "a~b", "a^b", "a:b", "a?b", "a*b", "a[b", "a\\b", "a@{b", "@", "HEAD",
		"/a", "a/", "a//b", "a.", ".a", "a/.b", "a.lock", "a/b.lock", "a\x01b", strings.Repeat("x", manifest.MaxBranch+1),
	} {
		_, err := manifest.Normalise([]manifest.Repo{{Path: "app", Remote: "github.com/acme/mono", BaseBranch: name}})
		assert.ErrorIs(t, err, manifest.ErrInvalid, "%q", name)
	}
}

func TestNormaliseBaseBranch_RefusesNamesSlopHausMakesBranchesUnder(t *testing.T) {
	for _, name := range []string{"ticket-796-a1", "ticket-1", "ticket-x/y", "slop-haus-base-ticket-796", "slop-haus-base-ticket-7/x"} {
		_, err := manifest.Normalise([]manifest.Repo{{Path: "app", Remote: "github.com/acme/mono", BaseBranch: name}})
		require.ErrorIs(t, err, manifest.ErrInvalid, name)
		assert.Contains(t, err.Error(), "collides", name)
	}
	_, err := manifest.Normalise([]manifest.Repo{{Path: "app", Remote: "github.com/acme/mono", BaseBranch: "slop-haus-team-a"}})
	assert.NoError(t, err, "the suggested name is not one slop-haus makes")
}

func TestDiffNamesABaseBranchChange(t *testing.T) {
	repo := func(b string) []manifest.Repo {
		return []manifest.Repo{{Path: "app", Remote: "github.com/a/b", BaseBranch: b}}
	}

	got := manifest.Diff(repo(""), repo("team-a"))

	require.Len(t, got, 1)
	assert.Equal(t, &manifest.BranchChange{Old: "", New: "team-a"}, got[0].BaseBranch)
	assert.Empty(t, manifest.Diff(repo("team-a"), repo("team-a")))

	added := manifest.Diff(nil, repo("team-a"))
	require.Len(t, added, 1)
	assert.Equal(t, &manifest.BranchChange{New: "team-a"}, added[0].BaseBranch)
	removed := manifest.Diff(repo("team-a"), nil)
	require.Len(t, removed, 1)
	assert.Equal(t, &manifest.BranchChange{Old: "team-a"}, removed[0].BaseBranch)
}

type branchRig struct {
	*tenantFixture
	svc    *manifest.Service
	fake   *codehosttest.Fake
	events *audittest.Counter
	log    *bytes.Buffer
	repo   codehost.Repo
}

func newBranchRig(t *testing.T, e *env) *branchRig {
	t.Helper()
	f := e.tenant()
	fake := &codehosttest.Fake{
		Domain:  "github.com",
		Remotes: map[string]codehost.Repo{"github.com/acme/mono": {Owner: "acme", Name: "mono"}},
	}
	repo := codehost.Repo{Owner: "acme", Name: "mono"}
	fake.HeadOf = map[codehost.Repo]*codehost.BranchHead{repo: {Branch: "main", SHA: strings.Repeat("a", 40)}}
	reg := codehost.NewRegistry()
	reg.Register(fake, fake)
	counter := audittest.NewCounter()
	logs := &bytes.Buffer{}
	rec := audit.New(slog.NewJSONHandler(logs, nil), counter)
	svc := manifest.NewService(manifest.NewPostgresStore(e.database), user.NewPostgresStore(e.database), nil, nil, slog.DiscardHandler,
		manifest.WithHosts(reg), manifest.WithAudit(rec))
	return &branchRig{tenantFixture: f, svc: svc, fake: fake, events: counter, log: logs, repo: repo}
}

func (r *branchRig) set(base string) (*manifest.Manifest, []manifest.BaseBranchResult) {
	r.t.Helper()
	m, err := r.svc.Set(r.ctx, r.owner, []manifest.Repo{{Path: "app", Remote: "github.com/acme/mono", BaseBranch: base}})
	require.NoError(r.t, err)
	return m, r.svc.EnsureBaseBranches(r.ctx, m)
}

func TestBaseBranch_IsMadeFromTheDefaultBranchTipOnceAndSavingAgainIsHarmless(t *testing.T) {
	r := newBranchRig(t, newEnv(t))

	_, res := r.set("slop-haus-team-a")
	require.Len(t, res, 1)
	assert.Equal(t, manifest.BranchReady, res[0].Status)
	assert.Equal(t, strings.Repeat("a", 40), r.fake.BranchAt[codehosttest.BranchKey{Repo: r.repo, Branch: "slop-haus-team-a"}])

	// The default branch moves on; the base branch must stay where it was.
	r.fake.HeadOf[r.repo].SHA = strings.Repeat("b", 40)
	_, res = r.set("slop-haus-team-a")
	assert.Equal(t, manifest.BranchReady, res[0].Status)
	assert.Equal(t, strings.Repeat("a", 40), r.fake.BranchAt[codehosttest.BranchKey{Repo: r.repo, Branch: "slop-haus-team-a"}], "never moved")
	for _, c := range r.fake.BranchCreates() {
		assert.True(t, c.Ensure, "idempotent create, not a refusing one")
	}
}

func TestBaseBranch_NoneSetMakesNothing(t *testing.T) {
	r := newBranchRig(t, newEnv(t))

	_, res := r.set("")

	assert.Empty(t, res)
	assert.Empty(t, r.fake.HeadReads())
	assert.Empty(t, r.fake.BranchCreates())
}

func TestBaseBranch_TheDefaultBranchIsAllowedWithANote(t *testing.T) {
	r := newBranchRig(t, newEnv(t))

	m, res := r.set("main")

	assert.Equal(t, "main", m.Repos[0].BaseBranch, "saved: that is today's behaviour")
	assert.Equal(t, manifest.BranchIsDefault, res[0].Status)
	assert.Contains(t, res[0].Message, "default branch")
	assert.Empty(t, r.fake.BranchCreates())
}

func TestBaseBranch_AFailureIsReportedAndTheSettingStays(t *testing.T) {
	r := newBranchRig(t, newEnv(t))
	r.fake.BranchErr = errors.New("host down")

	_, res := r.set("slop-haus-team-a")

	assert.Equal(t, manifest.BranchFailed, res[0].Status)
	assert.Contains(t, res[0].Message, "host down")
	got, err := r.svc.Get(r.ctx)
	require.NoError(t, err)
	assert.Equal(t, "slop-haus-team-a", got.Repos[0].BaseBranch)

	r.fake.BranchErr = nil
	_, res = r.set("slop-haus-team-a")
	assert.Equal(t, manifest.BranchReady, res[0].Status, "saving again retries")
}

func TestBaseBranch_ARepoOnAHostWeCannotActOnIsReportedUnavailable(t *testing.T) {
	r := newBranchRig(t, newEnv(t))
	m, err := r.svc.Set(r.ctx, r.owner, []manifest.Repo{{Path: "app", Remote: "gitlab.example/acme/mono", BaseBranch: "team-a"}})
	require.NoError(t, err)

	res := r.svc.EnsureBaseBranches(r.ctx, m)

	assert.Equal(t, manifest.BranchUnavailable, res[0].Status)
}

func TestBaseBranch_OnlyAnOwnerSetsItAndAChangeIsAudited(t *testing.T) {
	r := newBranchRig(t, newEnv(t))
	_, err := r.svc.Set(r.ctx, r.member, []manifest.Repo{{Path: "app", Remote: "github.com/acme/mono", BaseBranch: "team-a"}})
	require.ErrorIs(t, err, manifest.ErrNotOwner)
	got, err := r.svc.Get(r.ctx)
	require.NoError(t, err)
	assert.Nil(t, got)
	assert.Equal(t, 0, r.events.Count(audit.BaseBranchChanged))

	r.set("team-a")
	assert.Equal(t, 1, r.events.Count(audit.BaseBranchChanged))
	r.set("team-a")
	assert.Equal(t, 1, r.events.Count(audit.BaseBranchChanged), "an unchanged setting is not an event")
	r.set("team-b")
	assert.Equal(t, 2, r.events.Count(audit.BaseBranchChanged))

	var last map[string]any
	for _, line := range strings.Split(strings.TrimSpace(r.log.String()), "\n") {
		require.NoError(t, json.Unmarshal([]byte(line), &last))
	}
	assert.Equal(t, "base_branch_changed", last["action"])
	assert.Equal(t, r.id.String(), last["tenant"])
	assert.Equal(t, r.owner.ID.String(), last["user_id"])
	assert.Equal(t, "app", last["path"])
	assert.Equal(t, "github.com/acme/mono", last["remote"])
	assert.Equal(t, "team-a", last["old"])
	assert.Equal(t, "team-b", last["new"])
}

func TestBaseBranch_RemovingARepoIsAuditedWithItsRemote(t *testing.T) {
	r := newBranchRig(t, newEnv(t))
	r.set("team-a")
	r.log.Reset()

	_, err := r.svc.Set(r.ctx, r.owner, []manifest.Repo{{Path: "other", Remote: "github.com/acme/other"}})
	require.NoError(t, err)

	assert.Contains(t, r.log.String(), `"remote":"github.com/acme/mono"`)
	assert.Contains(t, r.log.String(), `"old":"team-a"`)
}

func TestBaseBranch_EachTenantKeepsItsOwn(t *testing.T) {
	e := newEnv(t)
	a, b := newBranchRig(t, e), newBranchRig(t, e)

	a.set("slop-haus-team-a")
	b.set("slop-haus-team-b")

	got, err := a.svc.Get(a.ctx)
	require.NoError(t, err)
	assert.Equal(t, "slop-haus-team-a", got.Repos[0].BaseBranch)
	got, err = b.svc.Get(b.ctx)
	require.NoError(t, err)
	assert.Equal(t, "slop-haus-team-b", got.Repos[0].BaseBranch)
}

func TestBaseBranch_TheRouteReportsWhatMakingItCameTo(t *testing.T) {
	r := newBranchRig(t, newEnv(t))
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		next(huma.WithContext(ctx, authn.NewUserContext(ctx.Context(), r.owner)))
	})
	manifest.RegisterRoutes(api, manifest.NewHandler(r.svc, slog.DiscardHandler))

	rec := call(mux, http.MethodPut, "/api/workspace/manifest", `{"repos":[{"path":"app","remote":"github.com/acme/mono","base_branch":"slop-haus-team-a"}]}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Repos        []manifest.Repo             `json:"repos"`
		BaseBranches []manifest.BaseBranchResult `json:"base_branches"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "slop-haus-team-a", body.Repos[0].BaseBranch)
	require.Len(t, body.BaseBranches, 1)
	assert.Equal(t, manifest.BranchReady, body.BaseBranches[0].Status)

	rec = call(mux, http.MethodPut, "/api/workspace/manifest", `{"repos":[{"path":"app","remote":"github.com/acme/mono","base_branch":"ticket-1-a1"}]}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}
