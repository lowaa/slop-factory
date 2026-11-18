package ticket_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// recordingQueue removes nothing: it proves the store reaches the landing
// queue only through the port.
type recordingQueue struct{ calls []ticket.DequeueRequest }

func (r *recordingQueue) Dequeue(_ context.Context, _ db.Querier, req *ticket.DequeueRequest) (bool, error) {
	r.calls = append(r.calls, *req)
	return false, nil
}

func TestStoreDropsLandingRowsOnlyThroughTheMergeQueuePort(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Lands", "server/")
	f.land(t, f.agent(t, "author"), created.ID)
	f.setAttemptStatus(t, created.ID, 1, "in_progress")
	rq := &recordingQueue{}
	store := ticket.NewPostgresStore(f.database, ticket.WithMergeQueue(rq))

	_, err := store.RepairMergeQueue(f.ctx, 0)
	require.NoError(t, err)
	require.Len(t, rq.calls, 1)
	assert.Equal(t, ticket.DequeueFinished, rq.calls[0].Reason)
	assert.Equal(t, created.ID, rq.calls[0].TicketID)
	assert.Equal(t, 1, f.landingRows(t, created.ID), "the store deleted nothing itself")

	require.NoError(t, store.InTransition(f.ctx, func(tx ticket.TransitionTx) error {
		_, err := tx.Dequeue(f.ctx, &ticket.DequeueRequest{TicketID: created.ID, Reason: ticket.DequeueMergedOutside})
		return err
	}))
	require.Len(t, rq.calls, 2)
	assert.Equal(t, ticket.DequeueMergedOutside, rq.calls[1].Reason)
}
