package ticket

import (
	"context"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// DequeueReason is why a queue row is being removed, which decides what may
// remove it and whether the merge is recorded.
type DequeueReason int

const (
	// DequeueMergedOutside: a person merged the change themselves. The merge
	// is recorded so Reports and the queue metrics count it.
	DequeueMergedOutside DequeueReason = iota + 1
	// DequeueLeftQueue: the attempt is no longer in the merge queue (a person
	// moved the ticket out). Nothing is recorded.
	DequeueLeftQueue
	// DequeueFinished: the ticket or its attempt is already finished, or the
	// attempt left the queue, and the row stayed. The merge is recorded when
	// the ticket is done; a cancelled ticket records nothing, the outcomes
	// have no code for it.
	DequeueFinished
)

// DequeueRequest asks for one ticket's queue row to be removed.
type DequeueRequest struct {
	TicketID int64
	Reason   DequeueReason
	// Detail is the recorded merge's detail.
	Detail string
	// StaleFor leaves a row changed more recently than this alone, as it may
	// be a write still in flight in another replica.
	StaleFor time.Duration
}

// noMergeQueue is the store's queue until WithMergeQueue sets the real one.
// ticket cannot import landing, which owns the queue's tables, so a store
// built without it (a tool or test that never has a landing row) removes
// nothing. The server wires landing.Dequeuer.
type noMergeQueue struct{}

func (noMergeQueue) Dequeue(context.Context, db.Querier, *DequeueRequest) (bool, error) {
	return false, nil
}
