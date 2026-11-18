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
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

var (
	mainSHA = strings.Repeat("a", 40)
	teamSHA = strings.Repeat("b", 40)
)

type syncRig struct {
	*tenantFixture
	fake   *codehosttest.Fake
	events *audittest.Counter
	repo   codehost.Repo
	key    codehosttest.BranchKey
	store  *ticket.PostgresStore
	log    *bytes.Buffer
}

func newSyncRig(t *testing.T, e *env) *syncRig {
	t.Helper()
	f := e.tenant()
	repo := codehost.Repo{Owner: "acme", Name: "mono"}
	key := codehosttest.BranchKey{Repo: repo, Branch: "team-a"}
	fake := &codehosttest.Fake{
		Domain:       "github.com",
		Remotes:      map[string]codehost.Repo{"github.com/acme/mono": repo},
		HeadOf:       map[codehost.Repo]*codehost.BranchHead{repo: {Branch: "main", SHA: mainSHA}},
		BranchAt:     map[codehosttest.BranchKey]string{key: teamSHA},
		ComparisonOf: map[codehosttest.BranchKey]*codehost.BranchComparison{key: {Ahead: 2, Behind: 5}},
		BranchMerged: codehost.BranchMerge{SHA: strings.Repeat("c", 40)},
	}
	reg := codehost.NewRegistry()
	reg.Register(fake, fake)
	counter := audittest.NewCounter()
	logs := &bytes.Buffer{}
	store := ticket.NewPostgresStore(e.database)
	f.svc = manifest.NewService(manifest.NewPostgresStore(e.database), user.NewPostgresStore(e.database), f.agents, f.layouts, slog.DiscardHandler,
		manifest.WithHosts(reg), manifest.WithAudit(audit.New(slog.NewJSONHandler(logs, nil), counter)), manifest.WithSync(store, f.tickets))
	f.adopt(manifest.Repo{Path: "app", Remote: "github.com/acme/mono", BaseBranch: "team-a"})
	return &syncRig{tenantFixture: f, fake: fake, events: counter, repo: repo, key: key, store: store, log: logs}
}

func TestDivergences_ShowBehindAndAheadOfTheDefaultBranch(t *testing.T) {
	r := newSyncRig(t, newEnv(t))
	m, err := r.svc.Get(r.ctx)
	require.NoError(t, err)

	got := r.svc.Divergences(r.ctx, m)

	assert.Equal(t, []manifest.Divergence{{Path: "app", BaseBranch: "team-a", DefaultBranch: "main", Behind: 5, Ahead: 2}}, got)
}

func TestDivergences_ShowNothingForARepoWithoutAnIntegrationBranch(t *testing.T) {
	r := newSyncRig(t, newEnv(t))
	r.adopt(manifest.Repo{Path: "app", Remote: "github.com/acme/mono"})
	m, err := r.svc.Get(r.ctx)
	require.NoError(t, err)
	assert.Empty(t, r.svc.Divergences(r.ctx, m))

	r.adopt(manifest.Repo{Path: "app", Remote: "github.com/acme/mono", BaseBranch: "main"})
	m, err = r.svc.Get(r.ctx)
	require.NoError(t, err)
	assert.Empty(t, r.svc.Divergences(r.ctx, m), "the default branch is not behind itself")
}

func TestDivergences_AnUnansweredRepoIsListedWithTheReasonAndHidesNoOther(t *testing.T) {
	r := newSyncRig(t, newEnv(t))
	other := codehost.Repo{Owner: "acme", Name: "other"}
	r.fake.Remotes["github.com/acme/other"] = other
	r.fake.HeadOf[other] = &codehost.BranchHead{Branch: "main", SHA: mainSHA}
	r.adopt(
		manifest.Repo{Path: "other", Remote: "github.com/acme/other", BaseBranch: "team-b"}, // the host has no such branch
		manifest.Repo{Path: "app", Remote: "github.com/acme/mono", BaseBranch: "team-a"},
	)
	m, err := r.svc.Get(r.ctx)
	require.NoError(t, err)

	got := r.svc.Divergences(r.ctx, m)

	require.Len(t, got, 2)
	byPath := map[string]manifest.Divergence{got[0].Path: got[0], got[1].Path: got[1]}
	assert.Contains(t, byPath["other"].Error, "team-b")
	assert.Zero(t, byPath["other"].Behind)
	assert.Empty(t, byPath["app"].Error)
	assert.Equal(t, 5, byPath["app"].Behind)
}

func TestSync_MergesTheDefaultBranchIntoTheBaseBranchAndAudits(t *testing.T) {
	r := newSyncRig(t, newEnv(t))

	res, err := r.svc.Sync(r.ctx, r.owner, "app")

	require.NoError(t, err)
	assert.Equal(t, &manifest.SyncResult{Path: "app", BaseBranch: "team-a", DefaultBranch: "main",
		Outcome: manifest.SyncMerged, Head: strings.Repeat("c", 40)}, res)
	assert.Equal(t, []codehosttest.BranchMergeCall{{Tenant: r.id, Repo: r.repo, Into: "team-a", From: "main"}}, r.fake.BranchMerges())
	assert.Equal(t, 1, r.events.Count(audit.RepoSynced))
	assert.Equal(t, map[string]any{"action": "repo_synced", "tenant": r.id.String(), "user_id": r.owner.ID.String(),
		"path": "app", "base_branch": "team-a", "default_branch": "main", "outcome": "merged", "head": strings.Repeat("c", 40)},
		r.lastAudit("ticket"))
}

func TestSync_NothingToMergeIsReportedAsSuch(t *testing.T) {
	r := newSyncRig(t, newEnv(t))
	r.fake.BranchMerged = codehost.BranchMerge{SHA: teamSHA, UpToDate: true}

	res, err := r.svc.Sync(r.ctx, r.owner, "app")

	require.NoError(t, err)
	assert.Equal(t, manifest.SyncUpToDate, res.Outcome)
	assert.Equal(t, teamSHA, res.Head)
}

func TestSync_OnlyAnOwnerAndTheHostIsNotTouchedOtherwise(t *testing.T) {
	r := newSyncRig(t, newEnv(t))

	_, err := r.svc.Sync(r.ctx, r.member, "app")

	require.ErrorIs(t, err, manifest.ErrNotOwner)
	assert.Empty(t, r.fake.BranchMerges())
	assert.Zero(t, r.events.Count(audit.RepoSynced))
}

func TestSync_RefusesARepoWithoutAnIntegrationBranch(t *testing.T) {
	r := newSyncRig(t, newEnv(t))
	r.adopt(manifest.Repo{Path: "app", Remote: "github.com/acme/mono"})
	_, err := r.svc.Sync(r.ctx, r.owner, "app")
	assert.ErrorIs(t, err, manifest.ErrInvalid)

	r.adopt(manifest.Repo{Path: "app", Remote: "github.com/acme/mono", BaseBranch: "main"})
	_, err = r.svc.Sync(r.ctx, r.owner, "app")
	assert.ErrorIs(t, err, manifest.ErrInvalid)

	_, err = r.svc.Sync(r.ctx, r.owner, "elsewhere")
	assert.ErrorIs(t, err, manifest.ErrInvalid)
	assert.Empty(t, r.fake.BranchMerges())
}

func TestSync_RefusedWhileTheBranchsLockIsHeldSayingByWhom(t *testing.T) {
	r := newSyncRig(t, newEnv(t))
	worker := r.newAgent("worker-7", nil)
	held := r.newTicket("holds the lock", "app/x/")
	_, err := r.tickets.Claim(r.ctx, worker.ID, &held.ID)
	require.NoError(t, err)
	lock := ticket.LockNameFor("app", &ticket.ResolvedBase{Branch: "team-a", Explicit: true})
	require.NoError(t, r.store.AcquireLock(r.ctx, worker.ID, lock))

	_, err = r.svc.Sync(r.ctx, r.owner, "app")

	var blocked *manifest.SyncBlockedError
	require.ErrorAs(t, err, &blocked)
	require.ErrorIs(t, err, manifest.ErrSyncBlocked)
	assert.Equal(t, "worker-7", blocked.AgentName)
	assert.Equal(t, held.ID, blocked.TicketID)
	assert.Empty(t, r.fake.BranchMerges())
	assert.Zero(t, r.events.Count(audit.RepoSynced))
}

func TestSync_AnotherBranchsLockDoesNotBlock(t *testing.T) {
	r := newSyncRig(t, newEnv(t))
	worker := r.newAgent("worker-7", nil)
	held := r.newTicket("holds the lock", "other/x/")
	_, err := r.tickets.Claim(r.ctx, worker.ID, &held.ID)
	require.NoError(t, err)
	require.NoError(t, r.store.AcquireLock(r.ctx, worker.ID, ticket.LockNameFor("other", &ticket.ResolvedBase{Branch: "team-a", Explicit: true})))
	require.NoError(t, r.store.AcquireLock(r.ctx, worker.ID, ticket.IntegrationLockName))

	_, err = r.svc.Sync(r.ctx, r.owner, "app")

	require.NoError(t, err)
}

// lastAudit is the last audit line as a map, without the keys named in drop
// and the fixed ones (time, level, msg) every line has.
func (r *syncRig) lastAudit(drop ...string) map[string]any {
	r.t.Helper()
	lines := strings.Split(strings.TrimSpace(r.log.String()), "\n")
	var got map[string]any
	require.NoError(r.t, json.Unmarshal([]byte(lines[len(lines)-1]), &got))
	for _, k := range append(drop, "time", "level", "msg", "client_ip") {
		delete(got, k)
	}
	return got
}

func (r *syncRig) conflict() {
	r.fake.BranchMergeErr = codehost.ErrMergeConflict
	r.fake.CommitsOf = map[codehosttest.CommitRange]*codehost.Diff{
		{From: mainSHA, To: teamSHA}: {Files: []codehost.FileDiff{{Path: "b.go"}, {Path: "only_team.go"}, {Path: "a.go"}}},
		{From: teamSHA, To: mainSHA}: {Files: []codehost.FileDiff{{Path: "a.go"}, {Path: "b.go"}, {Path: "only_main.go"}}},
	}
}

func TestSync_AConflictChangesNothingAndFilesOneTicketScopedToTheConflictingFiles(t *testing.T) {
	r := newSyncRig(t, newEnv(t))
	r.conflict()

	res, err := r.svc.Sync(r.ctx, r.owner, "app")

	require.NoError(t, err)
	assert.Equal(t, manifest.SyncConflict, res.Outcome)
	assert.Empty(t, res.Head)
	require.NotZero(t, res.TicketID)
	tk, err := r.tickets.Get(r.ctx, res.TicketID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusPreparing, tk.Status)
	assert.Equal(t, []string{"app/a.go", "app/b.go"}, r.pathsOf(res.TicketID))
	assert.Contains(t, tk.Title, "main into team-a")
	assert.Contains(t, tk.Body, mainSHA[:12])
	assert.Contains(t, tk.Body, teamSHA[:12])
	assert.Len(t, r.fake.BranchMerges(), 1, "the merge was tried once and nothing else moved a branch")
	assert.Equal(t, 1, r.events.Count(audit.RepoSynced))
	got := r.lastAudit("")
	assert.Equal(t, "conflict", got["outcome"])
	assert.EqualValues(t, res.TicketID, got["ticket"])
}

func TestSync_AConflictWhoseFilesCannotBeWorkedOutIsScopedToTheRepoAndSaysSo(t *testing.T) {
	r := newSyncRig(t, newEnv(t))
	r.fake.BranchMergeErr = codehost.ErrMergeConflict
	r.fake.CommitsErr = errors.New("compare too large")

	res, err := r.svc.Sync(r.ctx, r.owner, "app")

	require.NoError(t, err)
	assert.Equal(t, manifest.SyncConflict, res.Outcome)
	assert.Equal(t, []string{"app/"}, r.pathsOf(res.TicketID))
	tk, err := r.tickets.Get(r.ctx, res.TicketID)
	require.NoError(t, err)
	assert.Contains(t, tk.Body, "whole repo")
}

func TestSync_AHostFailureIsAnErrorAndFilesNothing(t *testing.T) {
	r := newSyncRig(t, newEnv(t))
	r.fake.BranchMergeErr = errors.New("boom")

	_, err := r.svc.Sync(r.ctx, r.owner, "app")

	require.ErrorIs(t, err, manifest.ErrSyncUnavailable)
	assert.Zero(t, r.events.Count(audit.RepoSynced))
}

func (r *syncRig) mux(who *authn.User) *http.ServeMux {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		next(huma.WithContext(ctx, authn.NewUserContext(ctx.Context(), who)))
	})
	manifest.RegisterRoutes(api, manifest.NewHandler(r.svc, slog.DiscardHandler))
	return mux
}

func TestSyncRoute_ReportsTheOutcomeAndTheWorkspaceShowsTheCounts(t *testing.T) {
	r := newSyncRig(t, newEnv(t))
	mux := r.mux(r.owner)

	rec := call(mux, http.MethodGet, "/api/workspace", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var ws struct {
		Divergences []manifest.Divergence `json:"divergences"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ws))
	require.Len(t, ws.Divergences, 1)
	assert.Equal(t, 5, ws.Divergences[0].Behind)

	rec = call(mux, http.MethodPost, "/api/workspace/manifest/sync", `{"path":"app"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var res manifest.SyncResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	assert.Equal(t, manifest.SyncMerged, res.Outcome)
}

func TestSyncRoute_Statuses(t *testing.T) {
	r := newSyncRig(t, newEnv(t))
	assert.Equal(t, http.StatusForbidden, call(r.mux(r.member), http.MethodPost, "/api/workspace/manifest/sync", `{"path":"app"}`).Code)
	assert.Equal(t, http.StatusUnprocessableEntity, call(r.mux(r.owner), http.MethodPost, "/api/workspace/manifest/sync", `{"path":"nope"}`).Code)

	worker := r.newAgent("worker-7", nil)
	held := r.newTicket("holds the lock", "app/x/")
	_, err := r.tickets.Claim(r.ctx, worker.ID, &held.ID)
	require.NoError(t, err)
	require.NoError(t, r.store.AcquireLock(r.ctx, worker.ID, ticket.LockNameFor("app", &ticket.ResolvedBase{Branch: "team-a", Explicit: true})))
	rec := call(r.mux(r.owner), http.MethodPost, "/api/workspace/manifest/sync", `{"path":"app"}`)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "worker-7")

	_, err = r.store.ForceReleaseLock(r.ctx, ticket.LockNameFor("app", &ticket.ResolvedBase{Branch: "team-a", Explicit: true}))
	require.NoError(t, err)
	r.fake.BranchMergeErr = errors.New("boom")
	rec = call(r.mux(r.owner), http.MethodPost, "/api/workspace/manifest/sync", `{"path":"app"}`)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}
