package landing_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// A batch left testing by a stopped runner whose first member cannot be claimed
// (its old lease is still held) is not carried on, and the queue behind it must
// not be landed serially into its app meanwhile.
func TestBatchLane_ALaterQueuedLandingInTheAppOfATestingBatchWaitsAndAnotherAppDoesNot(t *testing.T) {
	b, cs := newBatchRun(t, 2, "app-a/server/", "app-a/server/")
	b.crashWhileTesting(0)
	sameApp := b.finishedChangeAt(model.PriorityUrgent, "app-a/web/")
	otherApp := b.finishedChangeAt(model.PriorityNormal, "app-b/server/")
	b.enqueue(sameApp)
	b.enqueue(otherApp)
	// The batch's first member is not claimable, as when another replica's lease on it has not run out.
	b.delay(cs[0].ticketID)

	b.land(b.newRunner())

	assert.Equal(t, ticket.StatusDone, b.status(otherApp.ticketID), "another independent app does not wait")
	assert.NotEqual(t, ticket.StatusDone, b.status(sameApp.ticketID), "the same app waits behind the testing batch")
	assert.NotContains(t, b.world.merges, sameApp.ticketID)
	live, err := b.store.Live(b.ctx)
	require.NoError(t, err)
	assert.Len(t, live, 1, "the batch was left alone")
}

func (f *fixture) status(id int64) ticket.Status {
	f.t.Helper()
	got, err := f.tickets.Get(f.ctx, id)
	require.NoError(f.t, err)
	return got.Status
}
