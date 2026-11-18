package landing_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

func (f *fixture) assertOldFlow(c *finished) {
	f.t.Helper()
	f.assertHandedBack(c, landing.MergeRefused)
	assert.Contains(f.t, f.comments(c.ticketID), "take integration_lock", "the guidance says to land it the old way")
	assert.Contains(f.t, f.comments(c.ticketID), "nothing is wrong with the change")
}

// Turning the queue off mid-flight is safe: the landing in hand finishes the
// step it is in (merge-main is not abandoned half-way), then it and every
// queued ticket go back to their workers for the old flow, and nothing merges.
func TestSwitchedOffMidFlight_FinishesTheStepThenHandsEveryTicketBack(t *testing.T) {
	f := newFixture(t)
	first := f.finishedChange(model.PriorityNormal)
	second := f.finishedChange(model.PriorityNormal)
	f.setChecks(first.ref, headA, false, green)
	f.host.BranchUpdated = codehost.BranchUpdate{SHA: headB}
	f.hostWrap.onUpdate = func(context.Context, string) {
		f.host.DiffOf[headB] = f.diff(baseNew)
		f.setChecks(first.ref, headB, true, green)
		f.on.Store(false)
	}

	f.land(f.runner(landing.Config{}))

	f.assertOldFlow(first)
	f.assertOldFlow(second)
	assert.EqualValues(t, 1, f.hostWrap.updates.Load(), "the step in hand was finished")
	assert.Empty(t, f.host.Merges(), "nothing merges once the queue is off")
	assert.Equal(t, landing.MergeRefused, f.outcomeOf(second.ticketID).Outcome)
}

func TestSwitchedOffBeforeTheTake_HandsBackWithoutTouchingTheHost(t *testing.T) {
	f := newFixture(t)
	a := f.finishedChange(model.PriorityNormal)
	b := f.finishedChange(model.PriorityNormal)
	f.on.Store(false)
	readsBefore := len(f.host.Checked())

	f.land(f.runner(landing.Config{}))

	f.assertOldFlow(a)
	f.assertOldFlow(b)
	assert.Len(t, f.host.Checked(), readsBefore, "no check was read, no lock taken")
	assert.Empty(t, f.host.Merges())
}

func TestQueue_ServesByPriorityWithAging(t *testing.T) {
	f := newFixture(t)
	low := f.finishedChange(model.PriorityLow)
	urgent := f.finishedChange(model.PriorityUrgent)
	normal := f.finishedChange(model.PriorityNormal)

	order := func(skip ...int64) []int64 {
		var got []int64
		for {
			row, err := f.queue.Head(f.ctx, append(skip, got...))
			require.NoError(t, err)
			if row == nil {
				return got
			}
			got = append(got, row.TicketID)
		}
	}
	assert.Equal(t, []int64{urgent.ticketID, normal.ticketID, low.ticketID}, order(), "priority first, not first come")

	// Six aging steps of waiting (the default step is 30 minutes) outrank urgent.
	_, err := f.database.Exec(f.ctx, `UPDATE landings SET updated_at = now() - interval '4 hours' WHERE ticket_id = $1`, low.ticketID)
	require.NoError(t, err)
	assert.Equal(t, []int64{low.ticketID, urgent.ticketID, normal.ticketID}, order(), "a waiter ages up so it is not starved")

	// A landing that has begun is never displaced by a better-ranked arrival.
	require.NoError(t, f.queue.SetStep(f.ctx, normal.ticketID, landing.MergingMain)) // a row begins at merge-main
	require.NoError(t, f.queue.SetStep(f.ctx, normal.ticketID, landing.WaitingChecks))
	row, err := f.queue.Head(f.ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, normal.ticketID, row.TicketID)
}

func TestQueue_TiesAreFirstComeAndHandedBackRowsAreNotServed(t *testing.T) {
	f := newFixture(t)
	a := f.finishedChange(model.PriorityNormal)
	b := f.finishedChange(model.PriorityNormal)
	row, err := f.queue.Head(f.ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, a.ticketID, row.TicketID)

	require.NoError(t, f.queue.HandBack(f.ctx, a.ticketID, landing.ChecksRed, false))
	row, err = f.queue.Head(f.ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, b.ticketID, row.TicketID)
}

func TestQueue_HeadCountsEveryOtherLandingEvenWhenSomeAreSkipped(t *testing.T) {
	f := newFixture(t)
	a := f.finishedChange(model.PriorityUrgent)
	f.finishedChange(model.PriorityNormal)
	handedBack := f.finishedChange(model.PriorityLow)
	require.NoError(t, f.queue.HandBack(f.ctx, handedBack.ticketID, landing.ChecksRed, false))

	row, err := f.queue.Head(f.ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, row.QueueDepth, "a row handed back is not waiting")

	row, err = f.queue.Head(f.ctx, []int64{a.ticketID})
	require.NoError(t, err)
	assert.Equal(t, 1, row.QueueDepth, "a skipped row still waits")
}

func TestQueue_OrderedCarriesTheBatchAMemberIsIn(t *testing.T) {
	f := newFixture(t)
	a := f.finishedChange(model.PriorityNormal)
	b := f.finishedChange(model.PriorityNormal)
	var batch int64
	require.NoError(t, f.database.QueryRow(f.ctx, `
INSERT INTO batches (base_sha, branch, member_ticket_ids, state) VALUES ('abc', 'batch-1', $1, 'testing') RETURNING id`,
		[]int64{a.ticketID, b.ticketID}).Scan(&batch))
	_, err := f.database.Exec(f.ctx, `UPDATE landings SET batch_id = $1, batch_position = 2, queue_state = 'batch_testing' WHERE ticket_id = $2`, batch, b.ticketID)
	require.NoError(t, err)

	rows, err := f.queue.Ordered(f.ctx)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	byTicket := map[int64]landing.LandingRow{rows[0].TicketID: rows[0], rows[1].TicketID: rows[1]}
	assert.Nil(t, byTicket[a.ticketID].BatchID)
	assert.Zero(t, byTicket[a.ticketID].BatchPosition)
	require.NotNil(t, byTicket[b.ticketID].BatchID)
	assert.Equal(t, batch, *byTicket[b.ticketID].BatchID)
	assert.Equal(t, 2, byTicket[b.ticketID].BatchPosition)
}
