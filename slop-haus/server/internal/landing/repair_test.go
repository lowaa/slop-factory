package landing_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

func (f *fixture) setTicketStatus(id int64, status string) {
	f.t.Helper()
	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET status = $2 WHERE id = $1`, id, status)
	require.NoError(f.t, err)
	if status == "done" {
		_, err = f.database.Exec(f.ctx, `UPDATE attempts SET status = 'done' WHERE ticket_id = $1`, id)
		require.NoError(f.t, err)
	}
}

func (f *fixture) repair() {
	f.t.Helper()
	_, err := f.store.RepairMergeQueue(f.ctx, 0)
	require.NoError(f.t, err)
}

func (f *fixture) outcomesOf(id int64) []landing.Record {
	var out []landing.Record
	for _, r := range f.outcomesNow() {
		if r.TicketID == id {
			out = append(out, r)
		}
	}
	return out
}

func TestRepair_RemovesAnUnbegunRowOfADoneTicketAndRecordsTheMerge(t *testing.T) {
	f := newFixture(t)
	done := f.finishedChange(model.PriorityNormal)
	cancelled := f.finishedChange(model.PriorityNormal)
	live := f.finishedChange(model.PriorityNormal)
	f.setTicketStatus(done.ticketID, "done")
	f.setTicketStatus(cancelled.ticketID, "cancelled")

	f.repair()

	assert.Nil(t, f.row(done.ticketID))
	assert.Nil(t, f.row(cancelled.ticketID))
	require.Len(t, f.outcomesOf(done.ticketID), 1)
	assert.Equal(t, landing.Merged, f.outcomesOf(done.ticketID)[0].Outcome)
	assert.Empty(t, f.outcomesOf(cancelled.ticketID))
	require.NotNil(t, f.row(live.ticketID), "a queued ticket that is live keeps its row")
	assert.Equal(t, landing.Landing, f.row(live.ticketID).State)

	f.repair()
	assert.Len(t, f.outcomesOf(done.ticketID), 1)
}

func TestRepair_RecordsNoSecondMerge(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setTicketStatus(c.ticketID, "done")
	require.NoError(t, f.outcomes.Record(f.ctx, &landing.Record{TicketID: c.ticketID, AttemptNumber: c.attempt, ChangeURL: f.hostWrapURL(c.ref), HeadSHA: headA, Outcome: landing.Merged}))

	f.repair()

	assert.Nil(t, f.row(c.ticketID))
	assert.Len(t, f.outcomesOf(c.ticketID), 1)
}

func TestRepair_LeavesABegunRowAlone(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	require.NoError(t, f.queue.SetStep(f.ctx, c.ticketID, landing.MergingMain))
	f.setTicketStatus(c.ticketID, "done")

	f.repair()

	require.NotNil(t, f.row(c.ticketID))
}

func TestRepair_LeavesARowInALiveBatchAlone(t *testing.T) {
	b := newBatch(t, 2)
	batch, err := reserveNow(b.ctx, b.store, baseOld, b.ids())
	require.NoError(t, err)
	b.setTicketStatus(b.changes[0].ticketID, "done")

	b.repair()
	require.NotNil(t, b.row(b.changes[0].ticketID))

	require.NoError(t, b.store.Void(b.ctx, batch.ID, "test"))
	b.repair()
	assert.Nil(t, b.row(b.changes[0].ticketID), "once the batch is done the row is removable")
}

func TestRunner_RepairsBeforeTakingTheQueue(t *testing.T) {
	f := newFixture(t)
	stale := f.finishedChange(model.PriorityNormal)
	f.setTicketStatus(stale.ticketID, "done")
	// The sweep leaves a row newer than its grace alone.
	_, err := f.database.Exec(f.ctx, `UPDATE landings SET updated_at = now() - interval '1 hour'`)
	require.NoError(t, err)
	r := f.runner(landing.Config{})

	f.land(r)

	assert.Nil(t, f.row(stale.ticketID))
	require.Len(t, f.outcomesOf(stale.ticketID), 1)
	assert.Contains(t, f.outcomesOf(stale.ticketID)[0].Detail, "repair")
}

func TestRepair_RemovesTheRowOfAnOverAttemptOnALiveTicket(t *testing.T) {
	for _, status := range []string{"done", "closed"} {
		t.Run(status, func(t *testing.T) {
			f := newFixture(t)
			c := f.finishedChange(model.PriorityNormal)
			_, err := f.database.Exec(f.ctx, `UPDATE attempts SET status = $3 WHERE ticket_id = $1 AND number = $2`, c.ticketID, c.attempt, status)
			require.NoError(t, err)

			f.repair()

			assert.Nil(t, f.row(c.ticketID))
		})
	}
}
