package pendingaction_test

import (
	"fmt"
	"net/http"
	"strings"

	"context"
	"github.com/danielgtaylor/huma/v2"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
)

// manifestRig is the real manifest store behind a real pending-action service;
// only who is an owner (a table of another service) and the auto-approve
// setting are stood in for.
type manifestRig struct {
	*decider
	owner     *authn.User
	owners    *ownersFake
	auto      *autoSwitch
	store     *manifest.PostgresStore
	board     *manifest.Service
	boardUser *authn.User
}

func newManifestRig(t *testing.T) *manifestRig {
	t.Helper()
	d := newDecider(t)
	r := &manifestRig{decider: d, auto: &autoSwitch{}, owners: &ownersFake{owners: map[uuid.UUID]bool{}}}
	r.owner = &authn.User{ID: dbtest.NewUser(t, d.database), TenantID: d.tenant}
	r.owners.owners[r.owner.ID] = true
	r.boardUser = r.owner
	r.store = manifest.NewPostgresStore(d.database)
	r.board = manifest.NewService(r.store, r.owners, nil, nil, slog.DiscardHandler)
	recorder, counter := audittest.NewRecorder()
	d.events = counter
	d.svc = pendingaction.NewService(pendingaction.NewPostgresStore(d.database), pendingaction.Config{}, slog.DiscardHandler,
		pendingaction.WithApproval(d.tickets, d.grants), pendingaction.WithEvents(recorder), pendingaction.WithRoles(r.owners),
		pendingaction.WithManifests(manifest.NewEditor(r.store)),
		pendingaction.WithAutoApprove(ownerResolver{user: r.owner.ID}, r.auto))
	d.svc.SetNow(d.clock.Now)
	return r
}

func repoOf(path, remote string) manifest.Repo { return manifest.Repo{Path: path, Remote: remote} }

func (r *manifestRig) save(t *testing.T, repos ...manifest.Repo) *manifest.Manifest {
	t.Helper()
	m, err := r.board.Set(r.ctx, r.boardUser, repos)
	require.NoError(t, err)
	return m
}

func (r *manifestRig) tryPropose(base int64, repos ...manifest.Repo) (*pendingaction.Action, error) {
	return r.svc.Propose(r.ctx, &pendingaction.ProposeParams{
		AgentID: r.console, Kind: pendingaction.KindManifestEdit,
		Payload: pendingaction.Payload{ManifestEdit: &pendingaction.ManifestEditPayload{BaseRevision: base, Manifest: repos}},
	})
}

func (r *manifestRig) proposeManifest(t *testing.T, base int64, repos ...manifest.Repo) *pendingaction.Action {
	t.Helper()
	a, err := r.tryPropose(base, repos...)
	require.NoError(t, err)
	return a
}

func (r *manifestRig) current(t *testing.T) *manifest.Manifest {
	t.Helper()
	m, err := r.store.Get(r.ctx)
	require.NoError(t, err)
	return m
}

func deployRules(rules ...manifest.DeployOnlyRule) manifest.Repo {
	r := repoOf("app", "github.com/acme/app")
	r.DeployOnly = rules
	return r
}

func TestAnInvalidManifestIsRefusedAtProposal(t *testing.T) {
	r := newManifestRig(t)
	for name, repos := range map[string][]manifest.Repo{
		"escaping path":             {repoOf("../x", "github.com/acme/app")},
		"no repos":                  nil,
		"bad deploy regex":          {deployRules(manifest.DeployOnlyRule{Path: "a.yaml", Lines: "("})},
		"bad independent app":       {{Path: "app", Remote: "github.com/acme/app", IndependentApps: []string{"../x/"}}},
		"bad base branch":           {{Path: "app", Remote: "github.com/acme/app", BaseBranch: "a..b"}},
		"base branch of an attempt": {{Path: "app", Remote: "github.com/acme/app", BaseBranch: "ticket-1-a1"}},
		"bad resolve":               {{Path: "app", Remote: "github.com/acme/app", SharedPaths: []manifest.SharedPathRule{{Path: "gen/", Resolve: "yolo"}}}},
	} {
		_, err := r.tryPropose(0, repos...)
		assert.ErrorIs(t, err, pendingaction.ErrInvalid, name)
	}
	assert.Empty(t, r.pending(t), "an invalid one never reaches the owner")
}

func TestAProposalOnAnUnsupportedPayloadVersionOrWithStrayFieldsIsRefused(t *testing.T) {
	r := newManifestRig(t)
	_, err := r.svc.Propose(r.ctx, &pendingaction.ProposeParams{AgentID: r.console, Kind: pendingaction.KindManifestEdit,
		Payload: pendingaction.Payload{ManifestEdit: &pendingaction.ManifestEditPayload{
			PayloadVersion: 2, Manifest: []manifest.Repo{repoOf("app", "github.com/acme/app")}}}})
	require.ErrorIs(t, err, pendingaction.ErrInvalid)
	_, err = r.svc.Propose(r.ctx, &pendingaction.ProposeParams{AgentID: r.console, Kind: pendingaction.KindManifestEdit,
		Payload: pendingaction.Payload{Body: "x", ManifestEdit: &pendingaction.ManifestEditPayload{
			Manifest: []manifest.Repo{repoOf("app", "github.com/acme/app")}}}})
	require.ErrorIs(t, err, pendingaction.ErrInvalid)
	_, err = r.svc.Propose(r.ctx, &pendingaction.ProposeParams{AgentID: r.console, Kind: pendingaction.KindManifestEdit})
	require.ErrorIs(t, err, pendingaction.ErrInvalid)
	_, err = r.svc.Propose(r.ctx, &pendingaction.ProposeParams{AgentID: r.console, Kind: pendingaction.KindReady,
		TicketID: r.ticketIn(t, "preparing"), Payload: pendingaction.Payload{ManifestEdit: &pendingaction.ManifestEditPayload{}}})
	require.ErrorIs(t, err, pendingaction.ErrInvalid, "a ticket kind takes no manifest")
}

func TestAStaleBaseRevisionIsRefusedAtProposal(t *testing.T) {
	r := newManifestRig(t)
	m := r.save(t, repoOf("app", "github.com/acme/app"))
	require.EqualValues(t, 1, m.Revision)

	_, err := r.tryPropose(0, repoOf("app", "github.com/acme/other"))
	require.ErrorIs(t, err, pendingaction.ErrInvalid)
	assert.Contains(t, err.Error(), "revision 1")
	_, err = r.tryPropose(2, repoOf("app", "github.com/acme/other"))
	require.ErrorIs(t, err, pendingaction.ErrInvalid, "a revision from the future is no better")
	_, err = r.tryPropose(1, repoOf("app", "github.com/acme/other"))
	require.NoError(t, err)
}

func TestAnOwnerApprovingAppliesTheManifestAsTheirOwnEdit(t *testing.T) {
	r := newManifestRig(t)
	r.save(t, repoOf("app", "github.com/acme/app"))
	next := deployRules(manifest.DeployOnlyRule{Path: "deploy/*.yaml", Lines: `\s*newTag: \S+`})
	p := r.proposeManifest(t, 1, next)
	assert.Equal(t, pendingaction.StatePending, p.State)
	assert.Equal(t, "github.com/acme/app", r.current(t).Repos[0].Remote)
	assert.Empty(t, r.current(t).Repos[0].DeployOnly, "proposing changes nothing")

	settled, err := r.svc.Approve(r.ctx, r.owner, p.ID)
	require.NoError(t, err)
	assert.Equal(t, pendingaction.StateApproved, settled.State)

	got := r.current(t)
	assert.EqualValues(t, 2, got.Revision)
	require.Len(t, got.Repos[0].DeployOnly, 1)
	assert.Equal(t, "deploy/*.yaml", got.Repos[0].DeployOnly[0].Path)
	require.NotNil(t, got.UpdatedBy)
	assert.Equal(t, r.owner.ID, *got.UpdatedBy, "the approver's edit, not the console's")
	assert.Equal(t, 1, r.events.Count(audit.ManifestChanged))
	assert.Equal(t, 1, r.events.Count(audit.ProposalApproved))

	_, err = r.svc.Approve(r.ctx, r.owner, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrNotPending, "a replay applies nothing")
	assert.EqualValues(t, 2, r.current(t).Revision)
}

func TestAFirstManifestCanBeProposedAndApproved(t *testing.T) {
	r := newManifestRig(t)
	p := r.proposeManifest(t, 0, repoOf("app", "git@github.com:Acme/App.git"))
	_, err := r.svc.Approve(r.ctx, r.owner, p.ID)
	require.NoError(t, err)
	got := r.current(t)
	require.NotNil(t, got)
	assert.EqualValues(t, 1, got.Revision)
	assert.Equal(t, "github.com/acme/app", got.Repos[0].Remote, "stored normalised, as the board stores it")
}

func TestABoardSaveBetweenProposalAndApprovalSettlesItStaleAndWritesNothing(t *testing.T) {
	r := newManifestRig(t)
	r.save(t, repoOf("app", "github.com/acme/app"))
	p := r.proposeManifest(t, 1, repoOf("app", "github.com/acme/proposed"))

	r.save(t, repoOf("app", "github.com/acme/app"), repoOf("lib", "github.com/acme/lib"))

	settled, err := r.svc.Approve(r.ctx, r.owner, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrStale)
	assert.Equal(t, pendingaction.StateStale, settled.State)
	got := r.current(t)
	assert.EqualValues(t, 2, got.Revision, "only the board's save was written")
	assert.Len(t, got.Repos, 2, "the other edit is not undone")
	assert.Equal(t, 0, r.events.Count(audit.ManifestChanged))
	assert.Equal(t, 1, r.events.Count(audit.ProposalRefused))
}

func TestAClearAndRecreateDoesNotMakeAnOldProposalCurrentAgain(t *testing.T) {
	r := newManifestRig(t)
	r.save(t, repoOf("app", "github.com/acme/app"))
	p := r.proposeManifest(t, 1, repoOf("app", "github.com/acme/proposed"))

	require.NoError(t, r.board.Clear(r.ctx, r.owner))
	rev, err := r.store.Revision(r.ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 2, rev, "a clear is a revision; the count is not reset")
	r.save(t, repoOf("app", "github.com/acme/app"))

	_, err = r.svc.Approve(r.ctx, r.owner, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrStale)
	assert.Equal(t, "github.com/acme/app", r.current(t).Repos[0].Remote)
}

func TestAProposalPendingWhileNothingIsDeclaredGoesStaleWhenABoardSaveLands(t *testing.T) {
	r := newManifestRig(t)
	p := r.proposeManifest(t, 0, repoOf("app", "github.com/acme/proposed"))
	r.save(t, repoOf("app", "github.com/acme/app"))
	_, err := r.svc.Approve(r.ctx, r.owner, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrStale)
	assert.Equal(t, "github.com/acme/app", r.current(t).Repos[0].Remote)
}

// rewritePayload stores a payload the way an older or newer release could have.
func (r *manifestRig) rewritePayload(t *testing.T, id uuid.UUID, payload string) {
	t.Helper()
	r.exec(t, `UPDATE pending_actions SET payload = $2::jsonb WHERE id = $1`, id, payload)
}

func TestAStoredPayloadInvalidUnderTheCurrentRulesFailsAtApproval(t *testing.T) {
	r := newManifestRig(t)
	r.save(t, repoOf("app", "github.com/acme/app"))
	p := r.proposeManifest(t, 1, repoOf("app", "github.com/acme/proposed"))
	r.rewritePayload(t, p.ID, `{"manifest_edit":{"payload_version":1,"base_revision":1,"manifest":[{"path":"../escape","remote":"github.com/acme/x"}]}}`)

	settled, err := r.svc.Approve(r.ctx, r.owner, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrRefused)
	assert.Equal(t, pendingaction.StateFailed, settled.State)
	assert.Contains(t, settled.Outcome, "path")
	assert.EqualValues(t, 1, r.current(t).Revision)
	assert.Equal(t, "github.com/acme/app", r.current(t).Repos[0].Remote)
}

func TestAStoredPayloadWithAnUnknownFieldFailsAtApproval(t *testing.T) {
	for name, payload := range map[string]string{
		"in the manifest edit": `{"manifest_edit":{"payload_version":1,"base_revision":1,"surprise":true,"manifest":[{"path":"app","remote":"github.com/acme/x"}]}}`,
		"in a repo":            `{"manifest_edit":{"payload_version":1,"base_revision":1,"manifest":[{"path":"app","remote":"github.com/acme/x","auto_merge":true}]}}`,
		"beside it":            `{"manifest_edit":{"payload_version":1,"base_revision":1,"manifest":[{"path":"app","remote":"github.com/acme/x"}]},"new_field":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			r := newManifestRig(t)
			r.save(t, repoOf("app", "github.com/acme/app"))
			p := r.proposeManifest(t, 1, repoOf("app", "github.com/acme/proposed"))
			r.rewritePayload(t, p.ID, payload)

			settled, err := r.svc.Approve(r.ctx, r.owner, p.ID)
			require.ErrorIs(t, err, pendingaction.ErrRefused)
			assert.Equal(t, pendingaction.StateFailed, settled.State)
			assert.Equal(t, "github.com/acme/app", r.current(t).Repos[0].Remote)
		})
	}
}

func TestAStoredPayloadOfAnUnknownVersionFailsAtApproval(t *testing.T) {
	for _, version := range []string{"0", "2"} {
		r := newManifestRig(t)
		r.save(t, repoOf("app", "github.com/acme/app"))
		p := r.proposeManifest(t, 1, repoOf("app", "github.com/acme/proposed"))
		r.rewritePayload(t, p.ID, `{"manifest_edit":{"payload_version":`+version+`,"base_revision":1,"manifest":[{"path":"app","remote":"github.com/acme/x"}]}}`)

		settled, err := r.svc.Approve(r.ctx, r.owner, p.ID)
		require.ErrorIs(t, err, pendingaction.ErrRefused, version)
		assert.Equal(t, pendingaction.StateFailed, settled.State)
		assert.Contains(t, settled.Outcome, "payload_version")
		assert.EqualValues(t, 1, r.current(t).Revision)
	}
}

func TestAMemberCannotDecideAManifestEdit(t *testing.T) {
	r := newManifestRig(t)
	p := r.proposeManifest(t, 0, repoOf("app", "github.com/acme/app"))

	_, err := r.svc.Approve(r.ctx, r.approver, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrNotApprover)
	_, err = r.svc.Reject(r.ctx, r.approver, p.ID, "no")
	require.ErrorIs(t, err, pendingaction.ErrNotApprover)
	rejected, err := r.svc.RejectAll(r.ctx, r.approver, nil, "clearing")
	require.NoError(t, err)
	assert.Empty(t, rejected)
	assert.Equal(t, pendingaction.StatePending, r.stateOf(t, p).State)
	assert.Nil(t, r.current(t))

	r.owners.owners[r.owner.ID] = false
	_, err = r.svc.Approve(r.ctx, r.owner, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrNotApprover, "an owner demoted since is a member")
}

func TestAutoApproveSwitchedOnForEverythingLeavesAManifestEditPending(t *testing.T) {
	r := newManifestRig(t)
	r.auto.on.Store(true)
	p := r.proposeManifest(t, 0, repoOf("app", "github.com/acme/app"))
	assert.Equal(t, pendingaction.StatePending, p.State)
	assert.False(t, p.AutoApproved())
	assert.Nil(t, r.current(t))
	assert.NotContains(t, pendingaction.AutoApprovableKinds(), pendingaction.KindManifestEdit)

	_, err := r.svc.Approve(r.ctx, r.owner, p.ID)
	require.NoError(t, err, "a click still approves it")
}

func TestAManifestEditCannotTouchAnotherTenant(t *testing.T) {
	r := newManifestRig(t)
	other := dbtest.NewTenant(t, r.database)
	otherCtx := db.WithTenant(context.Background(), other)
	otherOwner := &authn.User{ID: dbtest.NewUser(t, r.database), TenantID: other}
	r.owners.owners[otherOwner.ID] = true
	_, err := manifest.NewService(r.store, r.owners, nil, nil, slog.DiscardHandler).Set(otherCtx, otherOwner, []manifest.Repo{repoOf("app", "github.com/other/app")})
	require.NoError(t, err)

	p := r.proposeManifest(t, 0, repoOf("app", "github.com/acme/app"))
	_, err = r.svc.Approve(otherCtx, otherOwner, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrNotFound, "another tenant's owner cannot even see it")

	_, err = r.svc.Approve(r.ctx, r.owner, p.ID)
	require.NoError(t, err)
	theirs, err := r.store.Get(otherCtx)
	require.NoError(t, err)
	assert.Equal(t, "github.com/other/app", theirs.Repos[0].Remote)
	assert.EqualValues(t, 1, theirs.Revision)
	assert.Equal(t, "github.com/acme/app", r.current(t).Repos[0].Remote)
}

func TestAManifestEditHasNoTicketAndASecondOneSupersedesTheFirst(t *testing.T) {
	r := newManifestRig(t)
	first := r.proposeManifest(t, 0, repoOf("app", "github.com/acme/one"))
	require.Nil(t, first.TicketID)
	second := r.proposeManifest(t, 0, repoOf("app", "github.com/acme/two"))
	assert.Equal(t, pendingaction.StateWithdrawn, r.stateOf(t, first).State)
	assert.Equal(t, pendingaction.StatePending, r.stateOf(t, second).State)

	_, err := r.svc.Propose(r.ctx, &pendingaction.ProposeParams{AgentID: r.console, Kind: pendingaction.KindManifestEdit, TicketID: 1,
		Payload: pendingaction.Payload{ManifestEdit: &pendingaction.ManifestEditPayload{Manifest: []manifest.Repo{repoOf("app", "github.com/acme/app")}}}})
	require.ErrorIs(t, err, pendingaction.ErrInvalid)
}

func TestAServiceWithoutManifestsCannotProposeOrApproveOne(t *testing.T) {
	r := newManifestRig(t)
	p := r.proposeManifest(t, 0, repoOf("app", "github.com/acme/app"))
	bare := pendingaction.NewService(pendingaction.NewPostgresStore(r.database), pendingaction.Config{}, slog.DiscardHandler,
		pendingaction.WithApproval(r.tickets, r.grants), pendingaction.WithRoles(r.owners))
	bare.SetNow(r.clock.Now)
	_, err := bare.Propose(r.ctx, &pendingaction.ProposeParams{AgentID: r.console, Kind: pendingaction.KindManifestEdit,
		Payload: pendingaction.Payload{ManifestEdit: &pendingaction.ManifestEditPayload{Manifest: []manifest.Repo{repoOf("app", "github.com/acme/app")}}}})
	require.ErrorIs(t, err, pendingaction.ErrInvalid)
	_, err = bare.Approve(r.ctx, r.owner, p.ID)
	require.Error(t, err)
	assert.Nil(t, r.current(t))
}

func TestThePendingViewDiffsTheProposalAgainstTheLiveManifest(t *testing.T) {
	r := newManifestRig(t)
	r.save(t, repoOf("app", "github.com/acme/app"), repoOf("lib", "github.com/acme/lib"))
	proposed := deployRules(manifest.DeployOnlyRule{Path: "deploy/*.yaml"})
	proposed.SharedPaths = []manifest.SharedPathRule{{Path: "gen/", Resolve: "regenerate"}}
	proposed.IndependentApps = []string{"app/"}
	proposed.SkipRetestAcrossApps = true
	p := r.proposeManifest(t, 1, proposed)

	v, err := r.svc.ViewOf(r.ctx, testPerson(), p.ID)
	require.NoError(t, err)
	require.NotNil(t, v.ManifestEdit)
	assert.Empty(t, v.StaleReason)
	assert.Contains(t, v.ActionLine, "Replace the workspace manifest")
	changes := map[string]manifest.RepoChange{}
	for _, c := range v.ManifestEdit.Changes {
		changes[c.Path] = c
	}
	require.Len(t, changes, 2)
	assert.Equal(t, manifest.OpRemoved, changes["lib"].Op)
	assert.Equal(t, manifest.OpChanged, changes["app"].Op)
	assert.Len(t, changes["app"].DeployOnlyAdded, 1)
	assert.Len(t, changes["app"].SharedPathsAdded, 1)
	assert.Equal(t, []string{"app/"}, changes["app"].IndependentAppsAdded)
	assert.Equal(t, &manifest.FlagChange{New: true}, changes["app"].SkipRetestAcrossApps)

	r.save(t, repoOf("app", "github.com/acme/app"))
	v, err = r.svc.ViewOf(r.ctx, testPerson(), p.ID)
	require.NoError(t, err)
	assert.Contains(t, v.StaleReason, "revision 1")

	_, _ = r.svc.Approve(r.ctx, r.owner, p.ID)
	v, err = r.svc.ViewOf(r.ctx, testPerson(), p.ID)
	require.NoError(t, err)
	assert.Empty(t, v.ManifestEdit.Changes, "a decided proposal is not diffed against a manifest that has moved on")
	assert.Len(t, v.ManifestEdit.Repos, 1)
}

func TestAManifestOverTheSizeLimitIsRefusedAtProposal(t *testing.T) {
	r := newManifestRig(t)
	repo := repoOf("app", "github.com/acme/app")
	for i := 0; i < manifest.MaxDeployOnly; i++ {
		repo.DeployOnly = append(repo.DeployOnly, manifest.DeployOnlyRule{Path: fmt.Sprintf("%03d/", i) + strings.Repeat("a", manifest.MaxDeployOnlyText-4)})
	}
	repos := []manifest.Repo{repo}
	for i := 1; i < 20; i++ {
		other := repo
		other.Path = fmt.Sprintf("r%d", i)
		repos = append(repos, other)
	}
	_, err := r.tryPropose(0, repos...)
	require.ErrorIs(t, err, pendingaction.ErrInvalid)
	assert.Contains(t, err.Error(), "bytes")
	assert.Empty(t, r.pending(t))
}

func TestTheApprovalsRoutesShowAManifestProposalAsAReadableDiff(t *testing.T) {
	r := newManifestRig(t)
	r.save(t, repoOf("app", "github.com/acme/app"), repoOf("lib", "github.com/acme/lib"))
	p := r.proposeManifest(t, 1, deployRules(manifest.DeployOnlyRule{Path: "deploy/*.yaml"}))
	h := pendingaction.NewHandler(r.svc, slog.DiscardHandler)
	ctx := authn.NewUserContext(r.ctx, r.owner)

	got, err := h.GetProposal(ctx, &pendingaction.ProposalIDRequest{ID: p.ID.String()})
	require.NoError(t, err)
	b := got.Body
	assert.Equal(t, "manifest_edit", b.Kind)
	assert.Equal(t, "tenant", b.Subject)
	assert.Equal(t, "owner", b.ApproverRole)
	assert.Nil(t, b.TicketID)
	require.NotNil(t, b.ManifestEdit)
	assert.EqualValues(t, 1, b.ManifestEdit.BaseRevision)
	assert.Len(t, b.ManifestEdit.Repos, 1)
	require.Len(t, b.ManifestEdit.Changes, 2)
	assert.Equal(t, manifest.OpChanged, b.ManifestEdit.Changes[0].Op)
	assert.Equal(t, manifest.OpRemoved, b.ManifestEdit.Changes[1].Op)

	member := authn.NewUserContext(r.ctx, r.approver)
	_, err = h.ApproveProposal(member, &pendingaction.ProposalIDRequest{ID: p.ID.String()})
	var status huma.StatusError
	require.ErrorAs(t, err, &status)
	assert.Equal(t, http.StatusForbidden, status.GetStatus())

	done, err := h.ApproveProposal(ctx, &pendingaction.ProposalIDRequest{ID: p.ID.String()})
	require.NoError(t, err)
	assert.Equal(t, "approved", done.Body.State)
	assert.Empty(t, done.Body.ManifestEdit.Changes, "decided: nothing to compare with")
}

func TestAProposalMayNotPutARepoAtTheRoot(t *testing.T) {
	r := newManifestRig(t)
	_, err := r.tryPropose(0, repoOf(".", "github.com/acme/app"))
	require.ErrorIs(t, err, pendingaction.ErrInvalid)
	require.ErrorIs(t, err, manifest.ErrInvalid)
}
