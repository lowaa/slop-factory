package landing_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
)

// pendingOnHost makes the batch branch's one check show pending on the host,
// as when the CI's final status never reached it.
func (b *batchRunFixture) pendingOnHost(verdict landing.CheckVerdict) {
	b.pipes.verdicts = map[string]landing.CheckVerdict{stuckURL: verdict}
	b.world.checks = func(head string) *codehost.Checks {
		return codehost.NewChecks(head, "main", []codehost.Check{{Name: "ci/build", State: codehost.CheckPending, TargetURL: stuckURL}})
	}
}

func TestBatchRecovery_ACheckPendingOnTheHostThatTheCISaysPassedCarriesTheBatchOn(t *testing.T) {
	b, cs := newBatchRun(t, 3, "app-a/server/", "app-a/server/", "app-a/server/")
	b.crashAfterMerge(1)
	b.pendingOnHost(landing.CIPassed)

	b.land(b.newRunner())

	assert.Equal(t, ids(cs), b.world.merges, "A once, then B and C on the same run")
	assert.Contains(t, b.pipes.askedAbout(), stuckURL)
	for _, c := range cs {
		assert.Equal(t, landing.Merged, b.outcomeOf(c.ticketID).Outcome)
	}
}

func TestBatchRecovery_ACheckPendingOnTheHostThatTheCIHasNotPassedRequeuesTheBatch(t *testing.T) {
	for _, v := range []landing.CheckVerdict{landing.CIRunning, landing.CIFailed} {
		t.Run(string(v), func(t *testing.T) {
			b, cs := newBatchRun(t, 3, "app-a/server/", "app-a/server/", "app-a/server/")
			b.crashAfterMerge(1)
			b.pendingOnHost(v)

			b.land(b.newRunner())

			assert.Equal(t, []int64{cs[0].ticketID}, b.world.merges[:1])
			voided := b.outcomesOfTicket(cs[2].ticketID)
			require.NotEmpty(t, voided)
			assert.Equal(t, landing.BatchVoided, voided[0].Outcome)
		})
	}
}

func TestBatchRecovery_ACarriedOnBatchAsksToMergeOnTheHeadsItsMembersWereTestedAt(t *testing.T) {
	b, cs := newBatchRun(t, 3, "app-a/server/", "app-a/server/", "app-a/server/")
	updated := "cccccccccccccccccccccccccccccccccccccccc"
	b.world.newHead[cs[1].ticketID] = updated
	b.crashAfterMerge(1)

	b.land(b.newRunner())

	assert.Equal(t, updated, b.world.asked[cs[1].ticketID], "the head the update made, not the enqueued one")
}

// crashWhileTesting runs the batch until the runner is waiting on its CI and
// stops the runner there, as a restart does. The batch's CI keeps running: the
// checks the next runner reads are pending for the first pendingReads reads and
// green after.
func (b *batchRunFixture) crashWhileTesting(pendingReads int64) {
	b.t.Helper()
	waiting := make(chan struct{})
	var once sync.Once
	b.world.atBatch = func(ctx context.Context) {
		once.Do(func() {
			close(waiting)
			<-ctx.Done()
		})
	}
	b.runner.Kick(b.tenant)
	<-waiting
	b.runner.Close()
	b.world.atBatch = nil
	b.ageBatches()
	b.world.checks = func(head string) *codehost.Checks {
		state := codehost.CheckSuccess
		if b.world.reads.Load() <= pendingReads {
			state = codehost.CheckPending
		}
		return codehost.NewChecks(head, "main", []codehost.Check{{Name: "ci/build", State: state}})
	}
}

func TestBatchRecovery_ATestingBatchWaitsOnItsCIAndMergesWhenItGoesGreen(t *testing.T) {
	b, cs := newBatchRun(t, 2, "app-a/server/", "app-a/server/")
	b.crashWhileTesting(b.world.reads.Load() + 3)

	b.land(b.newRunner())

	assert.Equal(t, ids(cs), b.world.merges)
	for _, c := range cs {
		assert.Equal(t, landing.Merged, b.outcomeOf(c.ticketID).Outcome)
		assert.Nil(t, b.row(c.ticketID))
	}
	assert.Len(t, b.world.created, 1, "the batch was not built again")
	assert.Len(t, b.world.closed, 1, "its change is closed once it ends")
	assert.Nil(t, b.lockHolder())
}

func TestBatchRecovery_ATestingBatchWhoseMemberMovedOnIsVoidedAndItsChangeClosed(t *testing.T) {
	b, cs := newBatchRun(t, 2, "app-a/server/", "app-a/server/")
	b.crashWhileTesting(0)
	b.setChecks(cs[1].ref, "cccccccccccccccccccccccccccccccccccccccc", true, codehost.Check{Name: "ci/build", State: codehost.CheckSuccess})

	b.land(b.newRunner())

	assert.Empty(t, b.world.merges)
	voided := b.outcomesOfTicket(cs[1].ticketID)
	require.NotEmpty(t, voided)
	assert.Equal(t, landing.BatchVoided, voided[0].Outcome)
	assert.Contains(t, voided[0].Detail, fmt.Sprintf("#%d", cs[1].ticketID), "the reason names the member")
	require.Len(t, b.world.created, 1)
	branch := strings.SplitN(b.world.created[0], "@", 2)[0]
	assert.Equal(t, []string{branch}, b.world.closed, "the batch's change is closed by the recovery")
	assert.Contains(t, b.world.deleted, branch)
	live, err := b.store.Live(b.ctx)
	require.NoError(t, err)
	assert.Empty(t, live)
}

func TestBatchRecovery_ABatchLeftWhileBuildingHasNoMembersToCloseAndIsVoided(t *testing.T) {
	b, cs := newBatchRun(t, 2, "app-a/server/", "app-a/server/")
	building, err := b.store.Create(b.ctx, baseOld, ids(cs))
	require.NoError(t, err)
	b.ageBatches()

	b.land(b.newRunner())

	got, err := b.store.Get(b.ctx, building.ID)
	require.NoError(t, err)
	assert.Equal(t, landing.BatchDone, got.State)
	var result, reason string
	require.NoError(t, b.database.QueryRow(b.ctx, `SELECT result, result_reason FROM batches WHERE id = $1`, building.ID).Scan(&result, &reason))
	assert.Equal(t, "voided", result, "not merged: no member was reserved, none merged")
	assert.Contains(t, reason, "being built")
	for _, c := range cs {
		assert.Equal(t, landing.Merged, b.outcomeOf(c.ticketID).Outcome, "the members were still queued, and a new batch took them")
	}
}
