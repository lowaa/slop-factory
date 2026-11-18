package landing_test

import (
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// holdLock has another agent take the integration lock on a ticket of its own,
// as an agent merging by hand does. The returned func gives the lock back.
func (f *fixture) holdLock() (release func()) {
	f.t.Helper()
	other, err := f.agents.Create(f.ctx, "hand-merger")
	require.NoError(f.t, err)
	created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{
		Title: "Merged by hand", Paths: []model.Path{{Path: "elsewhere/"}}, Status: ticket.StatusOpen, CreatedByUserID: &f.creator,
	})
	require.NoError(f.t, err)
	claimed, err := f.tickets.Claim(f.ctx, other.ID, &created.ID)
	require.NoError(f.t, err)
	require.NotNil(f.t, claimed.Ticket)
	require.NoError(f.t, f.tickets.AcquireIntegrationLock(f.ctx, other.ID))
	require.NotNil(f.t, f.lockHolder())
	return func() { require.NoError(f.t, f.tickets.ReleaseIntegrationLock(f.ctx, other.ID)) }
}

func TestLand_AnAgentHoldingTheIntegrationLockIsWaitedForAndTheMergeFollows(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	release := f.holdLock()
	r := f.runner(landing.Config{LockTimeout: time.Hour, PollInterval: time.Millisecond})
	r.Kick(f.tenant)

	// The runner has tried the held lock more than once: it waits and polls.
	require.Eventually(t, func() bool { return f.lockAttempts.Load() >= 3 }, 10*time.Second, time.Millisecond)
	assert.Empty(t, f.host.Merges(), "nothing merges while another agent holds the lock")
	assert.Empty(t, f.outcomesNow(), "and the landing is not given up on")

	release()
	r.Wait()

	f.assertMerged(c)
	assert.Len(t, f.host.Merges(), 1)
}

func TestLand_ALockThatStaysTakenPastTheTimeoutIsHandedBackKeepingItsPlace(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.holdLock()
	fake := clock.NewFake(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	f.clock = fake
	r := f.runner(landing.Config{LockTimeout: 20 * time.Minute, PollInterval: time.Minute})
	var tries []time.Time
	attempted := make(chan struct{}, 1000)
	f.onLockAttempt = func() { tries = append(tries, fake.Now()); attempted <- struct{}{} }

	// The clock moves a minute per try, once the runner is asleep after it: the
	// heartbeat's ticker and the poll's sleep are the two waiters.
	r.Kick(f.tenant)
	done := make(chan struct{})
	go func() { r.Wait(); close(done) }()
loop:
	for {
		select {
		case <-done:
			break loop
		case <-attempted:
			for fake.Waiters() < 2 {
				select {
				case <-done:
					break loop
				default:
					runtime.Gosched()
				}
			}
			fake.Advance(time.Minute)
		}
	}

	require.NotEmpty(t, tries)
	assert.GreaterOrEqual(t, tries[len(tries)-1].Sub(tries[0]), 20*time.Minute, "it kept trying for the whole timeout before giving up")

	rec := f.outcomeOf(c.ticketID)
	assert.Equal(t, landing.LockTimeout, rec.Outcome)
	assert.Empty(t, f.host.Merges())
	row := f.row(c.ticketID)
	require.NotNil(t, row)
	assert.Equal(t, landing.BackWithAgent, row.State)
	assert.True(t, row.KeepsPlace, "a busy lock is not the author's fault")
	assert.Contains(t, f.comments(c.ticketID), "the integration lock stayed taken for 20m0s")
	assert.NotNil(t, f.lockHolder(), "the other agent's lock is untouched")
}

func TestLand_ACancelWhileWaitingForTheLockLeavesTheRowForTheNextSweep(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.holdLock()
	r := f.runner(landing.Config{LockTimeout: time.Hour, PollInterval: time.Millisecond})
	r.Kick(f.tenant)
	require.Eventually(t, func() bool { return f.lockAttempts.Load() >= 2 }, 10*time.Second, time.Millisecond)

	r.Close()

	assert.Empty(t, f.outcomesNow(), "an abandoned landing records no outcome")
	row := f.row(c.ticketID)
	require.NotNil(t, row)
	assert.NotEqual(t, landing.BackWithAgent, row.State, "and hands nothing back")
	assert.Empty(t, f.host.Merges())
}

func TestLand_ALockErrorThatIsNotTheLockBeingHeldIsATriedAgainHostError(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.lockErr = errors.New("db down")

	f.land(f.runner(landing.Config{}))

	assert.Equal(t, landing.CodeHostError, f.outcomeOf(c.ticketID).Outcome)
	assert.EqualValues(t, 1, f.lockAttempts.Load(), "it is not polled for like a held lock")
	assert.True(t, f.row(c.ticketID).KeepsPlace)
	assert.Empty(t, f.host.Merges())
}

func TestBatchRun_AnAgentHoldingTheIntegrationLockIsWaitedForAndTheBatchMergesAfter(t *testing.T) {
	b, cs := newBatchRun(t, 2, "app-a/server/", "app-a/server/")
	release := b.holdLock()
	b.cfg.LockTimeout = time.Hour
	r := b.newRunner()
	r.Kick(b.tenant)

	require.Eventually(t, func() bool { return b.lockAttempts.Load() >= 3 }, 10*time.Second, time.Millisecond)
	assert.Empty(t, b.host.Merges(), "nothing merges while another agent holds the lock")
	assert.Empty(t, b.outcomesNow())

	release()
	r.Wait()

	assert.Equal(t, ids(cs), b.world.merges)
	assert.Nil(t, b.lockHolder())
}

func TestBatchRun_ALockThatStaysTakenPastTheTimeoutHandsEveryMemberBackKeepingItsPlace(t *testing.T) {
	b, cs := newBatchRun(t, 2, "app-a/server/", "app-a/server/")
	b.holdLock()
	b.cfg.LockTimeout = 20 * time.Millisecond

	b.land(b.newRunner())

	assert.Empty(t, b.host.Merges())
	for _, c := range cs {
		assert.Equal(t, landing.LockTimeout, b.firstOutcome(c.ticketID).Outcome)
		row := b.row(c.ticketID)
		require.NotNil(t, row)
		assert.True(t, row.KeepsPlace)
	}
	live, err := b.store.Live(b.ctx)
	require.NoError(t, err)
	assert.Empty(t, live)
}
