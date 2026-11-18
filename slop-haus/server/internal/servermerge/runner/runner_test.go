package runner_test

import (
	"context"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/jobs"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/servermerge/runner"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

func (f *fixture) onlyOutcome() landing.Record {
	f.t.Helper()
	recs := f.outcomesNow()
	require.Len(f.t, recs, 1)
	return recs[0]
}

// handedBack is what every failure leaves: nothing merged, the ticket
// queued for its author with guidance, the lock free.
func (f *fixture) assertHandedBack(a *approved, want landing.Outcome) {
	f.t.Helper()
	assert.Equal(f.t, want, f.onlyOutcome().Outcome)
	row := f.attemptOf(a)
	assert.Equal(f.t, "queued", row.status)
	require.NotNil(f.t, row.owed)
	assert.Equal(f.t, a.author, *row.owed)
	assert.Nil(f.t, f.lockHolder())
	assert.Contains(f.t, f.comments(a), "The server")
}

func (f *fixture) assertNotMerged() { f.t.Helper(); assert.Empty(f.t, f.host.Merges()) }

func TestApproved_MergesAnApprovedCurrentGreenChange(t *testing.T) {
	f := newFixture(t)
	a := f.approvedChange()

	f.attempt(a, f.runner(runner.Config{}))

	rec := f.onlyOutcome()
	assert.Equal(t, landing.Merged, rec.Outcome)
	require.NotNil(t, rec.ApproverUserID)
	assert.Equal(t, f.approver, *rec.ApproverUserID)
	assert.NotNil(t, rec.ApprovedAt)
	assert.Equal(t, headA, rec.HeadSHA)
	assert.Len(t, f.host.Merges(), 1)
	f.wrap.mu.Lock()
	require.Len(t, f.wrap.merges, 1)
	assert.Equal(t, headA, f.wrap.merges[0].ExpectedSHA, "the merge is asked for the head the checks were read for")
	f.wrap.mu.Unlock()
	got, err := f.tickets.Get(f.ctx, a.ticketID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDone, got.Status)
	assert.Nil(t, f.lockHolder())
	_, err = f.store.LeaseFor(f.ctx, a.author)
	assert.ErrorIs(t, err, ticket.ErrNoLease, "the author's agent was not resumed")
	merged := f.log.audited(t, audit.ChangeMerged)
	require.Len(t, merged, 1)
	assert.Equal(t, f.approver.String(), merged[0]["approved_by_user_id"])
}

func TestApproved_MainMovedIsHandedBack(t *testing.T) {
	f := newFixture(t)
	a := f.approvedChange()
	f.host.CompareOf[f.host.Opened].ContainsTip = false

	f.attempt(a, f.runner(runner.Config{}))

	f.assertHandedBack(a, landing.MainMoved)
	f.assertNotMerged()
	assert.Contains(t, f.comments(a), "origin/<base>")
}

func TestApproved_RedChecksAreHandedBack(t *testing.T) {
	f := newFixture(t)
	a := f.approvedChange()
	f.setChecks(headA, codehost.Check{Name: "ci/build", State: codehost.CheckFailure})

	f.attempt(a, f.runner(runner.Config{}))

	f.assertHandedBack(a, landing.ChecksRed)
	f.assertNotMerged()
}

func TestApproved_PendingChecksAreWaitedFor(t *testing.T) {
	f := newFixture(t)
	a := f.approvedChange()
	f.setChecks(headA, codehost.Check{Name: "ci/build", State: codehost.CheckPending})
	reads := 0
	f.host.AfterChecks = func() {
		if reads++; reads == 3 {
			f.setChecks(headA, codehost.Check{Name: "ci/build", State: codehost.CheckSuccess})
		}
	}

	f.attempt(a, f.runner(runner.Config{}))

	assert.Equal(t, landing.Merged, f.onlyOutcome().Outcome)
	assert.GreaterOrEqual(t, reads, 3)
}

func TestApproved_PendingChecksTimeOut(t *testing.T) {
	f := newFixture(t)
	a := f.approvedChange()
	f.setChecks(headA, codehost.Check{Name: "ci/build", State: codehost.CheckPending})

	fake := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	f.clock = fake
	f.attemptAdvancing(a, f.runner(runner.Config{ChecksTimeout: time.Hour, PollInterval: time.Minute}), fake, time.Minute)

	f.assertHandedBack(a, landing.ChecksPendingTimeout)
	f.assertNotMerged()
}

func TestApproved_ABusyLockIsQueuedForAndTheMergeFollows(t *testing.T) {
	f := newFixture(t)
	a := f.approvedChange()
	other, err := f.agents.Create(f.ctx, "other")
	require.NoError(t, err)
	_, err = f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "Other", Paths: []model.Path{{Path: "app/other/"}}, Status: ticket.StatusOpen})
	require.NoError(t, err)
	_, err = f.tickets.Claim(f.ctx, other.ID, nil)
	require.NoError(t, err)
	require.NoError(t, f.tickets.AcquireIntegrationLock(f.ctx, other.ID))

	r := f.runner(runner.Config{})
	require.NoError(t, r.Approved(context.Background(), a.approval))
	require.Eventually(t, func() bool {
		st, err := f.tickets.IntegrationLockState(f.ctx)
		return err == nil && len(st.Queue) == 1
	}, 10*time.Second, time.Millisecond)
	require.NoError(t, f.tickets.ReleaseIntegrationLock(f.ctx, other.ID))
	r.Wait()

	assert.Equal(t, landing.Merged, f.onlyOutcome().Outcome)
}

func TestApproved_ALockThatStaysBusyTimesOut(t *testing.T) {
	f := newFixture(t)
	a := f.approvedChange()
	other, err := f.agents.Create(f.ctx, "other")
	require.NoError(t, err)
	_, err = f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "Other", Paths: []model.Path{{Path: "app/other/"}}, Status: ticket.StatusOpen})
	require.NoError(t, err)
	_, err = f.tickets.Claim(f.ctx, other.ID, nil)
	require.NoError(t, err)
	require.NoError(t, f.tickets.AcquireIntegrationLock(f.ctx, other.ID))

	fake := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	f.clock = fake
	f.attemptAdvancing(a, f.runner(runner.Config{LockTimeout: time.Hour, PollInterval: time.Minute}), fake, time.Minute)

	assert.Equal(t, landing.LockTimeout, f.onlyOutcome().Outcome)
	assert.Empty(t, f.host.Merges())
	st, err := f.tickets.IntegrationLockState(f.ctx)
	require.NoError(t, err)
	assert.Empty(t, st.Queue, "the server leaves the queue")
	assert.Equal(t, other.ID, st.Holder.AgentID)
}

func TestApproved_AHeadPushedAfterTheApprovalIsNotMerged(t *testing.T) {
	f := newFixture(t)
	a := f.approvedChange()
	f.setChecks(headB, codehost.Check{Name: "ci/build", State: codehost.CheckSuccess})

	f.attempt(a, f.runner(runner.Config{}))

	f.assertHandedBack(a, landing.HeadChanged)
	f.assertNotMerged()
}

func TestApproved_AHeadThatMovesBetweenTheChecksAndTheMergeIsNotMerged(t *testing.T) {
	f := newFixture(t)
	a := f.approvedChange()
	f.host.MergeErr = codehost.ErrHeadMoved

	f.attempt(a, f.runner(runner.Config{}))

	f.assertHandedBack(a, landing.HeadChanged)
	assert.Len(t, f.host.Merges(), 1, "the host was asked and refused")
}

func TestApproved_AnApprovalTakenAwayIsNotMerged(t *testing.T) {
	f := newFixture(t)
	a := f.approvedChange()
	reads := 0
	f.host.AfterChecks = func() {
		// After the claim, which needs the approval, and before the merge.
		if reads++; reads == 1 {
			_, err := f.database.Exec(f.ctx, `UPDATE attempts SET approved_at = NULL, approved_by_user_id = NULL WHERE ticket_id = $1 AND number = 1`, a.ticketID)
			require.NoError(t, err)
		}
	}

	f.attempt(a, f.runner(runner.Config{}))

	f.assertHandedBack(a, landing.NotApprovedForHead)
	f.assertNotMerged()
}

func TestApproved_TheHostFailingIsHandedBackWithoutItsMessage(t *testing.T) {
	f := newFixture(t)
	a := f.approvedChange()
	f.host.MergeErr = codehost.ErrUnreachable

	f.attempt(a, f.runner(runner.Config{}))

	f.assertHandedBack(a, landing.CodeHostError)
	assert.Len(t, f.host.Merges(), 1, "the host was asked and refused")
	assert.NotContains(t, f.comments(a), codehost.ErrUnreachable.Error())
	assert.NotContains(t, f.onlyOutcome().Detail, codehost.ErrUnreachable.Error())
}

func TestApproved_AChangeStillInDraftIsRefused(t *testing.T) {
	f := newFixture(t)
	a := f.approvedChange()
	_, err := f.database.Exec(f.ctx, `UPDATE ticket_changes SET draft = true WHERE ticket_id = $1`, a.ticketID)
	require.NoError(t, err)

	f.attempt(a, f.runner(runner.Config{}))

	f.assertHandedBack(a, landing.MergeRefused)
	f.assertNotMerged()
}

func TestApproved_NeverAttempted(t *testing.T) {
	t.Run("a tenant without change tools", func(t *testing.T) {
		f := newFixture(t)
		a := f.approvedChange()
		f.tools.connected = false

		f.attempt(a, f.runner(runner.Config{}))

		assert.Empty(t, f.outcomesNow())
		assert.Equal(t, "queued", f.attemptOf(a).status)
	})
	t.Run("an attempt no person approved", func(t *testing.T) {
		f := newFixture(t)
		a := f.approvedChange()
		_, err := f.database.Exec(f.ctx, `UPDATE attempts SET approved_at = NULL, approved_by_user_id = NULL WHERE ticket_id = $1 AND number = 1`, a.ticketID)
		require.NoError(t, err)

		f.attempt(a, f.runner(runner.Config{}))

		assert.Empty(t, f.outcomesNow())
		assert.Empty(t, f.host.Merges())
	})
	t.Run("an attempt that is not the promoted one", func(t *testing.T) {
		f := newFixture(t)
		a := f.approvedChange()
		_, err := f.database.Exec(f.ctx, `UPDATE attempts SET promoted_at = NULL WHERE ticket_id = $1 AND number = 1`, a.ticketID)
		require.NoError(t, err)

		f.attempt(a, f.runner(runner.Config{}))

		assert.Empty(t, f.outcomesNow())
		assert.Empty(t, f.host.Merges())
	})
}

func TestApproved_TwoRunnersToldAtOnceMergeOnce(t *testing.T) {
	f := newFixture(t)
	a := f.approvedChange()
	first, second := f.runner(runner.Config{}), f.runner(runner.Config{})

	var start sync.WaitGroup
	start.Add(1)
	var told sync.WaitGroup
	for _, r := range []*runner.Runner{first, second} {
		told.Go(func() {
			start.Wait()
			require.NoError(t, r.Approved(context.Background(), a.approval))
		})
	}
	start.Done()
	told.Wait()
	first.Wait()
	second.Wait()

	assert.Len(t, f.host.Merges(), 1)
	assert.Equal(t, landing.Merged, f.onlyOutcome().Outcome)
}

func TestApproved_TheHookFiringAgainDoesNotMergeTwice(t *testing.T) {
	f := newFixture(t)
	a := f.approvedChange()
	r := f.runner(runner.Config{})

	f.attempt(a, r)
	f.attempt(a, r)

	assert.Len(t, f.host.Merges(), 1)
	assert.Len(t, f.outcomesNow(), 1)
}

// An attempt abandoned while it holds the lock (its process died) is
// recovered by the lease running out, not by a second runner doing the merge.
func TestAbandoned_HoldingTheLockIsReapedAfterTheLeaseExpires(t *testing.T) {
	f := newFixture(t)
	a := f.approvedChange()
	atMerge := make(chan struct{})
	f.wrap.onMerge = func(ctx context.Context) {
		close(atMerge)
		<-ctx.Done()
	}
	r := f.runner(runner.Config{})
	require.NoError(t, r.Approved(context.Background(), a.approval))
	<-atMerge
	require.NotNil(t, f.lockHolder())
	r.Close()

	assert.Empty(t, f.outcomesNow(), "an abandoned attempt records nothing itself")
	assert.Equal(t, "in_progress", f.attemptOf(a).status)

	// Not yet expired: a replica's reaper leaves it alone, and the hook
	// firing again is refused because the server holds the ticket.
	other := f.runner(runner.Config{})
	require.NoError(t, other.Reap(context.Background()))
	f.attempt(a, other)
	assert.Equal(t, "in_progress", f.attemptOf(a).status)
	assert.Empty(t, f.host.Merges())

	f.expireServerLease()
	require.NoError(t, other.Reap(context.Background()))

	assert.Equal(t, "queued", f.attemptOf(a).status)
	require.NotNil(t, f.attemptOf(a).owed)
	assert.Equal(t, a.author, *f.attemptOf(a).owed)
	assert.Nil(t, f.lockHolder())
	rec := f.onlyOutcome()
	assert.Equal(t, landing.MergeRefused, rec.Outcome)
	assert.Contains(t, rec.Detail, "did not finish")
	assert.Contains(t, f.comments(a), "stopped before it finished")
	assert.Empty(t, f.host.Merges())

	require.NoError(t, other.Reap(context.Background()), "a second reap finds nothing to do")
	assert.Len(t, f.outcomesNow(), 1)
}

// Before the lock is taken the lease is not pinned, so the ordinary expiry
// sweep returns the ticket, with the author still owed it.
func TestAbandoned_WhileWaitingForChecksTheOrdinaryExpiryRequeuesIt(t *testing.T) {
	f := newFixture(t)
	a := f.approvedChange()
	atChecks := make(chan struct{})
	f.wrap.onChecks = func(ctx context.Context) {
		close(atChecks)
		<-ctx.Done()
	}
	r := f.runner(runner.Config{})
	require.NoError(t, r.Approved(context.Background(), a.approval))
	<-atChecks
	r.Close()
	f.expireServerLease()

	// Any claim in the tenant runs the sweep; the author's own is the one
	// that matters.
	claimed, err := f.tickets.Claim(f.ctx, a.author, &a.ticketID)
	require.NoError(t, err)
	require.NotNil(t, claimed.Ticket, "the author gets the ticket back")
	assert.Nil(t, f.lockHolder())
	assert.Empty(t, f.host.Merges())
}

func (f *fixture) expireServerLease() {
	f.t.Helper()
	_, err := f.database.Exec(f.ctx, `UPDATE leases SET expires_at = now() - interval '1 minute'
		WHERE released_at IS NULL AND agent_id = (SELECT id FROM agents WHERE name = $1)`, ticket.ServerAgentName)
	require.NoError(f.t, err)
}

// abandonedAtMerge is an attempt that died holding the lock, its lease run out.
func (f *fixture) abandonedAtMerge() *approved {
	f.t.Helper()
	a := f.approvedChange()
	atMerge := make(chan struct{})
	f.wrap.onMerge = func(ctx context.Context) {
		close(atMerge)
		<-ctx.Done()
	}
	r := f.runner(runner.Config{})
	require.NoError(f.t, r.Approved(context.Background(), a.approval))
	<-atMerge
	r.Close()
	f.wrap.onMerge = nil
	f.expireServerLease()
	return a
}

// The host accepted the merge and it was recorded, then the process died
// before closing the ticket: handing it back would have the author redo it.
func TestReap_AMergedChangeIsClosedNotHandedBack(t *testing.T) {
	f := newFixture(t)
	a := f.abandonedAtMerge()
	_, err := f.database.Exec(f.ctx, `UPDATE ticket_changes SET merged_at = now(), merge_sha = 'mergecommit',
		merged_by_agent_id = (SELECT id FROM agents WHERE name = $2) WHERE ticket_id = $1`, a.ticketID, ticket.ServerAgentName)
	require.NoError(t, err)

	require.NoError(t, f.runner(runner.Config{}).Reap(context.Background()))

	got, err := f.tickets.Get(f.ctx, a.ticketID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDone, got.Status)
	assert.Nil(t, f.lockHolder())
	rec := f.onlyOutcome()
	assert.Equal(t, landing.Merged, rec.Outcome)
	require.NotNil(t, rec.ApproverUserID)
	assert.Equal(t, f.approver, *rec.ApproverUserID)
}

// A merge asked of the host a moment ago may still land, so it is not handed
// back until the change flow's own claim on it has lapsed.
func TestReap_AMergeStillInFlightIsLeftAlone(t *testing.T) {
	f := newFixture(t)
	a := f.abandonedAtMerge()
	_, err := f.database.Exec(f.ctx, `UPDATE ticket_changes SET merging_since = now() WHERE ticket_id = $1`, a.ticketID)
	require.NoError(t, err)
	r := f.runner(runner.Config{})

	require.NoError(t, r.Reap(context.Background()))
	assert.Equal(t, "in_progress", f.attemptOf(a).status)
	assert.Empty(t, f.outcomesNow())

	_, err = f.database.Exec(f.ctx, `UPDATE ticket_changes SET merging_since = now() - interval '10 minutes' WHERE ticket_id = $1`, a.ticketID)
	require.NoError(t, err)
	require.NoError(t, r.Reap(context.Background()))
	assert.Equal(t, "queued", f.attemptOf(a).status)
}

// The reaper runs as a job: a pass at start leaves a merge still in flight
// alone, and the tick after it has lapsed hands the ticket back.
func TestReap_JobHandsBackOnATickAfterTheLeaseExpires(t *testing.T) {
	f := newFixture(t)
	a := f.abandonedAtMerge()
	_, err := f.database.Exec(f.ctx, `UPDATE ticket_changes SET merging_since = now() WHERE ticket_id = $1`, a.ticketID)
	require.NoError(t, err)
	r := f.runner(runner.Config{})

	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	passes := make(chan error, 4)
	sched := jobs.New(&jobs.Options{Clock: clk, AfterRun: func(_ string, err error) { passes <- err }})
	_, err = sched.Start(f.ctx, r.Job())
	require.NoError(t, err)
	defer sched.Close()

	require.NoError(t, <-passes)
	assert.Equal(t, "in_progress", f.attemptOf(a).status)

	_, err = f.database.Exec(f.ctx, `UPDATE ticket_changes SET merging_since = now() - interval '10 minutes' WHERE ticket_id = $1`, a.ticketID)
	require.NoError(t, err)
	clk.BlockUntil(1)
	clk.Advance(time.Minute)
	require.NoError(t, <-passes)
	assert.Equal(t, "queued", f.attemptOf(a).status)
}

// ctxTools reports each Connected call and, when closed is set, counts the
// ones that begin after the runner's Close has returned.
type ctxTools struct {
	closed *atomic.Bool
	late   *atomic.Int64
}

func (c ctxTools) Connected(ctx context.Context) (bool, error) {
	if c.closed.Load() {
		c.late.Add(1)
	}
	<-ctx.Done()
	return false, ctx.Err()
}

func TestApproved_RacingCloseStartsNothingAfterCloseReturns(t *testing.T) {
	f := newFixture(t)
	a := f.approvedChange()
	for range 200 {
		var closed atomic.Bool
		var late atomic.Int64
		r, err := runner.New(&runner.Options{
			Tickets: ticket.NewServerAgent(f.tickets), Changes: f.wrap, Tools: ctxTools{&closed, &late}, Outcomes: f.outcomes,
			Reaper: runner.NewPostgresReaper(f.database), Logger: slog.DiscardHandler,
		})
		require.NoError(t, err)

		start := make(chan struct{})
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() {
				<-start
				_ = r.Approved(context.Background(), a.approval)
			})
		}
		close(start)
		r.Close()
		closed.Store(true)
		wg.Wait()
		r.Wait()
		require.Zero(t, late.Load(), "an attempt began after Close returned")
	}
}

type routingFunc func(context.Context) (bool, error)

func (r routingFunc) Enqueues(ctx context.Context) (bool, error) { return r(ctx) }

func TestApproved_ATenantTheQueueEnqueuesForIsLeftAlone(t *testing.T) {
	f := newFixture(t)
	a := f.approvedChange()
	r := f.runnerWith(runner.Config{}, routingFunc(func(context.Context) (bool, error) { return true, nil }))

	f.attempt(a, r)

	assert.Empty(t, f.outcomesNow())
	f.assertNotMerged()
	got := f.attemptOf(a)
	assert.Equal(t, "queued", got.status)
}

func TestApproved_ATenantNotRoutedToTheQueueIsMerged(t *testing.T) {
	f := newFixture(t)
	a := f.approvedChange()
	r := f.runnerWith(runner.Config{}, routingFunc(func(context.Context) (bool, error) { return false, nil }))

	f.attempt(a, r)

	assert.Equal(t, landing.Merged, f.onlyOutcome().Outcome)
}
