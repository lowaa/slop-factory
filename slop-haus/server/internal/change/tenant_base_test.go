package change_test

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

const tenantBase = "slop-haus-acme"

// withManifestBase has the manifest send the repo's changes to base, and the
// ticket service read that manifest too (the fixture's does not by default).
func (f *fixture) withManifestBase(base string) {
	f.t.Helper()
	pl := placedIn(hostName + "/acme/app")
	pl.Repos[0].BaseBranch = base
	f.ws.set(pl)
	f.useWorkspaceInTickets()
}

func (f *fixture) useWorkspaceInTickets() {
	f.tickets = ticket.NewService(f.ticketStore(), slog.DiscardHandler,
		ticket.WithCodeHosts(codehosttest.Registry(f.host)), ticket.WithWorkspace(f.ws))
	f.agents = agent.NewService(agent.NewPostgresStore(f.database), f.tickets, nil, slog.DiscardHandler)
	f.svc = f.serviceWith(f.host)
}

// hostBase is what the host says the change targets and compares against.
func (f *fixture) hostBase(base string) {
	f.host.ChecksOf[f.host.Opened].Base = base
	f.host.CompareOf[f.host.Opened].Base = base
}

func TestTenantBase_OpenTargetsTheManifestsBaseBranch(t *testing.T) {
	f := newFixture(t)
	f.withManifestBase(tenantBase)
	h := f.hold(false)

	f.open(h, true)

	opens := f.host.Opens()
	require.Len(t, opens, 1)
	assert.Equal(t, tenantBase, opens[0].Request.Base)
	assert.Equal(t, tenantBase, f.currentOf(h, 1).Base)
}

func TestTenantBase_TheRepoDefaultBranchIsUsedWhenTheManifestNamesNone(t *testing.T) {
	f := newFixture(t)
	f.useWorkspaceInTickets()
	f.host.HeadOf = map[codehost.Repo]*codehost.BranchHead{appRepo: {Branch: "trunk", SHA: baseNew}}
	h := f.hold(false)

	f.open(h, true)

	require.Len(t, f.host.Opens(), 1)
	assert.Equal(t, "trunk", f.host.Opens()[0].Request.Base)
	assert.Equal(t, baseNew, *h.ticket.BaseCommit, "pinned from the default branch")
}

func TestTenantBase_AClaimPinsTheBaseBranchsHeadNotTheDefaultsAndLeavesTheOneBranchAlone(t *testing.T) {
	f := newFixture(t)
	f.withManifestBase(tenantBase)
	f.host.HeadOf = map[codehost.Repo]*codehost.BranchHead{appRepo: {Branch: "main", SHA: baseNew}}
	f.host.BranchAt = map[codehosttest.BranchKey]string{{Repo: appRepo, Branch: tenantBase}: baseOld}

	h := f.hold(false)

	require.NotNil(t, h.ticket.BaseCommit)
	assert.Equal(t, baseOld, *h.ticket.BaseCommit)
	assert.Equal(t, []codehosttest.BranchKey{{Repo: appRepo, Branch: tenantBase}}, f.host.BranchHeadReads())
}

func TestTenantBase_AnUnpromotedAttemptsBaseBranchIsCutFromThePinnedCommitOfTheTenantBase(t *testing.T) {
	f := newFixture(t)
	f.withManifestBase(tenantBase)
	f.host.BranchAt = map[codehosttest.BranchKey]string{{Repo: appRepo, Branch: tenantBase}: baseOld}
	first := f.hold(false)
	second := f.shadowOf(first)
	// The tenant base moved on; the shadow must not see it.
	f.host.BranchAt[codehosttest.BranchKey{Repo: appRepo, Branch: tenantBase}] = baseNew

	f.openAs(second, 8, true)

	assert.Equal(t, baseOld, f.host.BranchAt[f.baseBranchKey(first.ticket.ID)])
	_, err := f.svc.Open(f.ctx, f.tenant, second.agent, &change.OpenInput{Branch: f.branchOf(second), Title: "T", Draft: true})
	require.NoError(t, err)
	calls := f.host.TakesCalls()
	require.NotEmpty(t, calls)
	assert.Equal(t, tenantBase, calls[0].Branch, "the shadow rule compares the tenant base, not main")
}

func TestTenantBase_TheMergeGateHoldsTheChangeToTheTenantBase(t *testing.T) {
	f := newFixture(t)
	f.withManifestBase(tenantBase)
	h := f.hold(false)
	f.open(h, false)
	f.lock(h)

	// The host still has the change on main: not the branch the tenant merges into.
	_, err := f.merge(h, "")
	require.ErrorIs(t, err, change.ErrWrongBase)
	assert.Empty(t, f.host.Merges())

	f.hostBase(tenantBase)
	merged, err := f.merge(h, headA)
	require.NoError(t, err)
	assert.Equal(t, "mergecommit", merged.MergeSHA)
}

func TestTenantBase_AHeadBehindTheTenantBaseIsRefusedAndTheRemedyNamesIt(t *testing.T) {
	f := newFixture(t)
	f.withManifestBase(tenantBase)
	h := f.hold(false)
	f.open(h, false)
	f.lock(h)
	f.hostBase(tenantBase)
	f.host.CompareOf[f.host.Opened].ContainsTip = false

	_, err := f.merge(h, "")

	require.ErrorIs(t, err, change.ErrBehindBase)
	assert.Contains(t, err.Error(), "merge origin/"+tenantBase)
}

func TestTenantBase_DriftIsReadAgainstTheTenantBase(t *testing.T) {
	f := newFixture(t)
	pl := placedIn(hostName + "/acme/app")
	pl.Repos[0].BaseBranch = tenantBase
	pl.Repos[0].DeployOnly = []scope.DeployOnlyRule{{Path: "deploy/*"}}
	f.ws.set(pl)
	f.useWorkspaceInTickets()
	h := f.hold(false)
	f.open(h, false)
	f.lock(h)
	f.hostBase(tenantBase)
	f.host.CompareOf[f.host.Opened].ContainsTip = false
	// The diff says the change targets main: it is not read as drift on the tenant's base.
	f.host.DiffOf = map[string]*codehost.Diff{headA: {Base: "main", BaseSHA: baseNew, MergeBaseSHA: baseOld}}

	_, err := f.merge(h, "")

	require.ErrorIs(t, err, change.ErrWrongBase)
}

func TestTenantBase_PromotionRetargetsToTheTenantBase(t *testing.T) {
	f := newFixture(t)
	f.withManifestBase(tenantBase)
	f.host.BranchAt = map[codehosttest.BranchKey]string{{Repo: appRepo, Branch: tenantBase}: baseOld}
	first := f.hold(false)
	second := f.shadowOf(first)
	f.openAs(first, 7, true)
	f.openAs(second, 8, true)
	f.withTitle(ref7, "Add the thing")
	f.withTitle(ref8, prefixed)

	f.promoteAttempt(first, 2)
	require.NoError(t, f.svc.ApplyPromotion(f.ctx, f.tenant, first.ticket.ID))

	assert.Contains(t, f.host.Retargeted(), codehosttest.RetargetCall{Tenant: f.tenant, Change: ref8, Base: tenantBase})
	assert.Equal(t, tenantBase, f.currentOf(first, 2).Base)
}

func TestTenantBase_TheLockIsPerRepoAndBranch(t *testing.T) {
	f := newFixture(t)
	other := placedIn(hostName + "/acme/app")
	other.Repos[0].BaseBranch = tenantBase
	f.ws.set(placedIn(hostName + "/acme/app"))
	f.ws.byScope = map[string]*ticket.Placement{"app/other/": other, "app/third/": other}
	f.useWorkspaceInTickets()

	onDefault := f.hold(false)
	onBase := f.hold2()
	third := f.holdAt("app/third/")

	f.lock(onDefault)
	f.lock(onBase)
	err := f.tickets.AcquireIntegrationLock(f.ctx, third.agent)
	var held *ticket.LockHeldError
	require.ErrorAs(t, err, &held, "the same branch is the same queue")
	assert.Equal(t, onBase.agent, held.Holder.AgentID)
	assert.Equal(t, 1, held.Position)

	state, err := f.tickets.IntegrationLockState(f.ctx)
	require.NoError(t, err)
	require.NotNil(t, state.Holder)
	assert.Equal(t, onDefault.agent, state.Holder.AgentID, "the tenant's default lock is the board's")
	assert.Empty(t, state.Queue)
	own, err := f.tickets.IntegrationLockStateFor(f.ctx, third.agent)
	require.NoError(t, err)
	require.Len(t, own.Queue, 1)
}

func TestTenantBase_AMergeNeedsTheLockOfItsOwnBranch(t *testing.T) {
	f := newFixture(t)
	f.withManifestBase(tenantBase)
	h := f.hold(false)
	f.open(h, false)
	f.hostBase(tenantBase)
	f.lock(h)
	// A lock held under the default name, as before the manifest named a base.
	_, err := f.database.Exec(f.ctx, `UPDATE locks SET name = 'integration' WHERE holder_agent_id = $1`, h.agent)
	require.NoError(t, err)

	_, err = f.merge(h, "")

	require.ErrorIs(t, err, change.ErrLockNotHeld)
}

// holdAt is hold for a ticket under path.
func (f *fixture) holdAt(path string) *holder {
	f.t.Helper()
	a := f.newAgent("builder-" + path)
	created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{
		Title: "At " + path, Paths: []model.Path{{Path: path}}, Status: ticket.StatusOpen,
	})
	require.NoError(f.t, err)
	claimed, err := f.tickets.Claim(f.ctx, a, &created.ID)
	require.NoError(f.t, err)
	require.NotNil(f.t, claimed.Ticket)
	return &holder{agent: a, ticket: claimed.Ticket, attempt: claimed.Attempt.Number}
}

func TestTenantBase_AnUnpromotedAttemptCannotTakeTheBranchLock(t *testing.T) {
	f := newFixture(t)
	f.withManifestBase(tenantBase)
	first := f.hold(false)
	second := f.shadowOf(first)

	err := f.tickets.AcquireIntegrationLock(f.ctx, second.agent)

	require.ErrorIs(t, err, ticket.ErrAttemptAwaitsChoice)
}

func TestTenantBase_AnOwnerChangingTheBaseWhileTheLockIsHeldDoesNotStrandIt(t *testing.T) {
	f := newFixture(t)
	f.withManifestBase(tenantBase)
	h := f.hold(false)
	f.lock(h)
	f.withManifestBase("")

	require.NoError(t, f.tickets.ReleaseIntegrationLock(f.ctx, h.agent))

	other := f.hold2()
	require.NoError(t, f.tickets.AcquireIntegrationLock(f.ctx, other.agent))
}
