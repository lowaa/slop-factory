package ticket_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// heldLeaseOps are the agent's operations on the ticket it holds that
// resolve its lease under the claim lock.
var heldLeaseOps = []struct {
	name string
	run  func(f *fixture, agentID uuid.UUID) error
	// unblocks is whether the operation reopens tickets that were waiting
	// on the held one.
	unblocks bool
}{
	{name: "release", run: func(f *fixture, a uuid.UUID) error {
		_, err := f.svc.Release(f.ctx, a, "giving it back", nil)
		return err
	}},
	{name: "done", unblocks: true, run: func(f *fixture, a uuid.UUID) error {
		_, err := f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "finished"})
		return err
	}},
	{name: "discuss", run: func(f *fixture, a uuid.UUID) error {
		_, err := f.svc.Discuss(f.ctx, a, "Which shape?", "Half-done on a branch.")
		return err
	}},
	{name: "review", run: func(f *fixture, a uuid.UUID) error {
		_, err := f.svc.Review(f.ctx, a, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: reviewPR, Handoff: "PR open."})
		return err
	}},
	{name: "prepare", run: func(f *fixture, a uuid.UUID) error {
		_, err := f.svc.Prepare(f.ctx, a, "Needs reshaping.")
		return err
	}},
	{name: "extend scope", run: func(f *fixture, a uuid.UUID) error {
		_, err := f.svc.ExtendScope(f.ctx, a, []model.Path{{Path: "docs/"}})
		return err
	}},
	{name: "edit held", run: func(f *fixture, a uuid.UUID) error {
		_, err := f.svc.EditHeld(f.ctx, a, &ticket.EditParams{Title: ptr("Retitled")})
		return err
	}},
	{name: "integration lock", run: func(f *fixture, a uuid.UUID) error {
		return f.svc.AcquireIntegrationLock(f.ctx, a)
	}},
}

// A human taking a ticket back ends its lease under the claim lock, so an
// operation on the held ticket must look its lease up only once it holds
// that lock too. One that read the lease first, or skipped the lock, would
// act on a ticket the agent had already lost.
//
// A transaction here holds the claim lock, the operation is started, and
// once it is observed waiting on that lock the transaction ends the lease
// and commits. The operation must then find no lease and write nothing.
func TestHeldLeaseOps_ResolveTheLeaseOnlyUnderTheClaimLock(t *testing.T) {
	t.Parallel()
	for _, op := range heldLeaseOps {
		t.Run(op.name, func(t *testing.T) {
			f := newFixture(t)
			a := f.agent(t, "builder-1")
			held := f.ticket(t, "Held", "server/")
			f.claim(t, a)

			locked, takeBack := make(chan struct{}), make(chan struct{})
			var takeBackOnce sync.Once
			letHolderGo := func() { takeBackOnce.Do(func() { close(takeBack) }) }
			holderDone := make(chan error, 1)
			go func() {
				holderDone <- f.database.InTx(f.ctx, func(q db.Querier) error {
					if err := db.TakeAdvisoryLock(f.ctx, q, db.ClaimLockKey); err != nil {
						return err
					}
					close(locked)
					<-takeBack
					_, err := q.Exec(f.ctx, `
						UPDATE leases SET released_at = now(), release_reason = 'taken back'
						 WHERE agent_id = $1 AND released_at IS NULL`, a)
					return err
				})
			}()
			t.Cleanup(letHolderGo)
			<-locked

			opDone := make(chan error, 1)
			go func() { opDone <- op.run(f, a) }()

			f.awaitLockWaiters(t, 1, opDone)
			letHolderGo()
			require.NoError(t, <-holderDone)

			assert.ErrorIs(t, <-opDone, ticket.ErrNoLease,
				"the lease is looked up under the lock, after it was taken back")
			got := f.get(t, held.ID)
			assert.Equal(t, ticket.StatusInProgress, got.Status, "nothing was written")
			assert.Equal(t, "Held", got.Title)
			assert.Equal(t, []model.Path{{Path: "server/", Source: model.PathSourceFiled}}, got.Paths)
			assert.Empty(t, f.comments(t, held.ID))
			assert.Zero(t, f.lockCount(t), "no lock pinned to the ended lease")
		})
	}
}

// Only finishing a ticket frees what waited on it; every other way of
// handing it over, or working on it, leaves its dependents blocked.
func TestHeldLeaseOps_OnlyDoneUnblocksWhatWaitedOnTheTicket(t *testing.T) {
	t.Parallel()
	for _, op := range heldLeaseOps {
		t.Run(op.name, func(t *testing.T) {
			f := newFixture(t)
			a := f.agent(t, "builder-1")
			held := f.ticket(t, "Held", "server/")
			waiting := f.ticket(t, "Waits on the held one", "web/")
			f.blockedBy(t, waiting.ID, held.ID)
			require.Equal(t, ticket.StatusBlocked, f.status(t, waiting.ID))
			_, err := f.svc.Claim(f.ctx, a, &held.ID)
			require.NoError(t, err)

			require.NoError(t, op.run(f, a))

			want := ticket.StatusBlocked
			if op.unblocks {
				want = ticket.StatusOpen
			}
			assert.Equal(t, want, f.status(t, waiting.ID))
		})
	}
}

// The race the claim lock closes for the integration lock, end to end: an
// acquire that read its lease before a human took the ticket back, and
// inserted its lock after the revocation had dropped the lease's locks,
// would leave a lock on a released lease. Nothing sweeps that lock, so every
// other agent would be refused the integration lock indefinitely.
//
// A transaction here holds an uncommitted lock row under the same name,
// which parks the acquire on that row's insert -- after any lease lookup it
// makes outside the claim lock, but holding the claim lock if it took it
// first. The human then moves the ticket away and the row is rolled back.
// Under the claim lock the revocation queues behind the acquire and drops
// its lock; without it, the revocation finishes first and the lock outlives
// the lease.
func TestAcquireIntegrationLock_ARevocationRacingItLeavesNoOrphanedLock(t *testing.T) {
	f := newFixture(t)
	a, other := f.agent(t, "merger"), f.agent(t, "other")
	held := f.ticket(t, "Held", "server/")
	f.claim(t, a)
	f.ticket(t, "Other's", "web/")
	f.claim(t, other)

	rowHeld, rollBack := make(chan struct{}), make(chan struct{})
	var rollBackOnce sync.Once
	letRowGo := func() { rollBackOnce.Do(func() { close(rollBack) }) }
	errRolledBack := errors.New("rolled back")
	rowDone := make(chan error, 1)
	go func() {
		rowDone <- f.database.InTx(f.ctx, func(q db.Querier) error {
			if _, err := q.Exec(f.ctx, `
				INSERT INTO locks (name, holder_agent_id, holder_lease_id)
				SELECT $1, agent_id, id FROM leases
				 WHERE agent_id = $2 AND released_at IS NULL`, ticket.IntegrationLockName, other); err != nil {
				return err
			}
			close(rowHeld)
			<-rollBack
			return errRolledBack
		})
	}()
	t.Cleanup(letRowGo)
	select {
	case <-rowHeld:
	case err := <-rowDone:
		t.Fatalf("could not hold the competing lock row: %v", err)
	}

	acquired := make(chan error, 1)
	go func() { acquired <- f.svc.AcquireIntegrationLock(f.ctx, a) }()
	f.awaitLockWaiters(t, 1, acquired)

	revoked := make(chan error, 1)
	go func() {
		_, err := f.setStatus(t, held.ID, ticket.StatusOpen)
		revoked <- err
	}()
	// The revocation either finishes (nothing serializes it with the
	// acquire) or joins the acquire waiting on a lock; both are decided
	// before the row is rolled back.
	revokeFinished := false
	var revokeErr error
	deadline := time.Now().Add(10 * time.Second)
	for !revokeFinished && f.lockWaiters(t) < 2 {
		select {
		case revokeErr = <-revoked:
			revokeFinished = true
		case <-time.After(5 * time.Millisecond):
		}
		require.True(t, time.Now().Before(deadline), "the revocation neither waited nor finished")
	}
	letRowGo()
	require.ErrorIs(t, <-rowDone, errRolledBack)
	require.NoError(t, <-acquired, "the lease was live when the acquire took the claim lock")
	if !revokeFinished {
		revokeErr = <-revoked
	}
	require.NoError(t, revokeErr)

	var orphaned int
	require.NoError(t, f.database.QueryRow(f.ctx, `
		SELECT count(*) FROM locks k JOIN leases l ON l.id = k.holder_lease_id
		 WHERE l.released_at IS NOT NULL`).Scan(&orphaned))
	assert.Zero(t, orphaned, "a lock outlived the lease it pins")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, other), "the lock is free for the next agent")
}

// ReleaseLock has no lease to look up, but it writes locks under the claim
// lock like everything else that does, so a later check on the lock's lease
// is made against one no revocation can end mid-transaction.
func TestReleaseIntegrationLock_WaitsForTheClaimLock(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "merger")
	f.ticket(t, "Held", "server/")
	f.claim(t, a)
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, a))

	locked, unlock := make(chan struct{}), make(chan struct{})
	var unlockOnce sync.Once
	letHolderGo := func() { unlockOnce.Do(func() { close(unlock) }) }
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- f.database.InTx(f.ctx, func(q db.Querier) error {
			if err := db.TakeAdvisoryLock(f.ctx, q, db.ClaimLockKey); err != nil {
				return err
			}
			close(locked)
			<-unlock
			return nil
		})
	}()
	t.Cleanup(letHolderGo)
	<-locked

	released := make(chan error, 1)
	go func() { released <- f.svc.ReleaseIntegrationLock(f.ctx, a) }()
	f.awaitLockWaiters(t, 1, released)
	letHolderGo()
	require.NoError(t, <-holderDone)

	require.NoError(t, <-released)
	assert.Zero(t, f.lockCount(t))
}

// awaitLockWaiters returns once n sessions are waiting on a lock, failing
// the test if the operation reporting to done finishes first instead.
func (f *fixture) awaitLockWaiters(t *testing.T, n int, done <-chan error) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for f.lockWaiters(t) < n {
		select {
		case err := <-done:
			t.Fatalf("finished (err: %v) without waiting on a lock", err)
		case <-time.After(5 * time.Millisecond):
		}
		require.True(t, time.Now().Before(deadline), "the operation neither waited nor finished")
	}
}

func TestAcquireIntegrationLock_NeedsALease(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "merger")
	f.ticket(t, "Unclaimed", "server/")

	require.ErrorIs(t, f.svc.AcquireIntegrationLock(f.ctx, a), ticket.ErrNoLease)
	assert.Zero(t, f.lockCount(t), "no lock without a lease to pin")
}

// Extending a scope sweeps lapsed leases first, as a claim does: a dead
// agent's ticket goes back to the queue rather than staying in progress
// under nobody.
func TestExtendScope_ReturnsALapsedLeasesTicketToTheQueue(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	gone, live := f.agent(t, "gone"), f.agent(t, "live")
	lapsed := f.ticket(t, "Lapsed", "server/internal/db/")
	mine := f.ticket(t, "Mine", "server/internal/ticket/")
	_, err := f.svc.Claim(f.ctx, gone, &lapsed.ID)
	require.NoError(t, err)
	_, err = f.svc.Claim(f.ctx, live, &mine.ID)
	require.NoError(t, err)
	f.expire(t, gone)

	result, err := f.svc.ExtendScope(f.ctx, live, []model.Path{{Path: "server/internal/db/"}})
	require.NoError(t, err)

	assert.True(t, result.Granted, "a lapsed lease holds nothing")
	assert.Equal(t, ticket.StatusOpen, f.status(t, lapsed.ID))
}
