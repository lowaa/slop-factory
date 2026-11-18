package schema_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// While a deploy rolls, a pod of the previous release writes `tickets` (the
// status trigger then writes the attempt) and a pod of this one writes the
// ticket and then its attempt through the store. A write that took the
// attempt first would deadlock with the trigger's, so the store takes the
// ticket's row first.
//
// The old pod holds the ticket's row and waits until the new pod's write is
// observed blocked on a lock -- the barrier -- then writes. With the store
// locking the ticket first, the new pod is waiting for the ticket and both
// finish. A store that wrote the attempt first would be holding it, blocked
// on the ticket: the old pod's trigger then waits for the attempt, and the
// database kills one of them.
func TestAnOldPodAndTheStoreWritingOneTicketDoNotDeadlock(t *testing.T) {
	database := dbtest.WithDB(t)
	ctx := db.WithTenant(context.Background(), uuid.New())
	svc := ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler)
	created, err := svc.Create(ctx, &ticket.CreateParams{Title: "one"})
	require.NoError(t, err)

	oldHolds := make(chan struct{})
	var wg sync.WaitGroup
	var oldErr, newErr error

	wg.Add(2)
	go func() {
		defer wg.Done()
		oldErr = database.InTx(ctx, func(q db.Querier) error {
			if _, err := q.Exec(ctx, `SELECT 1 FROM tickets WHERE id = $1 FOR UPDATE`, created.ID); err != nil {
				return err
			}
			close(oldHolds)
			if !waitFor(10*time.Second, func() bool { return lockWaiters(ctx, database) > 0 }) {
				t.Error("the store's write never blocked on the old pod's lock")
			}
			_, err := q.Exec(ctx, `UPDATE tickets SET status = 'in_progress', updated_at = now() WHERE id = $1`, created.ID)
			return err
		})
	}()
	go func() {
		defer wg.Done()
		<-oldHolds
		pr := "https://github.example/pull/new"
		newErr = svc.InTransition(ctx, func(tx ticket.TransitionTx) error {
			return tx.WriteTicket(ctx, created.ID, &ticket.TicketWrite{PRURL: &pr})
		})
	}()
	wg.Wait()

	assert.NoError(t, oldErr, "the old pod's write finishes")
	assert.NoError(t, newErr, "and so does the store's")
}

func lockWaiters(ctx context.Context, database *db.DB) int {
	var n int
	_ = database.QueryRow(ctx, `
		SELECT count(*) FROM pg_stat_activity
		 WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&n)
	return n
}

// waitFor polls cond, rather than sleeping a fixed time, until it holds.
func waitFor(limit time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
