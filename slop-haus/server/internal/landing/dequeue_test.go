package landing_test

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// queued is a ticket with a promoted attempt and an unbegun landing row.
func (f *storeFixture) queued(t *testing.T, ticketStatus, attemptStatus string) int64 {
	t.Helper()
	id := f.ticket(t)
	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET status = $2 WHERE id = $1`, id, ticketStatus)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `UPDATE attempts SET status = $2 WHERE ticket_id = $1 AND number = 1`, id, attemptStatus)
	require.NoError(t, err)
	require.NoError(t, landing.NewLandingStore(f.database).Enqueue(f.ctx, landingRow(id, "h1")))
	return id
}

func (f *storeFixture) dequeue(t *testing.T, id int64, reason ticket.DequeueReason) bool {
	t.Helper()
	var removed bool
	require.NoError(t, f.database.InTx(f.ctx, func(q db.Querier) error {
		var err error
		removed, err = landing.NewDequeuer().Dequeue(f.ctx, q, &ticket.DequeueRequest{TicketID: id, Reason: reason, Detail: "d"})
		return err
	}))
	return removed
}

func (f *storeFixture) merged(t *testing.T, id int64) int {
	t.Helper()
	var n int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM server_merge_outcomes WHERE ticket_id = $1 AND outcome = 'merged'`, id).Scan(&n))
	return n
}

func (f *storeFixture) hasRow(t *testing.T, id int64) bool {
	t.Helper()
	var n int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM landings WHERE ticket_id = $1`, id).Scan(&n))
	return n == 1
}

func TestDequeue_MergedOutsideRemovesTheRowAndRecordsTheMergeOnce(t *testing.T) {
	f := newStoreFixture(t)
	id := f.queued(t, "open", "in_merge_queue")

	assert.True(t, f.dequeue(t, id, ticket.DequeueMergedOutside))
	assert.False(t, f.hasRow(t, id))
	assert.Equal(t, 1, f.merged(t, id))

	// A landing re-enqueued and dequeued again must not double count.
	require.NoError(t, landing.NewLandingStore(f.database).Enqueue(f.ctx, landingRow(id, "h2")))
	assert.True(t, f.dequeue(t, id, ticket.DequeueMergedOutside))
	assert.Equal(t, 1, f.merged(t, id), "the merge is recorded once")
}

func TestDequeue_LeavesABegunRowAlone(t *testing.T) {
	f := newStoreFixture(t)
	id := f.queued(t, "open", "in_merge_queue")
	require.NoError(t, landing.NewLandingStore(f.database).SetStep(f.ctx, id, landing.MergingMain))

	assert.False(t, f.dequeue(t, id, ticket.DequeueMergedOutside))
	assert.True(t, f.hasRow(t, id))
	assert.Zero(t, f.merged(t, id))
}

func TestDequeue_LeftQueueDropsOnlyARowWhoseAttemptLeftAndRecordsNothing(t *testing.T) {
	f := newStoreFixture(t)
	in := f.queued(t, "open", "in_merge_queue")
	out := f.queued(t, "open", "in_progress")

	assert.False(t, f.dequeue(t, in, ticket.DequeueLeftQueue))
	assert.True(t, f.dequeue(t, out, ticket.DequeueLeftQueue))
	assert.True(t, f.hasRow(t, in))
	assert.False(t, f.hasRow(t, out))
	assert.Zero(t, f.merged(t, out))
}

func TestDequeue_FinishedRecordsAMergeOnlyForADoneTicket(t *testing.T) {
	f := newStoreFixture(t)
	done := f.queued(t, "done", "in_merge_queue")
	cancelled := f.queued(t, "cancelled", "in_merge_queue")
	live := f.queued(t, "open", "queued")

	assert.True(t, f.dequeue(t, done, ticket.DequeueFinished))
	assert.True(t, f.dequeue(t, cancelled, ticket.DequeueFinished))
	assert.False(t, f.dequeue(t, live, ticket.DequeueFinished), "a live queued ticket keeps its row")
	assert.Equal(t, 1, f.merged(t, done))
	assert.Zero(t, f.merged(t, cancelled))
	assert.True(t, f.hasRow(t, live))
}

func TestDequeue_StaleForLeavesARecentRowAlone(t *testing.T) {
	f := newStoreFixture(t)
	id := f.queued(t, "done", "done")

	var removed bool
	require.NoError(t, f.database.InTx(f.ctx, func(q db.Querier) error {
		var err error
		removed, err = landing.NewDequeuer().Dequeue(f.ctx, q, &ticket.DequeueRequest{TicketID: id, Reason: ticket.DequeueFinished, StaleFor: time.Hour})
		return err
	}))
	assert.False(t, removed)
	assert.True(t, f.hasRow(t, id))
}

func TestDequeue_AnAlreadyMergedOutcomeStopsASecondRecord(t *testing.T) {
	f := newStoreFixture(t)
	id := f.queued(t, "open", "in_merge_queue")
	require.NoError(t, f.store.Record(f.ctx, rec(id, landing.AlreadyMerged)))

	assert.True(t, f.dequeue(t, id, ticket.DequeueMergedOutside))
	assert.Zero(t, f.merged(t, id))
}
