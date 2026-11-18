package landing_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
)

func (b *batchFixture) phaseOf(id int64) string {
	b.t.Helper()
	var p string
	require.NoError(b.t, b.database.QueryRow(b.ctx, `SELECT queue_state FROM landings WHERE ticket_id = $1`, id).Scan(&p))
	return p
}

func (b *batchFixture) resultOf(id int64) (result, reason string) {
	b.t.Helper()
	require.NoError(b.t, b.database.QueryRow(b.ctx, `SELECT coalesce(result, ''), coalesce(result_reason, '') FROM batches WHERE id = $1`, id).Scan(&result, &reason))
	return result, reason
}

func TestBuild_NoMemberReadsAsBatchedWhileTheBuildCanStillFail(t *testing.T) {
	b := newBatch(t, 3)
	var seen []string
	b.host.observe = func() {
		for _, id := range b.ids() {
			if row := b.rowOf(id); row.BatchID != nil || row.Step != "" {
				seen = append(seen, b.phaseOf(id))
			}
			assert.Equal(b.t, "queued", b.phaseOf(id))
		}
	}

	built := b.build()

	require.NotNil(t, built)
	assert.Empty(t, seen)
	assert.NotEmpty(t, b.host.updated, "updateMember ran, with every member still queued")
	for _, id := range b.ids() {
		assert.Equal(t, "batch_testing", b.phaseOf(id), "reserved once nothing can fail")
	}
}

func TestBuild_AFailureAfterTheMergesLeavesEveryMemberQueued(t *testing.T) {
	b := newBatch(t, 3)
	b.host.openErr = errors.New("host down")

	built, err := b.builder.Build(b.ctx, b.candidates())

	require.Error(t, err)
	assert.Nil(t, built)
	for _, id := range b.ids() {
		row := b.rowOf(id)
		if row.TicketID == b.ids()[0] {
			continue // the holder is handed back for a host error
		}
		assert.Equal(t, "queued", b.phaseOf(id))
		assert.Nil(t, row.BatchID, "no half-built batch")
	}
	result, reason := b.resultOf(1)
	assert.Equal(t, "voided", result)
	assert.Contains(t, reason, "open the batch's change")
}

func TestBuild_AMemberThatLeavesDuringTheBuildVoidsTheBatchAndKeepsTheOthersQueued(t *testing.T) {
	b := newBatch(t, 3)
	left := false
	b.host.observe = func() {
		if left {
			return
		}
		left = true
		require.NoError(b.t, b.queue.Remove(b.ctx, b.changes[2].ticketID, landing.PhaseDequeued))
	}

	built, err := b.builder.Build(b.ctx, b.candidates())

	require.ErrorIs(t, err, landing.ErrNotReservable)
	assert.Nil(t, built)
	for _, id := range b.ids()[:2] {
		assert.Equal(t, "queued", b.phaseOf(id))
		assert.Nil(t, b.rowOf(id).BatchID)
	}
	result, reason := b.resultOf(1)
	assert.Equal(t, "voided", result)
	assert.Contains(t, reason, "left the queue")
}

func TestBatchStore_VoidIsRefusedOnceTheBatchEnded(t *testing.T) {
	b := newBatch(t, 2)
	batch, err := reserveNow(b.ctx, b.store, baseOld, b.ids())
	require.NoError(t, err)
	require.NoError(t, b.store.Void(b.ctx, batch.ID, "first"))
	// A later reservation takes the same members.
	next, err := reserveNow(b.ctx, b.store, baseOld, b.ids())
	require.NoError(t, err)

	err = b.store.Void(b.ctx, batch.ID, "second")

	require.ErrorIs(t, err, landing.ErrBatchTransition)
	for _, id := range b.ids() {
		require.NotNil(t, b.rowOf(id).BatchID)
		assert.Equal(t, next.ID, *b.rowOf(id).BatchID, "the ended batch cannot release another batch's members")
	}
	_, reason := b.resultOf(batch.ID)
	assert.Equal(t, "first", reason)
	require.ErrorIs(t, b.store.Void(b.ctx, batch.ID+100, "x"), landing.ErrNoBatch)
}

func TestBatchStore_VoidReleasesTestingAndMergingMembers(t *testing.T) {
	b := newBatch(t, 3)
	batch, err := reserveNow(b.ctx, b.store, baseOld, b.ids())
	require.NoError(t, err)
	require.NoError(t, b.queue.SetStep(b.ctx, b.changes[0].ticketID, landing.Merging))
	require.Equal(t, "batch_merging", b.phaseOf(b.changes[0].ticketID))

	require.NoError(t, b.store.Void(b.ctx, batch.ID, "main moved"))

	for _, id := range b.ids() {
		assert.Equal(t, "queued", b.phaseOf(id))
		assert.Nil(t, b.rowOf(id).BatchID)
	}
	result, reason := b.resultOf(batch.ID)
	assert.Equal(t, "voided", result)
	assert.Equal(t, "main moved", reason)
}

func TestBatchStore_ReleaseOnlyTakesAMemberOfThatBatch(t *testing.T) {
	b := newBatch(t, 2)
	batch, err := reserveNow(b.ctx, b.store, baseOld, b.ids()[:1])
	require.NoError(t, err)

	require.ErrorContains(t, b.store.Release(b.ctx, batch.ID, b.changes[1].ticketID), "not a member", "a queued row is not released")
	require.ErrorContains(t, b.store.Release(b.ctx, batch.ID+1, b.changes[0].ticketID), "not a member")
	require.NoError(t, b.store.Release(b.ctx, batch.ID, b.changes[0].ticketID))
	assert.Equal(t, "queued", b.phaseOf(b.changes[0].ticketID))
	require.Error(t, b.store.Release(b.ctx, batch.ID, b.changes[0].ticketID), "released once")
}

func TestBatchStore_ReleaseAndVoidEachSucceedOnlyWhileTheMemberIsReserved(t *testing.T) {
	t.Run("release then void", func(t *testing.T) {
		b := newBatch(t, 2)
		batch, err := reserveNow(b.ctx, b.store, baseOld, b.ids())
		require.NoError(t, err)

		require.NoError(t, b.store.Release(b.ctx, batch.ID, b.changes[0].ticketID))
		require.NoError(t, b.store.Void(b.ctx, batch.ID, "after"))

		for _, id := range b.ids() {
			assert.Equal(t, "queued", b.phaseOf(id))
		}
	})
	t.Run("void then release", func(t *testing.T) {
		b := newBatch(t, 2)
		batch, err := reserveNow(b.ctx, b.store, baseOld, b.ids())
		require.NoError(t, err)
		require.NoError(t, b.store.Void(b.ctx, batch.ID, "first"))

		require.ErrorContains(t, b.store.Release(b.ctx, batch.ID, b.changes[0].ticketID), "not a member")
	})
}

// assertLostToVoid accepts either refusal a release that lost to Void gets.
// Void committed before the release read the row, which then has no batch; or
// the release read the reserved row first and its compare-and-set waited on
// Void's lock, finding the row queued once Void committed.
func assertLostToVoid(t *testing.T, err error, ticketID int64) {
	t.Helper()
	var c *landing.ConflictError
	if errors.As(err, &c) {
		assert.Equal(t, landing.ConflictError{TicketID: ticketID, From: landing.PhaseBatchTesting, To: landing.PhaseQueued, Actual: landing.PhaseQueued}, *c,
			"a release that read the row before Void committed finds it queued")
		return
	}
	assert.ErrorContains(t, err, "not a member", "a release that read the row after Void committed is told so")
}

func TestBatchStore_ConcurrentReleaseAndVoidNeverLeaveAMemberReserved(t *testing.T) {
	for range 5 {
		b := newBatch(t, 2)
		batch, err := reserveNow(b.ctx, b.store, baseOld, b.ids())
		require.NoError(t, err)
		var wg sync.WaitGroup
		start := make(chan struct{})
		var relErr, voidErr error
		wg.Go(func() { <-start; relErr = b.store.Release(b.ctx, batch.ID, b.changes[0].ticketID) })
		wg.Go(func() { <-start; voidErr = b.store.Void(b.ctx, batch.ID, "race") })
		close(start)
		wg.Wait()

		require.NoError(t, voidErr, "Void waits for the member's lock and releases what is left")
		if relErr != nil {
			assertLostToVoid(t, relErr, b.changes[0].ticketID)
		}
		for _, id := range b.ids() {
			assert.Equal(t, "queued", b.phaseOf(id))
		}
	}
}

// The race above takes this path only now and then, so it is forced here:
// Void is held between locking the first member and the second, and the
// release reads the first while Void has it.
func TestBatchStore_AReleaseThatReadTheRowBeforeVoidCommittedFindsItQueued(t *testing.T) {
	b := newBatch(t, 2)
	batch, err := reserveNow(b.ctx, b.store, baseOld, b.ids())
	require.NoError(t, err)
	first, second := b.changes[0].ticketID, b.changes[1].ticketID
	unlock := b.holdRow(second)
	defer unlock()

	voidErr := make(chan error, 1)
	go func() { voidErr <- b.store.Void(b.ctx, batch.ID, "race") }()
	b.awaitLockWaiters(1) // Void has the first member and waits for the second
	relErr := make(chan error, 1)
	go func() { relErr <- b.store.Release(b.ctx, batch.ID, first) }()
	b.awaitLockWaiters(2) // the release read the first reserved and waits on Void
	unlock()

	require.NoError(t, <-voidErr)
	err = <-relErr
	var c *landing.ConflictError
	require.ErrorAs(t, err, &c, "this interleaving is the compare-and-set's refusal")
	assertLostToVoid(t, err, first)
	for _, id := range b.ids() {
		assert.Equal(t, "queued", b.phaseOf(id))
	}
}

// holdRow locks the ticket's landing row in a transaction of its own until the
// returned func is called; calling it again is a no-op.
func (b *batchFixture) holdRow(ticketID int64) (unlock func()) {
	b.t.Helper()
	locked, release := make(chan error, 1), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- b.database.InTx(b.ctx, func(q db.Querier) error {
			_, err := q.Exec(b.ctx, `SELECT 1 FROM landings WHERE ticket_id = $1 FOR UPDATE`, ticketID)
			locked <- err
			if err != nil {
				return err
			}
			<-release
			return nil
		})
	}()
	require.NoError(b.t, <-locked, "hold the row")
	var once sync.Once
	return func() {
		once.Do(func() {
			close(release)
			require.NoError(b.t, <-done)
		})
	}
}

// awaitLockWaiters waits until n sessions on this test's database wait on a
// lock: the point a forced interleaving has reached. The count is exact, not
// at least n, because each step waits for one more waiter than the last; the
// database is this test's alone, so nothing else waits in it.
func (b *batchFixture) awaitLockWaiters(n int) {
	b.t.Helper()
	require.Eventually(b.t, func() bool {
		var waiting int
		// assert, not require: the condition runs off the test goroutine.
		err := b.database.QueryRow(b.ctx, `
SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting)
		return assert.NoError(b.t, err) && waiting == n
	}, 30*time.Second, 5*time.Millisecond)
}
