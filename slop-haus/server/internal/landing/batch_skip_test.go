package landing_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

func TestBatchRun_ATicketSkippedAsNotClaimableIsTriedAgainAfterABatchRuns(t *testing.T) {
	f := newFixture(t)
	w := newBatchWorld(f)
	// a is the head but alone in its app, so it is landed serially, and is not
	// claimable then: it is skipped while the batch of b and c is formed.
	a := f.finishedChangeAt(model.PriorityUrgent, "app-b/server/")
	b := f.finishedChangeAt(model.PriorityNormal, "app-a/server/")
	c := f.finishedChangeAt(model.PriorityNormal, "app-a/server/")
	for _, x := range []*finished{a, b, c} {
		f.enqueue(x)
	}
	f.delay(a.ticketID)
	br := &batchRunFixture{fixture: f, world: w, store: landing.NewBatchStore(f.database), size: 2, told: &membersFake{}}
	// a becomes claimable while the batch runs.
	cleared := false
	w.onBatch = func(string) {
		if !cleared {
			cleared = true
			f.undelay(a.ticketID)
		}
	}
	r := br.newRunner()

	f.land(r)

	require.True(t, cleared, "the batch ran")
	assert.Equal(t, []int64{b.ticketID, c.ticketID}, w.merges, "b and c landed as a batch")
	got, err := f.tickets.Get(f.ctx, a.ticketID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDone, got.Status, "a was skipped before the batch and is a candidate again after it")
}
