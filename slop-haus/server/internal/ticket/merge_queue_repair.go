package ticket

import (
	"context"
	"fmt"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// RepairMergeQueue restores the invariant that an attempt is in_merge_queue
// exactly when its ticket has a landings row that is not handed back. The two are
// written together (Land, ServerHandBack, a person's move), so a break means a
// crash between writes, or a row the release before the status existed
// enqueued. A row or attempt changed within grace is left alone: it may be a
// write still in flight in another replica.
//
//   - a landing row whose attempt is queued is taken into the merge queue: the
//     author's finish put it there, and a crash after ServerHandBack but before
//     the row was marked leaves the same picture, which the landing then
//     retries and hands back again if it still fails;
//   - a landing row whose attempt is anything else (a worker holds it, it is
//     done, closed, in review), or whose ticket is done or cancelled, is
//     dropped through MergeQueue, which records a done ticket's merge;
//   - an attempt in the merge queue with no landing row, and no lease on it, is
//     put back in the queue for its author.
//
// A ticket with nothing live and nothing promoted (its only attempt closed)
// has no status to derive, so only its row is dropped.
//
// It returns the tickets it changed.
func (s *PostgresStore) RepairMergeQueue(ctx context.Context, grace time.Duration) ([]int64, error) {
	var repaired []int64
	err := s.underClaimLock(ctx, func(q db.Querier) error {
		repaired = nil
		if err := systemMover("merge queue repaired").set(ctx, q); err != nil {
			return err
		}
		interval := grace.String()
		// Tickets first, in id order, then their attempts: the lock order
		// AttemptRef names.
		ids, err := updatedIDs(ctx, q, `
			SELECT t.id FROM tickets t
			 WHERE EXISTS (
			         SELECT 1 FROM landings w JOIN attempts a ON a.ticket_id = w.ticket_id AND a.number = w.attempt_number
			          WHERE w.ticket_id = t.id AND w.queue_state <> 'back_with_agent' AND w.updated_at < now() - $1::interval
			            AND (a.status <> 'in_merge_queue' OR t.status IN ('done', 'cancelled')))
			    OR EXISTS (
			         SELECT 1 FROM attempts a
			          WHERE a.ticket_id = t.id AND a.status = 'in_merge_queue' AND a.updated_at < now() - $1::interval
			            AND NOT EXISTS (SELECT 1 FROM landings w WHERE w.ticket_id = a.ticket_id
			                              AND w.attempt_number = a.number AND w.queue_state <> 'back_with_agent')
			            AND NOT EXISTS (SELECT 1 FROM leases l WHERE l.ticket_id = a.ticket_id
			                              AND l.attempt_number = a.number AND `+activeLeaseClause+`))
			 ORDER BY t.id FOR UPDATE OF t`, interval)
		if err != nil {
			return fmt.Errorf("find broken merge queue entries: %w", err)
		}
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			if _, err := s.mergeQueue.Dequeue(ctx, q, &DequeueRequest{
				TicketID: id, Reason: DequeueFinished, StaleFor: grace,
				Detail: "merged outside the queue; row removed by the repair",
			}); err != nil {
				return fmt.Errorf("drop landings of attempts out of the queue: %w", err)
			}
		}
		if _, err := q.Exec(ctx, `
			UPDATE attempts a SET status = 'in_merge_queue', updated_at = now()
			  FROM landings w
			 WHERE a.ticket_id = ANY($1) AND a.status = 'queued' AND a.promoted_at IS NOT NULL
			   AND w.ticket_id = a.ticket_id AND w.attempt_number = a.number
			   AND w.queue_state <> 'back_with_agent' AND w.updated_at < now() - $2::interval`, ids, interval); err != nil {
			return fmt.Errorf("take landing attempts into the merge queue: %w", err)
		}
		if _, err := q.Exec(ctx, `
			UPDATE attempts a SET status = 'queued', updated_at = now()
			 WHERE a.ticket_id = ANY($1) AND a.status = 'in_merge_queue' AND a.updated_at < now() - $2::interval
			   AND NOT EXISTS (SELECT 1 FROM landings w WHERE w.ticket_id = a.ticket_id
			                     AND w.attempt_number = a.number AND w.queue_state <> 'back_with_agent')
			   AND NOT EXISTS (SELECT 1 FROM leases l WHERE l.ticket_id = a.ticket_id
			                     AND l.attempt_number = a.number AND `+activeLeaseClause+`)`, ids, interval); err != nil {
			return fmt.Errorf("requeue attempts that left the merge queue: %w", err)
		}
		repaired, err = deriveStatus(ctx, q, `t.id = ANY($1) AND `+inQueueLifecycle+` AND (`+derivedStatus+`) IS NOT NULL`, ids)
		return err
	})
	return repaired, err
}
