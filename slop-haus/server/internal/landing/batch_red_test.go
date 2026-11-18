package landing_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
)

// redBatch makes the batch's run red, whatever it holds.
func (b *batchRunFixture) redBatch() {
	b.world.checks = func(head string) *codehost.Checks {
		return codehost.NewChecks(head, "main", []codehost.Check{red})
	}
	b.pipes.passedOnBase = true
}

// outcomesOfTicket lists every outcome recorded for the ticket, oldest first.
func (b *batchRunFixture) outcomesOfTicket(id int64) []landing.Record {
	b.t.Helper()
	var out []landing.Record
	all := b.outcomesNow()
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].TicketID == id {
			out = append(out, all[i])
		}
	}
	return out
}

func (b *batchRunFixture) retriesOtherThan(head string) []string {
	var out []string
	for _, h := range b.pipes.retried() {
		if h != head {
			out = append(out, h)
		}
	}
	return out
}

func TestBatchRun_ARedBatchIsRetriedOnceThenSplitAndTheCulpritIsHandedBack(t *testing.T) {
	b, cs := newBatchRun(t, 3, "app-a/server/", "app-a/server/", "app-a/server/")
	b.redBatch()
	b.setChecks(cs[1].ref, headA, true, red)

	b.land(b.runner)

	assert.Len(t, b.retriesOtherThan(headA), 1, "the batch's run was retried once, and once only")
	assert.Empty(t, b.world.merges, "nothing merges on the red batch's evidence")
	assert.Equal(t, []int64{1, 3}, b.mergedRefs(), "the innocents landed, in order, each on its own run")
	for _, c := range []*finished{cs[0], cs[2]} {
		rec := b.outcomeOf(c.ticketID)
		assert.Equal(t, landing.Merged, rec.Outcome)
		require.NotNil(t, rec.BatchID)
		assert.Nil(t, b.row(c.ticketID))
		assert.Contains(t, b.told.comments[c.ticketID], "split", "told on the ticket, not handed back")
	}
	culprit := b.outcomeOf(cs[1].ticketID)
	assert.Equal(t, landing.ChecksRed, culprit.Outcome)
	require.NotNil(t, culprit.BatchID)
	assert.Contains(t, culprit.Detail, fmt.Sprintf("batch %d", *culprit.BatchID))
	row := b.row(cs[1].ticketID)
	require.NotNil(t, row)
	assert.Equal(t, landing.BackWithAgent, row.State)
	assert.Contains(t, b.comments(cs[1].ticketID), "ci/build")
	assert.Contains(t, b.comments(cs[1].ticketID), fmt.Sprintf("batch %d", *culprit.BatchID))
	live, err := b.store.Live(b.ctx)
	require.NoError(t, err)
	assert.Empty(t, live, "the batch is done")
	require.Len(t, b.world.created, 1)
	assert.Contains(t, b.world.created[0], b.world.deleted[0], "and its branch is deleted")
}

func TestBatchRun_AFlakeInTheBatchIsRetriedAndNoMemberIsSplit(t *testing.T) {
	b, cs := newBatchRun(t, 2, "app-a/server/", "app-a/server/")
	b.pipes.passedOnBase = true
	reads := 0
	b.world.checks = func(head string) *codehost.Checks {
		reads++
		if reads == 1 {
			return codehost.NewChecks(head, "main", []codehost.Check{red})
		}
		return codehost.NewChecks(head, "main", []codehost.Check{green})
	}

	b.land(b.runner)

	assert.Len(t, b.retriesOtherThan(headA), 1)
	assert.Equal(t, ids(cs), b.world.merges, "merged on the batch's own evidence")
	assert.Empty(t, b.told.comments, "nobody is told of a split that did not happen")
}

func TestBatchRun_ASecondRedRunSplitsTheBatchEvenWhenNoOneIsToBlame(t *testing.T) {
	b, cs := newBatchRun(t, 2, "app-a/server/", "app-a/server/")
	b.redBatch()

	b.land(b.runner)

	assert.Empty(t, b.world.merges, "nothing merged on the batch's evidence")
	assert.Equal(t, []int64{1, 2}, b.mergedRefs(), "both members are green alone and land")
	for _, c := range cs {
		assert.Equal(t, landing.Merged, b.outcomeOf(c.ticketID).Outcome)
	}
}

func TestBatchRun_AMemberGreenAloneButRedBehindTheOnesAheadIsTheInteractionAndTheLaterIsHandedBack(t *testing.T) {
	b, cs := newBatchRun(t, 2, "app-a/server/", "app-a/server/")
	b.redBatch()
	b.host.MergedWith = codehost.Merged{SHA: mergeOfA}
	b.pinBase(cs[0])
	b.pinBase(cs[1])
	// The second is behind the base once the first has landed, and red after the base is merged in.
	b.setChecks(cs[1].ref, headA, false, green)
	b.host.BranchUpdated = codehost.BranchUpdate{SHA: headB}
	b.hostWrap.onUpdate = func(context.Context, string) {
		b.host.DiffOf[headB] = b.diff(baseNew)
		b.setChecks(cs[1].ref, headB, true, red)
	}
	b.pipes.passedOnCommit = map[string]bool{headA: true}

	b.land(b.runner)

	assert.Equal(t, landing.Merged, b.outcomeOf(cs[0].ticketID).Outcome, "the earlier landed")
	later := b.outcomeOf(cs[1].ticketID)
	assert.Equal(t, landing.BatchInteraction, later.Outcome)
	require.NotNil(t, later.BatchID)
	assert.True(t, strings.HasPrefix(later.Detail, fmt.Sprintf("collision cause=interaction predecessors=%d", cs[0].ticketID)), later.Detail)
	text := b.comments(cs[1].ticketID)
	assert.Contains(t, text, fmt.Sprintf("#%d", cs[0].ticketID), "names the earlier member")
	assert.Contains(t, text, b.hostWrapURL(cs[0].ref), "and what it changed, as the collision guidance does")
	assert.Contains(t, text, "green on the base's tip and on your change before the merge")
	assert.Equal(t, landing.BackWithAgent, b.row(cs[1].ticketID).State)
}

func TestBatchRun_ASplitRedThatIsTheMembersOwnBugIsNotAnInteraction(t *testing.T) {
	b, cs := newBatchRun(t, 2, "app-a/server/", "app-a/server/")
	b.redBatch()
	b.host.MergedWith = codehost.Merged{SHA: mergeOfA}
	b.pinBase(cs[0])
	b.pinBase(cs[1])
	b.setChecks(cs[1].ref, headA, false, green)
	b.host.BranchUpdated = codehost.BranchUpdate{SHA: headB}
	b.hostWrap.onUpdate = func(context.Context, string) {
		b.host.DiffOf[headB] = b.diff(baseNew)
		b.setChecks(cs[1].ref, headB, true, red)
	}
	b.pipes.passedOnCommit = map[string]bool{headA: false}

	b.land(b.runner)

	assert.Equal(t, landing.ChecksRed, b.outcomeOf(cs[1].ticketID).Outcome)
}

func TestBatchRun_ADroppedPredecessorVoidsTheEvidenceOfTheMembersBehindIt(t *testing.T) {
	b, cs := newBatchRun(t, 3, "app-a/server/", "app-a/server/", "app-a/server/")
	// The first member's head moved after the batch was built.
	b.setChecks(cs[0].ref, headB, true, green)
	b.host.DiffOf[headB] = b.diff(baseOld)

	b.land(b.runner)

	assert.Equal(t, landing.HeadChanged, b.outcomeOf(cs[0].ticketID).Outcome)
	for _, c := range cs[1:] {
		recs := b.outcomesOfTicket(c.ticketID)
		require.GreaterOrEqual(t, len(recs), 2)
		assert.Equal(t, landing.BatchVoided, recs[0].Outcome, "voided, not handed back for a fault of its own")
		assert.Equal(t, landing.Merged, recs[len(recs)-1].Outcome, "and landed, from the place it kept")
	}
	assert.Equal(t, []int64{cs[1].ticketID, cs[2].ticketID}, b.world.merges)
}

func TestBatchRun_ASplitGivesWayToTheKillSwitch(t *testing.T) {
	b, cs := newBatchRun(t, 2, "app-a/server/", "app-a/server/")
	b.redBatch()
	n := 0
	b.world.onBatch = func(string) {
		if n++; n == 2 {
			b.on.Store(false)
		}
	}

	b.land(b.runner)

	assert.Empty(t, b.host.Merges())
	for _, c := range cs {
		assert.Equal(t, landing.MergeRefused, b.outcomeOf(c.ticketID).Outcome)
	}
}

// crashAfterMerge runs the batch until its nth member has merged on the host
// and stops the runner there, as a restart does: what the first runner wrote
// by then is all the next one has.
func (b *batchRunFixture) crashAfterMerge(n int) {
	b.t.Helper()
	merged := make(chan struct{})
	b.world.afterMerge = func(ctx context.Context, got int) {
		if got == n {
			close(merged)
			<-ctx.Done()
		}
	}
	b.runner.Kick(b.tenant)
	<-merged
	b.runner.Close()
	b.world.afterMerge = nil
	b.ageBatches()
}

// ageBatches makes every batch as still as one a replica has left for longer
// than a lease lives.
func (b *batchRunFixture) ageBatches() {
	b.t.Helper()
	_, err := b.database.Exec(b.ctx, `UPDATE batches SET updated_at = now() - interval '1 hour'`)
	require.NoError(b.t, err)
}

func TestBatchRecovery_AHalfMergedBatchCarriesOnWhenNothingElseLanded(t *testing.T) {
	b, cs := newBatchRun(t, 3, "app-a/server/", "app-a/server/", "app-a/server/")
	b.crashAfterMerge(1)
	require.Equal(t, []int64{cs[0].ticketID}, b.world.merges)

	b.land(b.newRunner())

	assert.Equal(t, ids(cs), b.world.merges, "A once, then B and C: nothing merged twice or skipped")
	assert.Equal(t, []int64{1, 2, 3}, b.mergedRefs())
	for _, c := range cs {
		assert.Equal(t, landing.Merged, b.outcomeOf(c.ticketID).Outcome)
		assert.Nil(t, b.row(c.ticketID))
	}
	live, err := b.store.Live(b.ctx)
	require.NoError(t, err)
	assert.Empty(t, live)
	require.NotEmpty(t, b.world.deleted)
	assert.Contains(t, b.world.created[0], b.world.deleted[len(b.world.deleted)-1], "the batch branch is deleted")
	assert.Nil(t, b.lockHolder())
}

func TestBatchRecovery_AHalfMergedBatchIsRequeuedWhenSomethingElseLandedMeanwhile(t *testing.T) {
	b, cs := newBatchRun(t, 3, "app-a/server/", "app-a/server/", "app-a/server/")
	b.crashAfterMerge(1)
	b.world.main = "dddddddddddddddddddddddddddddddddddddddd"
	b.host.BranchAt[codehosttest.BranchKey{Repo: b.world.repo, Branch: "main"}] = b.world.main

	b.land(b.newRunner())

	assert.Equal(t, []int64{cs[0].ticketID}, b.world.merges[:1], "A is on the base once")
	assert.Equal(t, 1, strings.Count(fmt.Sprint(b.mergedRefs()), "1"), "A was not merged again")
	voided := b.outcomesOfTicket(cs[1].ticketID)
	require.NotEmpty(t, voided)
	assert.Equal(t, landing.BatchVoided, voided[0].Outcome)
	holder := b.row(cs[1].ticketID)
	assert.Equal(t, landing.BackWithAgent, holder.State, "the lease cannot be let go of but by a handback")
	assert.True(t, holder.KeepsPlace)
	assert.Equal(t, landing.BatchVoided, holder.BackReason)
	third := b.outcomesOfTicket(cs[2].ticketID)
	require.NotEmpty(t, third)
	assert.Equal(t, landing.BatchVoided, third[0].Outcome)
	assert.Equal(t, landing.Merged, third[len(third)-1].Outcome, "C kept its place and landed on its own")
	live, err := b.store.Live(b.ctx)
	require.NoError(t, err)
	assert.Empty(t, live)
}

func TestBatchRecovery_AMemberMergedOnTheHostButNotRecordedIsClosedNotMergedAgain(t *testing.T) {
	b, cs := newBatchRun(t, 3, "app-a/server/", "app-a/server/", "app-a/server/")
	b.crashAfterMerge(1)
	// B's merge reached the host, but the runner died before it recorded it.
	b.markMergedOnHost(cs[1])

	b.land(b.newRunner())

	assert.Equal(t, landing.AlreadyMerged, b.outcomeOf(cs[1].ticketID).Outcome)
	assert.Equal(t, []int64{cs[0].ticketID}, b.world.merges[:1])
	assert.Nil(t, b.row(cs[1].ticketID))
	for _, id := range b.world.merges[1:] {
		assert.NotEqual(t, cs[1].ticketID, id, "B was not merged again")
	}
}

func TestBatchRecovery_ABatchHeldByALiveLeaseIsNotTakenOverAndAlertsWhenOld(t *testing.T) {
	b, cs := newBatchRun(t, 2, "app-a/server/", "app-a/server/")
	atCI := make(chan struct{})
	release := make(chan struct{})
	b.world.onBatch = func(string) {
		select {
		case <-atCI:
		default:
			close(atCI)
			<-release
		}
	}
	b.runner.Kick(b.tenant)
	<-atCI
	live, err := b.store.Live(b.ctx)
	require.NoError(t, err)
	require.Len(t, live, 1)

	other := b.newRunner()
	other.Kick(b.tenant)
	other.Wait()
	assert.Empty(t, b.world.alerts, "young: nothing to say")
	_, err = b.database.Exec(b.ctx, `UPDATE batches SET updated_at = now() - interval '5 hours'`)
	require.NoError(t, err)
	other.Kick(b.tenant)
	other.Wait()

	assert.Equal(t, []int64{cs[0].ticketID}, b.world.alerts, "alerted on the batch's first member")
	live, err = b.store.Live(b.ctx)
	require.NoError(t, err)
	assert.Len(t, live, 1, "and left to the runner that holds it")
	assert.Empty(t, b.host.Merges())
	close(release)
	b.runner.Wait()
	assert.Equal(t, ids(cs), b.world.merges, "which finishes it")
}

func TestBatchRecovery_ABranchLeftByAnEndedBatchIsSweptOnStart(t *testing.T) {
	b, _ := newBatchRun(t, 2, "app-a/server/", "app-a/server/")
	b.world.deleteErr = errors.New("host down")
	b.world.otherTree = 1 // ends the batch voided: its branch delete is the one that fails
	b.land(b.runner)
	live, err := b.store.Live(b.ctx)
	require.NoError(t, err)
	require.Empty(t, live, "voided batches are done even when their branch could not be deleted")
	before := len(b.world.deleted)
	require.NotZero(t, before)
	b.world.deleteErr = nil

	b.land(b.newRunner())

	assert.Greater(t, len(b.world.deleted), before, "the stray branch is deleted again")
	assert.Equal(t, b.world.deleted[0], b.world.deleted[before])
}

func TestBatchRecovery_ACarriedOnBatchWhoseRunIsNoLongerGreenIsRequeued(t *testing.T) {
	b, cs := newBatchRun(t, 3, "app-a/server/", "app-a/server/", "app-a/server/")
	b.crashAfterMerge(1)
	b.world.checks = func(head string) *codehost.Checks {
		return codehost.NewChecks(head, "main", []codehost.Check{red})
	}

	b.land(b.newRunner())

	assert.Equal(t, []int64{cs[0].ticketID}, b.world.merges, "nothing more merged on a run that is not green")
	voided := b.outcomesOfTicket(cs[2].ticketID)
	require.NotEmpty(t, voided)
	assert.Equal(t, landing.BatchVoided, voided[0].Outcome)
}

func TestBatchRecovery_ABatchStillForLessThanALeaseIsLeftToItsRunner(t *testing.T) {
	b, cs := newBatchRun(t, 3, "app-a/server/", "app-a/server/", "app-a/server/")
	b.crashAfterMerge(1)
	_, err := b.database.Exec(b.ctx, `UPDATE batches SET updated_at = now()`)
	require.NoError(t, err)
	b.world.checks = func(string) *codehost.Checks { t.Fatal("a batch not yet stale is not looked at"); return nil }

	b.land(b.newRunner())

	live, err := b.store.Live(b.ctx)
	require.NoError(t, err)
	assert.Len(t, live, 1)
	assert.Equal(t, []int64{cs[0].ticketID}, b.world.merges)
}
