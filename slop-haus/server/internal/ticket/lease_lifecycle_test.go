package ticket_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

func TestHeartbeat_ExtendsAHeldLease(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	f.ticket(t, "Work", "server/")

	_, err := f.svc.Claim(f.ctx, a, nil)
	require.NoError(t, err)

	before, err := f.svc.LeaseFor(f.ctx, a)
	require.NoError(t, err)

	f.expire(t, a)
	// An expired lease is no longer held, so the heartbeat must refuse it
	// rather than resurrect it.
	require.ErrorIs(t, f.svc.Heartbeat(f.ctx, a), ticket.ErrNoLease)
	assert.NotZero(t, before.ExpiresAt)
}

// The race the conditional update exists for: the agent's heartbeat arrives
// after its lease has been expired and the ticket re-granted. A blind
// UPDATE would resurrect a lease the server already gave away, leaving two
// agents holding overlapping paths.
func TestHeartbeat_DoesNotResurrectARegrantedLease(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a1, a2 := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	created := f.ticket(t, "Contended", "server/")

	_, err := f.svc.Claim(f.ctx, a1, nil)
	require.NoError(t, err)

	f.expire(t, a1)

	regranted, err := f.svc.Claim(f.ctx, a2, nil)
	require.NoError(t, err)
	require.NotNil(t, regranted.Ticket)
	require.Equal(t, created.ID, regranted.Ticket.ID)

	// The original holder's heartbeat must report the loss, not silently
	// succeed -- it has been working on a ticket it no longer holds.
	require.ErrorIs(t, f.svc.Heartbeat(f.ctx, a1), ticket.ErrNoLease)

	var active int
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT count(*) FROM leases WHERE released_at IS NULL`).Scan(&active))
	assert.Equal(t, 1, active, "the expired lease must not have come back")
}

func TestRelease_ReturnsTheTicketToTheQueue(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Given back", "server/")

	_, err := f.svc.Claim(f.ctx, a, nil)
	require.NoError(t, err)

	released, err := f.svc.Release(f.ctx, a, "changed my mind", nil)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusOpen, released.Status)
	assert.Equal(t, created.ID, released.ID)

	// Claimable again, by the same agent or another.
	again, err := f.svc.Claim(f.ctx, a, nil)
	require.NoError(t, err)
	require.NotNil(t, again.Ticket)
}

func TestRelease_BlockedOnRecordsADependency(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")

	blocker := f.ticket(t, "Must land first")
	work := f.ticket(t, "Needs the other", "server/")

	claimed, err := f.svc.Claim(f.ctx, a, &work.ID)
	require.NoError(t, err)
	require.NotNil(t, claimed.Ticket)

	released, err := f.svc.Release(f.ctx, a, "needs #1", &blocker.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusBlocked, released.Status)
	assert.Equal(t, []int64{blocker.ID}, released.DependsOn)
}

// The release path writes a blocking edge like any other, so it is held to
// the same rule. An agent that releases onto a ticket already waiting on
// the one it holds would otherwise knot the two together, and nothing in
// the claim path could ever hand either of them out again. This route is
// driven by a model, not by a human reading the board, which is why it
// matters more here than on the REST surface.
func TestRelease_BlockedOnThatWouldFormACycleIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")

	first := f.ticket(t, "Must land first", "server/one/")
	second := f.ticket(t, "Waits on the first", "server/two/")
	f.blockedBy(t, first.ID, second.ID)

	claimed, err := f.svc.Claim(f.ctx, a, &second.ID)
	require.NoError(t, err)
	require.NotNil(t, claimed.Ticket)

	_, err = f.svc.Release(f.ctx, a, "needs the other one", &first.ID)
	require.ErrorIs(t, err, ticket.ErrDependencyCycle)

	// The lease is still the agent's, so it has a way out: release again
	// without a blocked_on.
	released, err := f.svc.Release(f.ctx, a, "needs the other one", nil)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusOpen, released.Status)

	g, err := f.svc.Graph(f.ctx, &ticket.GraphQuery{})
	require.NoError(t, err)
	assert.Len(t, g.Edges, 1, "the knot was never tied")
}

// Blocked-ness is a predicate, not an edge trigger. Releasing onto a
// dependency that is *already* terminal must leave the ticket open -- the
// transition it would have waited for happened in the past and will never
// fire again.
func TestRelease_BlockedOnAlreadyDoneLeavesItOpen(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")

	blocker := f.ticket(t, "Already finished")
	done := ticket.StatusDone
	_, err := f.svc.Update(f.ctx, blocker.ID, &ticket.UpdateParams{Status: &done})
	require.NoError(t, err)

	work := f.ticket(t, "Released onto finished work", "server/")
	_, err = f.svc.Claim(f.ctx, a, &work.ID)
	require.NoError(t, err)

	released, err := f.svc.Release(f.ctx, a, "thought it was blocked", &blocker.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusOpen, released.Status,
		"a dependency that is already terminal must not strand the ticket")
}

func TestDone_ClosesTheTicketAndRecordsTheSummary(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	f.ticket(t, "Finish me", "server/")

	_, err := f.svc.Claim(f.ctx, a, nil)
	require.NoError(t, err)

	finished, err := f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "landed on branch ticket/1"})
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDone, finished.Status)
	require.NotNil(t, finished.ClosedAt)

	comments, err := f.svc.ListComments(f.ctx, finished.ID)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, "landed on branch ticket/1", comments[0].Body)
	require.NotNil(t, comments[0].AuthorAgentID, "the summary is the agent's, not the human's")
}

func TestDone_WithoutALease(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")

	_, err := f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "nothing to finish"})
	require.ErrorIs(t, err, ticket.ErrNoLease)
}

func TestExtendScope_GrantsWhenNothingConflicts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Grows", "server/internal/ticket/")

	_, err := f.svc.Claim(f.ctx, a, nil)
	require.NoError(t, err)

	result, err := f.svc.ExtendScope(f.ctx, a, []model.Path{{Path: "server/internal/db/"}})
	require.NoError(t, err)
	assert.True(t, result.Granted)
	assert.Nil(t, result.Denied)

	got, err := f.svc.Get(f.ctx, created.ID)
	require.NoError(t, err)
	assert.Contains(t, got.Paths, model.Path{Path: "server/internal/db/", Source: model.PathSourceExtended})
}

func TestExtendScope_DeniedNamesTheHolder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a1, a2 := f.agent(t, "builder-1"), f.agent(t, "builder-2")

	held := f.ticket(t, "Holds db", "server/internal/db/")
	mine := f.ticket(t, "Wants db too", "server/internal/ticket/")

	_, err := f.svc.Claim(f.ctx, a1, &held.ID)
	require.NoError(t, err)
	_, err = f.svc.Claim(f.ctx, a2, &mine.ID)
	require.NoError(t, err)

	result, err := f.svc.ExtendScope(f.ctx, a2, []model.Path{{Path: "server/internal/db/lease.go"}})
	require.NoError(t, err)
	assert.False(t, result.Granted)
	require.NotNil(t, result.Denied, "denial has to name what conflicted, so the agent can act")
	assert.Equal(t, held.ID, result.Denied.ConflictingTicketID)
	assert.Equal(t, a1, result.Denied.HolderAgentID)
}

// A ticket's own scopes must not deny its extension -- without excluding
// self, every extension of an already-scoped ticket would be refused by its
// own paths.
func TestExtendScope_OwnScopesDoNotConflict(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	f.ticket(t, "Already scoped", "server/internal/ticket/")

	_, err := f.svc.Claim(f.ctx, a, nil)
	require.NoError(t, err)

	result, err := f.svc.ExtendScope(f.ctx, a,
		[]model.Path{{Path: "server/internal/ticket/lease.go"}})
	require.NoError(t, err)
	assert.True(t, result.Granted)
}

// Holding the integration lock pins its lease: a merge is exactly when an
// agent goes quiet, so a lock derived from an *expiring* lease would be
// handed away mid-merge.
func TestIntegrationLock_PinsItsLeaseAgainstExpiry(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a1, a2 := f.agent(t, "merger"), f.agent(t, "other")
	created := f.ticket(t, "Merging", "server/")

	_, err := f.svc.Claim(f.ctx, a1, nil)
	require.NoError(t, err)
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, a1))

	// Past the nominal TTL, still merging, still making no calls.
	f.expire(t, a1)

	other, err := f.svc.Claim(f.ctx, a2, nil)
	require.NoError(t, err)
	assert.Nil(t, other.Ticket, "a pinned lease must not be swept up and re-granted")

	// And the holder can still finish, which an inline expires_at guard
	// would have refused.
	finished, err := f.svc.Done(f.ctx, a1, &ticket.DoneParams{Summary: "merged"})
	require.NoError(t, err)
	assert.Equal(t, created.ID, finished.ID)
	assert.Equal(t, ticket.StatusDone, finished.Status)
}

func TestIntegrationLock_IsExclusiveAndReentrantForItsHolder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a1, a2 := f.agent(t, "merger"), f.agent(t, "other")
	f.ticket(t, "One", "a/")
	f.ticket(t, "Two", "b/")

	_, err := f.svc.Claim(f.ctx, a1, nil)
	require.NoError(t, err)
	_, err = f.svc.Claim(f.ctx, a2, nil)
	require.NoError(t, err)

	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, a1))
	// A retry after a timeout must not be told someone else took its own
	// lock.
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, a1))
	require.ErrorIs(t, f.svc.AcquireIntegrationLock(f.ctx, a2), ticket.ErrLockHeld)
}

func TestIntegrationLock_RefusalNamesTheHolderAndSinceWhen(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a1, a2 := f.agent(t, "merger"), f.agent(t, "other")
	holding := f.ticket(t, "Holder work", "a/")
	f.ticket(t, "Two", "b/")

	_, err := f.svc.Claim(f.ctx, a1, nil)
	require.NoError(t, err)
	_, err = f.svc.Claim(f.ctx, a2, nil)
	require.NoError(t, err)
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, a1))
	// Backdate the hold so the server-computed duration is distinguishable
	// from zero without sleeping.
	_, err = f.database.Exec(f.ctx, `UPDATE locks SET acquired_at = now() - interval '90 minutes'`)
	require.NoError(t, err)
	var acquired time.Time
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT acquired_at FROM locks`).Scan(&acquired))

	err = f.svc.AcquireIntegrationLock(f.ctx, a2)
	require.ErrorIs(t, err, ticket.ErrLockHeld)
	var held *ticket.LockHeldError
	require.ErrorAs(t, err, &held)
	require.NotNil(t, held.Holder)
	assert.Equal(t, holding.ID, held.Holder.TicketID)
	assert.Equal(t, "Holder work", held.Holder.TicketTitle)
	assert.Equal(t, "merger", held.Holder.AgentName)
	assert.WithinDuration(t, acquired, held.Holder.AcquiredAt, 0)
	assert.GreaterOrEqual(t, held.Holder.Held, 90*time.Minute)
	assert.Less(t, held.Holder.Held, 91*time.Minute)
	assert.Contains(t, err.Error(), acquired.UTC().Format(time.RFC3339))
	assert.Contains(t, err.Error(), `"Holder work"`)
	assert.Contains(t, err.Error(), "1h30m")
}

// Nothing in the schema cascades the lock's release, so every route out of
// a lease has to drop it. An agent giving up mid-merge would otherwise
// leave a lock whose lease is gone, stalling integration until a human
// noticed.
func TestIntegrationLock_IsDroppedByEveryLeaseEndingRoute(t *testing.T) {
	t.Parallel()
	lockCount := func(f *fixture, t *testing.T) int {
		t.Helper()
		var n int
		require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM locks`).Scan(&n))
		return n
	}

	t.Run("done", func(t *testing.T) {
		f := newFixture(t)
		a := f.agent(t, "merger")
		f.ticket(t, "Work", "server/")
		_, err := f.svc.Claim(f.ctx, a, nil)
		require.NoError(t, err)
		require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, a))

		_, err = f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "merged"})
		require.NoError(t, err)
		assert.Zero(t, lockCount(f, t))
	})

	t.Run("release", func(t *testing.T) {
		f := newFixture(t)
		a := f.agent(t, "merger")
		f.ticket(t, "Work", "server/")
		_, err := f.svc.Claim(f.ctx, a, nil)
		require.NoError(t, err)
		require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, a))

		_, err = f.svc.Release(f.ctx, a, "gave up mid-merge", nil)
		require.NoError(t, err)
		assert.Zero(t, lockCount(f, t), "release must drop the lock, not leave it orphaned")
	})

	t.Run("explicit unlock", func(t *testing.T) {
		f := newFixture(t)
		a := f.agent(t, "merger")
		f.ticket(t, "Work", "server/")
		_, err := f.svc.Claim(f.ctx, a, nil)
		require.NoError(t, err)
		require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, a))

		require.NoError(t, f.svc.ReleaseIntegrationLock(f.ctx, a))
		assert.Zero(t, lockCount(f, t))

		// Un-pinned, so normal expiry resumes.
		f.expire(t, a)
		other := f.agent(t, "other")
		result, err := f.svc.Claim(f.ctx, other, nil)
		require.NoError(t, err)
		assert.NotNil(t, result.Ticket)
	})
}

func TestDisabledAgent_IsSkippedAndItsLeaseReleased(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Held then abandoned", "server/")

	_, err := f.svc.Claim(f.ctx, a, nil)
	require.NoError(t, err)

	_, err = f.agents.SetDisabled(f.ctx, a, true)
	require.NoError(t, err)

	// The ticket comes back rather than waiting out the TTL for an agent
	// nobody is going to run.
	got, err := f.svc.Get(f.ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusOpen, got.Status)

	// And the disabled agent is served nothing.
	result, err := f.svc.Claim(f.ctx, a, nil)
	require.NoError(t, err)
	assert.Nil(t, result.Ticket)

	other := f.agent(t, "builder-2")
	regranted, err := f.svc.Claim(f.ctx, other, nil)
	require.NoError(t, err)
	require.NotNil(t, regranted.Ticket)
	assert.Equal(t, created.ID, regranted.Ticket.ID)
}

// Disabling an agent a second time must not touch work that has since been
// re-granted to somebody else.
//
// The reopen used to match every historical lease of that agent carrying
// release_reason = 'agent disabled', so the second disable swept up a
// ticket another agent was in the middle of and set it back to open --
// with that agent's lease still active, leaving the ticket claimable while
// held.
func TestDisableAgent_TwiceDoesNotReopenSomeoneElsesWork(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	first, second := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	created := f.ticket(t, "Passed along", "server/")

	_, err := f.svc.Claim(f.ctx, first, nil)
	require.NoError(t, err)

	_, err = f.agents.SetDisabled(f.ctx, first, true)
	require.NoError(t, err)

	// Someone else picks the ticket up.
	regranted, err := f.svc.Claim(f.ctx, second, nil)
	require.NoError(t, err)
	require.NotNil(t, regranted.Ticket)
	require.Equal(t, created.ID, regranted.Ticket.ID)

	// Disabling the first agent again -- it now holds nothing -- must be
	// inert.
	_, err = f.agents.SetDisabled(f.ctx, first, true)
	require.NoError(t, err)

	got, err := f.svc.Get(f.ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusInProgress, got.Status,
		"the second agent's in-progress ticket must be untouched")

	held, err := f.svc.LeaseFor(f.ctx, second)
	require.NoError(t, err)
	assert.Equal(t, created.ID, held.TicketID, "its lease must still be active")
}

// A disabled agent holding the integration lock must not leave it behind:
// the lock's lifetime derives from a lease that no longer exists, so
// nothing would ever release it and every merge would stall.
func TestDisableAgent_DropsTheIntegrationLock(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	merger, other := f.agent(t, "merger"), f.agent(t, "other")
	f.ticket(t, "Merging", "a/")
	f.ticket(t, "Waiting", "b/")

	_, err := f.svc.Claim(f.ctx, merger, nil)
	require.NoError(t, err)
	_, err = f.svc.Claim(f.ctx, other, nil)
	require.NoError(t, err)
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, merger))

	_, err = f.agents.SetDisabled(f.ctx, merger, true)
	require.NoError(t, err)

	var locks int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM locks`).Scan(&locks))
	assert.Zero(t, locks, "a disabled agent's lock must not outlive its lease")

	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, other),
		"the lock must be available again")
}
