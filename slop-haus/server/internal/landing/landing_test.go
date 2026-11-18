package landing_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

var (
	green = codehost.Check{Name: "ci/build", State: codehost.CheckSuccess}
	red   = codehost.Check{Name: "ci/build", State: codehost.CheckFailure}
	pend  = codehost.Check{Name: "ci/build", State: codehost.CheckPending}
)

// handedBack is what every failure leaves: nothing merged, the ticket queued
// for its author with guidance, the lock free, the row marked back with the reason.
func (f *fixture) assertHandedBack(c *finished, want landing.Outcome) {
	f.t.Helper()
	assert.Equal(f.t, want, f.outcomeOf(c.ticketID).Outcome)
	var status string
	require.NoError(f.t, f.database.QueryRow(f.ctx, `SELECT status FROM attempts WHERE ticket_id = $1 AND number = 1`, c.ticketID).Scan(&status))
	assert.Equal(f.t, "queued", status)
	got, err := f.tickets.Get(f.ctx, c.ticketID)
	require.NoError(f.t, err)
	assert.Equal(f.t, ticket.StatusOpen, got.Status, "a handed-back ticket is the author's to claim, not the queue's")
	assert.Nil(f.t, f.lockHolder())
	assert.Contains(f.t, f.comments(c.ticketID), "The server")
	row := f.row(c.ticketID)
	require.NotNil(f.t, row)
	assert.Equal(f.t, landing.BackWithAgent, row.State)
	assert.Equal(f.t, want, row.BackReason)
}

func (f *fixture) assertMerged(c *finished) {
	f.t.Helper()
	assert.Equal(f.t, landing.Merged, f.outcomeOf(c.ticketID).Outcome)
	got, err := f.tickets.Get(f.ctx, c.ticketID)
	require.NoError(f.t, err)
	assert.Equal(f.t, ticket.StatusDone, got.Status)
	assert.Nil(f.t, f.row(c.ticketID), "a landed ticket leaves the queue")
	assert.Nil(f.t, f.lockHolder())
}

func TestLand_MergesAGreenCurrentChangeWithoutMergingMain(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)

	f.land(f.runner(landing.Config{}))

	f.assertMerged(c)
	require.Len(t, f.host.Merges(), 1)
	assert.Equal(t, headA, f.host.Merges()[0].Merge.HeadSHA)
	assert.Zero(t, f.hostWrap.updates.Load(), "a head that is current is not given a CI run for nothing")
}

func TestLand_AHeadBehindMainTakesMainInWaitsForCIAndMerges(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, false, green)
	f.host.BranchUpdated = codehost.BranchUpdate{SHA: headB}
	f.hostWrap.onUpdate = func(context.Context, string) {
		f.host.DiffOf[headB] = f.diff(baseNew)
		f.setChecks(c.ref, headB, true, pend)
	}
	reads := 0
	f.host.AfterChecks = func() {
		if reads++; reads >= 6 {
			f.setChecks(c.ref, headB, true, green)
		}
	}

	f.land(f.runner(landing.Config{}))

	f.assertMerged(c)
	assert.EqualValues(t, 1, f.hostWrap.updates.Load())
	require.Len(t, f.host.Merges(), 1)
	assert.Equal(t, headB, f.host.Merges()[0].Merge.HeadSHA, "what merges is the head CI ran on")
	assert.Equal(t, []string{headA}, []string{f.host.BranchUpdates()[0].Update.HeadSHA})
}

func TestLand_AConflictMergingMainIsHandedBackWithoutCI(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, false, green)
	f.host.BranchUpdateErr = codehost.ErrMergeConflict

	f.land(f.runner(landing.Config{}))

	f.assertHandedBack(c, landing.MergeMainConflict)
	assert.Empty(t, f.host.Merges())
	assert.Contains(t, f.comments(c.ticketID), "origin/<base>")
}

func TestLand_BaseMovingAgainAfterMergeMainIsHandedBackNotLooped(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, false, green)
	f.host.BranchUpdated = codehost.BranchUpdate{SHA: headB}
	f.hostWrap.onUpdate = func(context.Context, string) {
		f.host.DiffOf[headB] = f.diff(baseNew)
		f.setChecks(c.ref, headB, false, green) // main moved on once more
	}

	f.land(f.runner(landing.Config{}))

	f.assertHandedBack(c, landing.MainMoved)
	assert.EqualValues(t, 1, f.hostWrap.updates.Load())
	assert.Empty(t, f.host.Merges())
}

func TestLand_RedChecksAreHandedBackWithTheCheckNames(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, true, red)

	f.land(f.runner(landing.Config{}))

	f.assertHandedBack(c, landing.ChecksRed)
	assert.Empty(t, f.host.Merges())
	assert.Empty(t, f.pipes.retried(), "a failure nobody knows to be flaky is not run again")
	assert.Contains(t, f.comments(c.ticketID), "ci/build")
}

func TestLand_AKnownFlakyCheckIsRetriedOnceAndThenLands(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, true, red)
	f.pipes.newest = func(string) *landing.Pipeline {
		if len(f.pipes.retries) == 0 {
			return &landing.Pipeline{ID: 1, State: landing.PipelineFailure}
		}
		return &landing.Pipeline{ID: 2, State: landing.PipelineRunning}
	}
	reads := 0
	f.host.AfterChecks = func() {
		// The host still shows the first pipeline's failure for a few reads
		// after the retry: that is not a second failure.
		if len(f.pipes.retried()) > 0 {
			if reads++; reads >= 4 {
				f.setChecks(c.ref, headA, true, green)
			}
		}
	}

	f.land(f.runner(landing.Config{KnownFlaky: []string{"ci/build"}}))

	f.assertMerged(c)
	assert.Equal(t, []string{headA}, f.pipes.retried())
}

func TestLand_AKnownFlakyWorkflowNameMatchesTheHostsCheckName(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, true, red)
	f.pipes.newest = func(string) *landing.Pipeline {
		return &landing.Pipeline{ID: int64(1 + len(f.pipes.retries)), State: landing.PipelineFailure}
	}

	f.land(f.runner(landing.Config{KnownFlaky: []string{"build"}}))

	assert.Len(t, f.pipes.retried(), 1, "the workflow name is the check's last segment")
}

func TestLand_AFlakyCheckThatFailsAgainIsHandedBackAfterOneRetry(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, true, red)
	f.pipes.newest = func(string) *landing.Pipeline {
		return &landing.Pipeline{ID: int64(1 + len(f.pipes.retries)), State: landing.PipelineFailure}
	}

	f.land(f.runner(landing.Config{KnownFlaky: []string{"ci/build"}}))

	f.assertHandedBack(c, landing.ChecksRed)
	assert.Len(t, f.pipes.retried(), 1, "retried once, never in a loop")
	assert.Contains(t, f.comments(c.ticketID), "retried them once")
}

func TestLand_ACheckThatPassedOnMainsTipIsRetried(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, true, red)
	f.pipes.passedOnBase = true

	f.land(f.runner(landing.Config{}))

	f.assertHandedBack(c, landing.ChecksRed)
	assert.Len(t, f.pipes.retried(), 1)
}

// The watchdog cancels a pipeline stuck past the repo timeout and restarts it
// as a new one on the same commit. The host shows the cancelled one as a
// failure until the restart reports; following the first pipeline would hand
// back a ticket the watchdog was about to rescue.
func TestLand_TheCIWaitFollowsTheNewestPipelineThroughAWatchdogRestart(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, true, red)
	now := time.Now()
	reads := 0
	f.pipes.newest = func(string) *landing.Pipeline {
		switch {
		case reads < 2:
			return &landing.Pipeline{ID: 1, State: landing.PipelineCancelled, Created: now.Add(-2 * time.Hour), Finished: now}
		default:
			return &landing.Pipeline{ID: 2, State: landing.PipelineRunning, Created: now}
		}
	}
	f.host.AfterChecks = func() {
		if reads++; reads >= 5 {
			f.pipes.mu.Lock()
			f.pipes.newest = func(string) *landing.Pipeline {
				return &landing.Pipeline{ID: 2, State: landing.PipelineSuccess, Created: now, Finished: now}
			}
			f.pipes.mu.Unlock()
			f.setChecks(c.ref, headA, true, green)
		}
	}

	f.land(f.runner(landing.Config{}))

	f.assertMerged(c)
	assert.Empty(t, f.pipes.retried())
}

// The 26 Oct stall: pipelines orphaned by a Woodpecker restart never finish and
// the checks stay pending for ever. The wait ends at the repo timeout plus the
// margin, the pipeline is cancelled and restarted once, and the restart's own
// bound ends in a handback, so the queue moves on and a re-queue is not left
// waiting on the same dead pipeline.
func TestLand_OrphanedPipelinesEndInAPendingTimeoutHandbackNotAFrozenQueue(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, true, pend)
	f.pipes.timeout = 30 * time.Minute
	f.pipes.newest = func(string) *landing.Pipeline {
		return &landing.Pipeline{ID: 7, State: landing.PipelineRunning, Created: time.Now()}
	}
	fake := clock.NewFake(time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC))
	f.clock = fake
	start := fake.Now()
	last := f.landPerRead(f.runner(landing.Config{RestartMargin: 15 * time.Minute, PollInterval: time.Minute}), fake)

	f.assertHandedBack(c, landing.ChecksPendingTimeout)
	assert.GreaterOrEqual(t, last.Sub(start), 89*time.Minute, "it gave the restarted pipeline its whole bound")
	assert.LessOrEqual(t, last.Sub(start), 95*time.Minute, "and it did not wait on a pipeline that never ends")
	assert.Equal(t, []int64{7}, f.pipes.restarted(), "the lost pipeline was restarted once, not on every wait")
	assert.Empty(t, f.host.Merges())
	assert.True(t, f.row(c.ticketID).KeepsPlace, "a CI stall is not the author's fault")
}

// The 29 Oct stall: a head whose pipeline never started has no checks at all,
// which is not CI running. It is handed back after the short bound, not the CI
// bound, so the queue moves on.
func TestLand_AHeadWithNoChecksIsHandedBackAsNeverStartedAfterTheShortBound(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, true)
	f.pipes.timeout = 30 * time.Minute
	fake := clock.NewFake(time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC))
	f.clock = fake
	start := fake.Now()
	last := f.landPerRead(f.runner(landing.Config{NeverStartedWait: 5 * time.Minute, NoPipelineWait: 5 * time.Minute, PollInterval: time.Minute}), fake)

	f.assertHandedBack(c, landing.ChecksNeverStarted)
	assert.GreaterOrEqual(t, last.Sub(start), 4*time.Minute)
	assert.Less(t, last.Sub(start), 10*time.Minute, "it did not wait for the CI bound")
	assert.Empty(t, f.host.Merges())
	assert.True(t, f.row(c.ticketID).KeepsPlace, "CI not starting is not the author's fault")
}

func TestLand_ChecksThatAppearBeforeTheShortBoundAreWaitedForAsUsual(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, true)
	polls := 0
	f.pipes.newest = func(string) *landing.Pipeline {
		if polls++; polls == 4 {
			f.setChecks(c.ref, headA, true, green)
		}
		return nil
	}
	fake := clock.NewFake(time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC))
	f.clock = fake

	f.landPerRead(f.runner(landing.Config{NeverStartedWait: 5 * time.Minute, NoPipelineWait: 5 * time.Minute, PollInterval: time.Minute}), fake)

	f.assertMerged(c)
}

func TestLand_NeutralOnlyChecksMeanCIStartedSoTheyAreNotNeverStarted(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, true, codehost.Check{Name: "ci/lint", State: codehost.CheckNeutral})
	f.pipes.timeout = 30 * time.Minute
	fake := clock.NewFake(time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC))
	f.clock = fake

	f.landPerRead(f.runner(landing.Config{NeverStartedWait: 5 * time.Minute, RestartMargin: 15 * time.Minute, PollInterval: time.Minute}), fake)

	f.assertHandedBack(c, landing.ChecksPendingTimeout)
}

func TestLand_PendingChecksAreNotNeverStartedWhateverTheShortBound(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, true, pend)
	f.pipes.timeout = 30 * time.Minute
	fake := clock.NewFake(time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC))
	f.clock = fake
	start := fake.Now()
	last := f.landPerRead(f.runner(landing.Config{NeverStartedWait: 5 * time.Minute, RestartMargin: 15 * time.Minute, PollInterval: time.Minute}), fake)

	f.assertHandedBack(c, landing.ChecksPendingTimeout)
	assert.GreaterOrEqual(t, last.Sub(start), 44*time.Minute)
}

func TestLand_ACancelledPipelineThatIsNeverRestartedIsAPendingTimeoutNotRed(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, true, red)
	fake := clock.NewFake(time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC))
	f.clock = fake
	f.pipes.newest = func(string) *landing.Pipeline {
		return &landing.Pipeline{ID: 1, State: landing.PipelineCancelled, Created: fake.Now().Add(-time.Hour), Finished: fake.Now().Add(-time.Minute)}
	}

	f.landAdvancing(f.runner(landing.Config{RestartMargin: 10 * time.Minute, PollInterval: time.Minute}), fake, time.Minute)

	f.assertHandedBack(c, landing.ChecksPendingTimeout)
}

func TestLand_ACodeHostErrorIsTriedAgainThenHandedBackKeepingItsPlace(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.host.ChecksErr = errors.New("boom: secret-token-in-body")

	f.land(f.runner(landing.Config{HostRetries: 3}))

	f.assertHandedBack(c, landing.CodeHostError)
	row := f.row(c.ticketID)
	assert.True(t, row.KeepsPlace)
	assert.GreaterOrEqual(t, len(f.host.Checked()), 3, "tried again")
	assert.NotContains(t, f.comments(c.ticketID), "secret-token", "the host's own words are not shown")
}

func TestLand_AHeadThatChangedAfterTheApprovalIsHandedBack(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headB, true, green) // pushed after approval; no diff recorded to compare

	f.land(f.runner(landing.Config{}))

	f.assertHandedBack(c, landing.HeadChanged)
	assert.Empty(t, f.host.Merges())
}

func TestLand_AChangeAlreadyMergedOnTheHostIsClosedNotMergedAgain(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, false, green)
	// Head is in the base on the host, though the store never heard.
	f.markMergedOnHost(c)

	f.land(f.runner(landing.Config{}))

	assert.Equal(t, landing.AlreadyMerged, f.outcomeOf(c.ticketID).Outcome)
	got, err := f.tickets.Get(f.ctx, c.ticketID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDone, got.Status)
	assert.Empty(t, f.host.Merges())
	assert.Zero(t, f.hostWrap.updates.Load())
	assert.Nil(t, f.row(c.ticketID))
	assert.Nil(t, f.lockHolder())
}

func TestRun_SweepsEveryTenantsQueue(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	r := f.runner(landing.Config{SweepInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	require.Eventually(t, func() bool { return f.row(c.ticketID) == nil }, 30*time.Second, time.Millisecond)
	cancel()
	<-done
	r.Wait()
	f.assertMerged(c)
}

func TestLand_ACancellationGraceThatHasPassedIsAPendingTimeout(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, true, red)
	f.pipes.newest = func(string) *landing.Pipeline {
		return &landing.Pipeline{ID: 1, State: landing.PipelineCancelled, Created: time.Now().Add(-2 * time.Hour), Finished: time.Now().Add(-time.Hour)}
	}

	f.land(f.runner(landing.Config{RestartMargin: 10 * time.Minute}))

	f.assertHandedBack(c, landing.ChecksPendingTimeout)
	assert.Contains(t, f.comments(c.ticketID), "not restarted")
}

// A restart is a new pipeline with its own bound: the wait is extended once, to
// the restart's creation plus the bound, and no further.
func TestLand_AWatchdogRestartExtendsTheBoundOnce(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, true, pend)
	f.pipes.timeout = 30 * time.Minute
	fake := clock.NewFake(time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC))
	f.clock = fake
	start := fake.Now()
	f.pipes.newest = func(string) *landing.Pipeline {
		if fake.Now().Before(start.Add(40 * time.Minute)) {
			return &landing.Pipeline{ID: 1, State: landing.PipelineRunning, Created: start}
		}
		return &landing.Pipeline{ID: 2, State: landing.PipelineRunning, Created: start.Add(40 * time.Minute)}
	}

	last := f.landPerRead(f.runner(landing.Config{RestartMargin: 15 * time.Minute, PollInterval: time.Minute}), fake)

	f.assertHandedBack(c, landing.ChecksPendingTimeout)
	assert.GreaterOrEqual(t, last.Sub(start), 84*time.Minute, "the restart got its own bound (40m + 30m + 15m)")
	assert.LessOrEqual(t, last.Sub(start), 90*time.Minute)
}

// A pending update may still land, so it is read for and not asked for twice.
func TestLand_APendingBranchUpdateIsNotAskedForAgain(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, false, green)
	f.host.BranchUpdateErr = codehost.ErrUpdatePending
	f.hostWrap.onUpdate = func(context.Context, string) {
		f.host.DiffOf[headB] = f.diff(baseNew)
		f.setChecks(c.ref, headB, true, green) // the update landed after all
	}

	f.land(f.runner(landing.Config{HostRetries: 3}))

	f.assertMerged(c)
	assert.EqualValues(t, 1, f.hostWrap.updates.Load())
	assert.Equal(t, headB, f.host.Merges()[0].Merge.HeadSHA)
}

// A host that accepts the update and never applies it costs the short update
// bound twice, not the CI bound.
func TestLand_AnUpdateTheHostNeverAppliesIsAskedForOnceMoreThenHandedBack(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, false, green)
	f.host.BranchUpdateErr = codehost.ErrUpdatePending
	f.pipes.timeout = 2 * time.Hour
	fake := clock.NewFake(time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC))
	f.clock = fake
	start := fake.Now()

	last := f.landPerRead(f.runner(landing.Config{UpdateWait: 3 * time.Minute, PollInterval: time.Minute}), fake)

	f.assertHandedBack(c, landing.CodeHostError)
	assert.EqualValues(t, 2, f.hostWrap.updates.Load())
	assert.GreaterOrEqual(t, last.Sub(start), 5*time.Minute, "the first ask is waited out before the second, and the second too")
	assert.LessOrEqual(t, last.Sub(start), 8*time.Minute, "two update bounds, nowhere near the CI bound")
	assert.Empty(t, f.host.Merges())
}

// An update that lands only after the first ask has been waited out is found by
// the second wait, and the landing goes on.
func TestLand_AnUpdateThatLandsAfterTheReaskProceeds(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, false, green)
	f.host.BranchUpdateErr = codehost.ErrUpdatePending
	var firstAsk time.Time
	f.hostWrap.onUpdate = func(context.Context, string) {
		if f.hostWrap.updates.Load() == 1 {
			firstAsk = time.Now()
			return
		}
		assert.GreaterOrEqual(t, time.Since(firstAsk), 50*time.Millisecond, "the first ask is waited out before the second")
		// The update lands as the second ask arrives, which then finds the head moved.
		f.host.DiffOf[headB] = f.diff(baseNew)
		f.setChecks(c.ref, headB, true, green)
		f.host.BranchUpdateErr = codehost.ErrHeadMoved
	}

	f.land(f.runner(landing.Config{UpdateWait: 50 * time.Millisecond}))

	f.assertMerged(c)
	assert.EqualValues(t, 2, f.hostWrap.updates.Load())
	assert.Equal(t, headB, f.host.Merges()[0].Merge.HeadSHA)
}

// A change that is on the base is never handed back as a failure because the
// bookkeeping after it failed.
func TestLand_AMergedChangeWhoseTicketCannotBeClosedIsNotHandedBack(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, false, green)
	f.markMergedOnHost(c)
	f.doneErr = errors.New("db down")

	f.land(f.runner(landing.Config{LeaseTTL: time.Hour}))

	row := f.row(c.ticketID)
	require.NotNil(t, row)
	assert.Equal(t, landing.Landing, row.State, "still queued for the next sweep to close")
	assert.Empty(t, f.outcomesNow())
	assert.NotContains(t, f.comments(c.ticketID), "The server")
}

func TestLand_RecordsHowManyWereWaitingWhenTheHeadWasTaken(t *testing.T) {
	f := newFixture(t)
	first := f.finishedChange(model.PriorityUrgent)
	second := f.finishedChange(model.PriorityNormal)
	third := f.finishedChange(model.PriorityLow)

	f.land(f.runner(landing.Config{}))

	for c, want := range map[*finished]int{first: 2, second: 1, third: 0} {
		f.assertMerged(c)
		got := f.outcomeOf(c.ticketID).QueueDepth
		require.NotNil(t, got)
		assert.Equal(t, want, *got)
	}
}

func TestLand_TellsTheObserverOfTheOutcomeOnceItIsRecorded(t *testing.T) {
	f := newFixture(t)
	obs := &recordingObserver{}
	f.observer = obs
	first := f.finishedChange(model.PriorityUrgent)
	f.finishedChange(model.PriorityNormal)

	f.land(f.runner(landing.Config{}))

	f.assertMerged(first)
	require.Len(t, obs.got, 2)
	assert.Equal(t, "merged", obs.got[0].outcome)
	assert.Equal(t, 1, obs.got[0].depth)
	assert.Equal(t, 0, obs.got[1].depth)
	assert.Equal(t, 1, obs.got[0].batchSize, "a serial landing is a batch of one")
	rec := f.outcomeOf(first.ticketID)
	require.NotNil(t, rec.EnqueuedAt, "the report queries need the enqueue time")
}

func TestLand_TellsTheObserverOfAHandBackToo(t *testing.T) {
	f := newFixture(t)
	obs := &recordingObserver{}
	f.observer = obs
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, true, red)

	f.land(f.runner(landing.Config{}))

	f.assertHandedBack(c, landing.ChecksRed)
	require.Len(t, obs.got, 1)
	assert.Equal(t, "checks_red", obs.got[0].outcome)
}

func TestLand_DoesNotTellTheObserverOfAnOutcomeItCouldNotRecord(t *testing.T) {
	f := newFixture(t)
	obs := &recordingObserver{}
	f.observer = obs
	f.recorder = failingOutcomes{}
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, true, red)

	f.land(f.runner(landing.Config{}))

	assert.Empty(t, obs.got, "the series must agree with the table the report queries read")
}

func (f *fixture) statusOf(id int64) (ticketStatus, attemptStatus string) {
	f.t.Helper()
	require.NoError(f.t, f.database.QueryRow(f.ctx, `SELECT t.status, a.status FROM tickets t JOIN attempts a ON a.ticket_id = t.id AND a.number = 1 WHERE t.id = $1`, id).
		Scan(&ticketStatus, &attemptStatus))
	return ticketStatus, attemptStatus
}

func TestLand_TheTicketReadsInMergeQueueFromTheRowToTheMergeOrTheHandBack(t *testing.T) {
	t.Run("merged", func(t *testing.T) {
		f := newFixture(t)
		c := f.finishedChange(model.PriorityNormal)
		_, err := f.store.RepairMergeQueue(f.ctx, 0)
		require.NoError(t, err)
		ts, as := f.statusOf(c.ticketID)
		assert.Equal(t, "in_merge_queue", ts)
		assert.Equal(t, "in_merge_queue", as)

		f.land(f.runner(landing.Config{}))
		f.assertMerged(c)
	})
	t.Run("handed back", func(t *testing.T) {
		f := newFixture(t)
		c := f.finishedChange(model.PriorityNormal)
		f.setChecks(c.ref, headA, true, red)
		_, err := f.store.RepairMergeQueue(f.ctx, 0)
		require.NoError(t, err)

		f.land(f.runner(landing.Config{}))

		f.assertHandedBack(c, landing.ChecksRed)
		var stays int
		require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM ticket_status_changes WHERE ticket_id = $1 AND (to_status = 'in_merge_queue' OR from_status = 'in_merge_queue')`, c.ticketID).Scan(&stays))
		assert.Equal(t, 2, stays, "into the queue, and out of it on the hand-back")
	})
}

const stuckURL = "https://ci.example/repos/7/pipeline/11/9"

// The 29 Oct stall: Woodpecker finished the workflow, but its status never
// reached the host, which kept showing it pending.
func stuckPending(f *fixture, c *finished, verdict landing.CheckVerdict, url string) (*clock.Fake, *landing.Runner) {
	f.setChecks(c.ref, headA, true, codehost.Check{Name: "ci/build", State: codehost.CheckPending, TargetURL: url})
	if verdict != "" {
		f.pipes.verdicts = map[string]landing.CheckVerdict{url: verdict}
	}
	fake := clock.NewFake(time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC))
	f.clock = fake
	return fake, f.runner(landing.Config{PollInterval: time.Millisecond})
}

func TestLand_ACheckPendingOnTheHostThatTheCISaysPassedMergesAfterTheShortBound(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	fake, r := stuckPending(f, c, landing.CIPassed, stuckURL)
	start := fake.Now()

	f.landPerRead(r, fake)

	f.assertMerged(c)
	require.Len(t, f.host.Merges(), 1)
	assert.Equal(t, headA, f.host.Merges()[0].Merge.HeadSHA)
	assert.Contains(t, f.outcomeOf(c.ticketID).Detail, "ci/build")
	assert.GreaterOrEqual(t, fake.Now().Sub(start), 10*time.Minute, "the CI is not asked before the short bound")
}

func TestLand_ACheckPendingOnTheHostThatTheCISaysFailedIsRed(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	fake, r := stuckPending(f, c, landing.CIFailed, stuckURL)

	f.landPerRead(r, fake)

	f.assertHandedBack(c, landing.ChecksRed)
	assert.Empty(t, f.host.Merges())
}

func TestLand_ACheckThatTheCISaysIsStillRunningKeepsWaiting(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	fake, r := stuckPending(f, c, landing.CIRunning, stuckURL)
	f.pipes.timeout = 30 * time.Minute

	start := fake.Now()

	f.landPerRead(r, fake)

	f.assertHandedBack(c, landing.ChecksPendingTimeout)
	assert.Empty(t, f.host.Merges())
	assert.GreaterOrEqual(t, fake.Now().Sub(start), 30*time.Minute, "a check the CI does not vouch for is waited out")
	assert.NotEmpty(t, f.pipes.askedAbout())
}

func TestLand_APendingCheckThatIsNotTheCIsIsWaitedForAsToday(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	fake, r := stuckPending(f, c, "", "https://other-ci.example/build/1")
	f.pipes.timeout = 30 * time.Minute

	start := fake.Now()

	f.landPerRead(r, fake)

	f.assertHandedBack(c, landing.ChecksPendingTimeout)
	assert.Empty(t, f.host.Merges())
	assert.GreaterOrEqual(t, fake.Now().Sub(start), 30*time.Minute, "a check the CI does not vouch for is waited out")
}

func TestLand_OnePendingCheckTheCIDoesNotVouchForKeepsTheOthersFromMerging(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, true,
		codehost.Check{Name: "ci/build", State: codehost.CheckPending, TargetURL: stuckURL},
		codehost.Check{Name: "ext/scan", State: codehost.CheckPending, TargetURL: "https://other-ci.example/1"})
	f.pipes.verdicts = map[string]landing.CheckVerdict{stuckURL: landing.CIPassed}
	f.pipes.timeout = 30 * time.Minute
	fake := clock.NewFake(time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC))
	f.clock = fake

	f.landPerRead(f.runner(landing.Config{PollInterval: time.Millisecond}), fake)

	f.assertHandedBack(c, landing.ChecksPendingTimeout)
	assert.Empty(t, f.host.Merges())
}

func TestLand_ACIPassOfOneURLDoesNotCoverTheSameCheckRerunElsewhere(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	rerun := codehost.Check{Name: "ci/build", State: codehost.CheckPending, TargetURL: "https://ci.example/repos/7/pipeline/12/9"}
	f.pipes.timeout = 30 * time.Minute
	f.wrapChanges = func(inner landing.Changes) landing.Changes {
		return &rerunBeforeMerge{Changes: inner, f: f, ref: c.ref, check: rerun}
	}
	fake, r := stuckPending(f, c, landing.CIPassed, stuckURL)

	f.landPerRead(r, fake)

	assert.Empty(t, f.host.Merges(), "the CI vouched for the first run, not the rerun")
}

// rerunBeforeMerge moves the host's pending check to another URL just before
// the merge, as a rerun of the workflow would.
type rerunBeforeMerge struct {
	landing.Changes
	f     *fixture
	ref   codehost.ChangeRef
	check codehost.Check
}

func (r *rerunBeforeMerge) Merge(ctx context.Context, tenant, agent uuid.UUID, in *change.MergeInput) (*change.Merged, error) {
	r.f.setChecks(r.ref, headA, true, r.check)
	return r.Changes.Merge(ctx, tenant, agent, in)
}

const (
	failedURL  = "https://ci.example/repos/7/pipeline/11/3"
	runningURL = "https://ci.example/repos/7/pipeline/11/4"
)

// One workflow failed while another still runs: the pipeline is not waited out.
func earlyRed(f *fixture, c *finished, pipeID int64) (*clock.Fake, *landing.Runner) {
	f.setChecks(c.ref, headA, true,
		codehost.Check{Name: "ci/frontend-lint", State: codehost.CheckFailure, TargetURL: failedURL},
		codehost.Check{Name: "ci/build", State: codehost.CheckPending, TargetURL: runningURL})
	f.pipes.newest = func(string) *landing.Pipeline {
		return &landing.Pipeline{ID: pipeID, State: landing.PipelineRunning, Created: time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)}
	}
	f.pipes.timeout = 30 * time.Minute
	fake := clock.NewFake(time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC))
	f.clock = fake
	return fake, f.runner(landing.Config{PollInterval: time.Millisecond})
}

func TestLand_AFailedWorkflowThatWillNotBeRetriedIsRedWhileOthersStillRun(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	fake, r := earlyRed(f, c, 11)
	start := fake.Now()

	f.landPerRead(r, fake)

	f.assertHandedBack(c, landing.ChecksRed)
	assert.Empty(t, f.host.Merges())
	assert.Less(t, fake.Now().Sub(start), time.Minute, "the running workflows are not waited for")
}

func TestLand_AFailedWorkflowOfAnOlderPipelineIsNotRedWhileTheNewestRuns(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	fake, r := earlyRed(f, c, 12)
	start := fake.Now()

	f.landPerRead(r, fake)

	f.assertHandedBack(c, landing.ChecksPendingTimeout)
	assert.GreaterOrEqual(t, fake.Now().Sub(start), 30*time.Minute)
}

func TestLand_AFailedWorkflowThatWillBeRetriedWaitsForTheRunningPipeline(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	fake, r := earlyRed(f, c, 11)
	f.pipes.passedOnBase = true
	start := fake.Now()

	f.landPerRead(r, fake)

	f.assertHandedBack(c, landing.ChecksPendingTimeout)
	assert.Empty(t, f.pipes.retried(), "a running pipeline is not retried")
	assert.GreaterOrEqual(t, fake.Now().Sub(start), 30*time.Minute)
}

// The 30 and 31 Oct stalls: the server's own merge of main got a push event
// and no pull_request event, so the CI had no pipeline for it. The change is not
// at fault, so main is merged again (a new commit, a new event) rather than the
// change handed back.
const headC = "cccccccccccccccccccccccccccccccccccccccc"

// mergeMainTwice sets up a head behind main whose first merge of main (headB)
// gets no pipeline. second says what the next merge of main does.
func (f *fixture) mergeMainTwice(c *finished, second func()) {
	f.setChecks(c.ref, headA, false, green)
	f.host.BranchUpdated = codehost.BranchUpdate{SHA: headB}
	f.hostWrap.onUpdate = func(context.Context, string) {
		switch f.hostWrap.updates.Load() {
		case 1:
			f.host.DiffOf[headB] = f.diff(baseNew)
			f.setChecks(c.ref, headB, true)
		case 2:
			second()
		}
	}
}

func (f *fixture) outcomeSequence(id int64) []landing.Outcome {
	var out []landing.Outcome
	for _, r := range f.outcomesNow() {
		if r.TicketID == id {
			out = append(out, r.Outcome)
		}
	}
	slices.Reverse(out)
	return out
}

func TestLand_AMergeOfMainThatGetsNoPipelineIsMergedAgainOnceAndLands(t *testing.T) {
	f := newFixture(t)
	obs := &recordingObserver{}
	f.observer = obs
	c := f.finishedChange(model.PriorityNormal)
	f.mergeMainTwice(c, func() {
		f.host.BranchUpdated = codehost.BranchUpdate{SHA: headC}
		f.host.DiffOf[headC] = f.diff(baseNew)
		f.setChecks(c.ref, headC, true, green)
	})
	fake := clock.NewFake(time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC))
	f.clock = fake
	start := fake.Now()

	last := f.landPerRead(f.runner(landing.Config{NoPipelineWait: 3 * time.Minute, NeverStartedWait: 10 * time.Minute, PollInterval: time.Minute}), fake)

	f.assertMerged(c)
	assert.EqualValues(t, 2, f.hostWrap.updates.Load())
	require.Len(t, f.host.Merges(), 1)
	assert.Equal(t, headC, f.host.Merges()[0].Merge.HeadSHA, "what merges is the head that got CI")
	assert.Equal(t, []landing.Outcome{landing.CIEventMissingRequeued, landing.Merged}, f.outcomeSequence(c.ticketID))
	require.Len(t, obs.got, 1, "the observer counts the landing, not the retry inside it")
	assert.Equal(t, "merged", obs.got[0].outcome)
	assert.Less(t, last.Sub(start), 10*time.Minute, "it did not wait for the long bound")
}

func TestLand_AMergeOfMainThatGetsNoPipelineTwiceIsHandedBack(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.mergeMainTwice(c, func() {
		f.host.BranchUpdated = codehost.BranchUpdate{SHA: headC}
		f.host.DiffOf[headC] = f.diff(baseNew)
		f.setChecks(c.ref, headC, true)
	})
	fake := clock.NewFake(time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC))
	f.clock = fake

	f.landPerRead(f.runner(landing.Config{NoPipelineWait: 3 * time.Minute, NeverStartedWait: 10 * time.Minute, PollInterval: time.Minute}), fake)

	f.assertHandedBack(c, landing.ChecksNeverStarted)
	assert.EqualValues(t, 2, f.hostWrap.updates.Load(), "main is merged again once, not in a loop")
	assert.Empty(t, f.host.Merges())
	assert.Equal(t, []landing.Outcome{landing.CIEventMissingRequeued, landing.ChecksNeverStarted}, f.outcomeSequence(c.ticketID))
}

func TestLand_NothingNewToMergeAfterAMissingPipelineIsHandedBackAsNeverStarted(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.mergeMainTwice(c, func() { f.host.BranchUpdated = codehost.BranchUpdate{UpToDate: true} })
	fake := clock.NewFake(time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC))
	f.clock = fake

	f.landPerRead(f.runner(landing.Config{NoPipelineWait: 3 * time.Minute, NeverStartedWait: 10 * time.Minute, PollInterval: time.Minute}), fake)

	f.assertHandedBack(c, landing.ChecksNeverStarted)
	assert.Contains(t, f.comments(c.ticketID), "Woodpecker has no pipeline")
}

func TestLand_APipelineThatExistsButReportsNoCheckIsNotMergedAgainEarly(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.mergeMainTwice(c, func() { t.Error("main was merged again though a pipeline exists") })
	f.pipes.newest = func(head string) *landing.Pipeline {
		if head == headB {
			return &landing.Pipeline{ID: 7, State: landing.PipelinePending}
		}
		return nil
	}
	fake := clock.NewFake(time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC))
	f.clock = fake
	start := fake.Now()

	last := f.landPerRead(f.runner(landing.Config{NoPipelineWait: 2 * time.Minute, NeverStartedWait: 6 * time.Minute, PollInterval: time.Minute}), fake)

	f.assertHandedBack(c, landing.ChecksNeverStarted)
	assert.GreaterOrEqual(t, last.Sub(start), 5*time.Minute, "it kept today's long wait")
	assert.EqualValues(t, 1, f.hostWrap.updates.Load())
}
