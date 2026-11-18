package landing_test

import (
	"context"
	"fmt"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

var (
	kustomizeRule = scope.DeployOnlyRule{Path: "deploy/*/kustomization.yaml", Lines: `\s*newTag: \S+`}
	tagBumpPatch  = "@@ -5,3 +5,3 @@ images:\n   - name: api\n-    newTag: 20261011T1\n+    newTag: 20261011T2\n   - name: web"
)

var threeInApp = []string{"app-a/server/", "app-a/server/", "app-a/server/"}

// firstOutcome is the first outcome recorded for the ticket.
func (b *batchRunFixture) firstOutcome(id int64) landing.Record {
	b.t.Helper()
	recs := b.outcomesOfTicket(id)
	require.NotEmpty(b.t, recs, "ticket %d has no outcome", id)
	return recs[0]
}

func TestBatchRun_AMemberTheGateRefusesTheEvidenceOfIsHandedBackKeepingItsPlaceAndNothingMergesOnTheBatch(t *testing.T) {
	b, cs := newBatchRun(t, 3, threeInApp...)
	b.world.evidenceRefused = true

	b.land(b.runner)

	assert.Empty(t, b.world.merges, "a merge the gate refused is not made")
	for _, c := range cs {
		assert.Equal(t, landing.BatchVoided, b.firstOutcome(c.ticketID).Outcome, "not merge_refused or a host error: it is the batch's evidence that is stale")
	}
	for _, c := range cs[:2] {
		row := b.row(c.ticketID)
		require.NotNil(t, row, "still queued")
		assert.True(t, row.KeepsPlace, "a stale batch is not the author's fault")
	}
	assert.Empty(t, b.world.alerts, "a refusal is not a tree the batch did not test")
}

func TestBatchRun_AMemberMergedElsewhereWhileTheBatchRanIsClosedAndTheRestRequeued(t *testing.T) {
	b, cs := newBatchRun(t, 3, threeInApp...)
	b.world.afterMerge = func(ctx context.Context, n int) {
		if n != 1 {
			return
		}
		// Member 2's change is merged by someone else, and the store has heard.
		server, err := ticket.NewServerAgent(b.tickets).EnsureServerAgent(ctx)
		require.NoError(t, err)
		require.NoError(t, b.hostWrap.MarkMerged(ctx, cs[1].ticketID, cs[1].attempt, server))
	}

	b.land(b.runner)

	assert.Equal(t, landing.Merged, b.outcomeOf(cs[0].ticketID).Outcome)
	second := b.outcomesOfTicket(cs[1].ticketID)
	require.Len(t, second, 1)
	assert.Equal(t, landing.AlreadyMerged, second[0].Outcome)
	assert.Nil(t, b.row(cs[1].ticketID), "closed, not left in the queue")
	third := b.outcomesOfTicket(cs[2].ticketID)
	require.NotEmpty(t, third)
	assert.Equal(t, landing.BatchVoided, third[0].Outcome, "the member behind it was requeued with its place")
	assert.Equal(t, landing.Merged, third[len(third)-1].Outcome, "and landed on its own")
	assert.Equal(t, []int64{cs[0].ticketID}, b.world.merges[:1])
	for _, id := range b.world.merges {
		assert.NotEqual(t, cs[1].ticketID, id, "the member merged elsewhere is not merged again")
	}
}

// movesMainWhileTested moves main to a new commit once the batch's CI runs.
// drift, when set, is what the move changed, for the base the batch was built
// on and for each merge commit a member leaves behind; nil leaves the
// comparison unreadable.
func (b *batchRunFixture) movesMainWhileTested(drift ...codehost.FileDiff) {
	moved := "dddddddddddddddddddddddddddddddddddddddd"
	if drift != nil {
		b.host.CommitsOf = map[codehosttest.CommitRange]*codehost.Diff{}
		for _, from := range []string{baseOld, "mergecommit"} {
			b.host.CommitsOf[codehosttest.CommitRange{From: from, To: moved}] = &codehost.Diff{BaseSHA: from, MergeBaseSHA: from, Files: drift}
		}
		b.mergesAdd(moved, codehost.FileDiff{Path: "a.go", Status: codehost.DiffModified, Patch: hunkOne})
	}
	var once sync.Once
	b.world.onBatch = func(string) {
		once.Do(func() {
			b.world.main = moved
			b.host.BranchAt[codehosttest.BranchKey{Repo: b.world.repo, Branch: "main"}] = moved
		})
	}
}

// mergesAdd is what each member's merge on tip adds to it, as the host
// reports it. The fake host numbers its merge commits.
func (b *batchRunFixture) mergesAdd(tip string, files ...codehost.FileDiff) {
	for n := 1; n <= 5; n++ {
		b.host.CommitsOf[codehosttest.CommitRange{From: tip, To: fmt.Sprintf("%040d", n)}] = &codehost.Diff{BaseSHA: tip, MergeBaseSHA: tip, Files: files}
	}
}

func (b *batchRunFixture) requireVoidedBeforeAnythingMerges(cs []*finished) {
	b.t.Helper()
	for _, c := range cs {
		first := b.firstOutcome(c.ticketID)
		assert.Equal(b.t, landing.BatchVoided, first.Outcome)
		assert.True(b.t, first.BatchID != nil)
	}
	assert.GreaterOrEqual(b.t, len(b.world.created), 2, "the members were tested again as a batch of the new tip")
	assert.Contains(b.t, b.firstOutcome(cs[0].ticketID).Detail, "main moved")
	holder := b.row(cs[0].ticketID)
	require.NotNil(b.t, holder)
	assert.Equal(b.t, landing.BackWithAgent, holder.State, "the member the lease was on is handed back, as the lease cannot be let go of otherwise")
	assert.True(b.t, holder.KeepsPlace)
	for _, c := range cs[1:] {
		recs := b.outcomesOfTicket(c.ticketID)
		assert.Equal(b.t, landing.Merged, recs[len(recs)-1].Outcome, "the others landed after")
	}
	for _, id := range b.world.merges {
		assert.NotEqual(b.t, cs[0].ticketID, id, "the first batch merged nothing")
	}
}

func TestBatchRun_MainMovingWhileTheBatchWasTestedVoidsItWhenTheMoveCannotBeRead(t *testing.T) {
	b, cs := newBatchRun(t, 3, threeInApp...)
	b.movesMainWhileTested()

	b.land(b.runner)

	b.requireVoidedBeforeAnythingMerges(cs)
}

func TestBatchRun_MainMovingInTheBatchsOwnAppVoidsIt(t *testing.T) {
	b, cs := newBatchRun(t, 3, threeInApp...)
	b.workspace.setRepo(scope.WorkspaceRepo{Path: "app-a", IndependentApps: []string{"server/", "other/"}, SkipRetestAcrossApps: true})
	b.movesMainWhileTested(codehost.FileDiff{Path: "server/x.go", Status: codehost.DiffModified, Patch: hunkOne})

	b.land(b.runner)

	b.requireVoidedBeforeAnythingMerges(cs)
}

func TestBatchRun_MainMovingOnlyInDeployOnlyFilesDoesNotVoidIt(t *testing.T) {
	b, cs := newBatchRun(t, 3, threeInApp...)
	b.workspace.setRepo(scope.WorkspaceRepo{Path: "app-a", DeployOnly: []scope.DeployOnlyRule{kustomizeRule}})
	b.movesMainWhileTested(codehost.FileDiff{Path: "deploy/test/kustomization.yaml", Status: codehost.DiffModified, Patch: tagBumpPatch})

	b.land(b.runner)

	b.requireBatchMerged(cs)
}

func TestBatchRun_MainMovingOnlyInAnotherIndependentAppDoesNotVoidIt(t *testing.T) {
	b, cs := newBatchRun(t, 3, threeInApp...)
	b.workspace.setRepo(scope.WorkspaceRepo{Path: "app-a", IndependentApps: []string{"server/", "other/"}, SkipRetestAcrossApps: true})
	b.movesMainWhileTested(codehost.FileDiff{Path: "other/main.go", Status: codehost.DiffModified, Patch: hunkOne})

	b.land(b.runner)

	b.requireBatchMerged(cs)
}

func TestBatchRun_ACommitLandingAfterTheGateAcceptedDriftIsAlerted(t *testing.T) {
	b, cs := newBatchRun(t, 2, "app-a/server/", "app-a/server/")
	b.workspace.setRepo(scope.WorkspaceRepo{Path: "app-a", DeployOnly: []scope.DeployOnlyRule{kustomizeRule}})
	b.movesMainWhileTested(codehost.FileDiff{Path: "deploy/test/kustomization.yaml", Status: codehost.DiffModified, Patch: tagBumpPatch})
	// The host's merge holds a file that is neither the member's nor the accepted drift.
	b.mergesAdd("dddddddddddddddddddddddddddddddddddddddd", codehost.FileDiff{Path: "server/late.go", Status: codehost.DiffModified, Patch: hunkOne})

	b.land(b.runner)

	assert.NotEmpty(t, b.world.alerts)
	assert.Equal(t, landing.Merged, b.outcomeOf(cs[0].ticketID).Outcome, "the host's merge stands")
}

func (b *batchRunFixture) requireBatchMerged(cs []*finished) {
	b.t.Helper()
	assert.Empty(b.t, b.world.alerts, "drift the gate allowed is not a tree the batch did not test")
	for _, c := range cs {
		rec := b.outcomeOf(c.ticketID)
		assert.Equal(b.t, landing.Merged, rec.Outcome)
		require.NotNil(b.t, rec.BatchID)
	}
	assert.Len(b.t, b.world.created, 1, "one batch carried them all")
}

func TestBatchRun_ALeaseLostWhileTheBatchIsTestedHandsNothingBackAndTheNextPassRecoversIt(t *testing.T) {
	b, cs := newBatchRun(t, 2, "app-a/server/", "app-a/server/")
	var once sync.Once
	b.world.atBatch = func(ctx context.Context) {
		once.Do(func() {
			b.revokeServerLease()
			<-ctx.Done()
			b.ageBatches()
		})
	}
	b.cfg.HeartbeatInterval = time.Millisecond

	b.land(b.newRunner())

	for _, c := range cs {
		for _, rec := range b.outcomesOfTicket(c.ticketID) {
			assert.Equal(t, landing.Merged, rec.Outcome, "the run that lost the lease handed ticket %d back", c.ticketID)
		}
		assert.NotContains(t, b.comments(c.ticketID), "The server", "or told its author anything")
	}
	live, err := b.store.Live(b.ctx)
	require.NoError(t, err)
	assert.Len(t, live, 1, "the batch was left live: the lease and the rows are the recovery")

	b.ageBatches()
	b.land(b.newRunner())

	live, err = b.store.Live(b.ctx)
	require.NoError(t, err)
	assert.Empty(t, live, "and the next pass ended it")
	assert.Nil(t, b.lockHolder())
}

// allMerged is a batch whose every member has merged and left the queue, the
// state a restart finds when the runner died after the last merge.
func (b *batchRunFixture) allMerged(cs []*finished, withOutcome bool) {
	b.t.Helper()
	b.crashAfterMerge(1)
	for _, c := range cs {
		_, err := b.database.Exec(b.ctx, `DELETE FROM landings WHERE ticket_id = $1`, c.ticketID)
		require.NoError(b.t, err)
	}
	if !withOutcome {
		// The member's own outcome is what names the batch's repository.
		_, err := b.database.Exec(b.ctx, `DELETE FROM server_merge_outcomes`)
		require.NoError(b.t, err)
	}
	b.ageBatches()
}

func TestBatchRecovery_ABatchWhoseMembersAllMergedIsDoneAndItsBranchDeleted(t *testing.T) {
	b, cs := newBatchRun(t, 3, threeInApp...)
	b.allMerged(cs, true)

	b.land(b.newRunner())

	live, err := b.store.Live(b.ctx)
	require.NoError(t, err)
	assert.Empty(t, live, "the batch is done")
	require.Len(t, b.world.created, 1)
	require.NotEmpty(t, b.world.deleted, "and its branch is deleted")
	assert.Contains(t, b.world.created[0], b.world.deleted[0])
	assert.Len(t, b.world.closed, 1, "with its change closed")
}

func TestBatchRecovery_ABatchWhoseMembersAllMergedWithNoOutcomeToNameItsRepositoryIsDoneAndItsBranchLeft(t *testing.T) {
	b, cs := newBatchRun(t, 3, threeInApp...)
	b.allMerged(cs, false)

	b.land(b.newRunner())

	live, err := b.store.Live(b.ctx)
	require.NoError(t, err)
	assert.Empty(t, live, "the batch is not recovered again and again")
	assert.Empty(t, b.world.deleted, "no repository to delete from: the branch is left to the sweep")
}
