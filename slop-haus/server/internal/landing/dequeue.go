package landing

import (
	"context"
	"fmt"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Dequeuer is the SQL side of the ticket package's MergeQueue port.
type Dequeuer struct{}

func NewDequeuer() *Dequeuer { return &Dequeuer{} }

// dequeueSQL is one statement so the delete and the outcome are one unit, and
// the delete locks the row: a runner that takes it as head at the same moment
// waits on the caller's transaction and finds it gone.
//
// A row that has begun (it has a step) is the runner's, which reconciles it,
// and a row reserved by a batch that is not done belongs to that batch's
// runner; neither is touched. The merge is recorded only if none was recorded
// for the ticket already (by a landing, or an earlier dequeue), so recording
// it is idempotent.
//
// $3 is the reason's row condition, $4 its recording rule.
const dequeueSQL = `
WITH gone AS (
    DELETE FROM landings w USING tickets t, attempts a
     WHERE w.ticket_id = $1 AND t.id = w.ticket_id AND a.ticket_id = w.ticket_id AND a.number = w.attempt_number
       AND w.queue_state = 'queued'
       AND w.updated_at < now() - $2::interval
       AND NOT EXISTS (SELECT 1 FROM batches b WHERE b.id = w.batch_id AND b.state <> 'done')
       AND (
            ($3 = 'any')
         OR ($3 = 'left_queue' AND a.status <> 'in_merge_queue')
         OR ($3 = 'finished' AND (t.status IN ('done', 'cancelled') OR a.status NOT IN ('in_merge_queue', 'queued')))
       )
    RETURNING w.ticket_id, w.attempt_number, w.change_url, w.head_sha, w.enqueued_at, t.status AS ticket_status
), recorded AS (
    INSERT INTO server_merge_outcomes (ticket_id, attempt_number, change_url, head_sha, outcome, detail, enqueued_at)
    SELECT g.ticket_id, g.attempt_number, g.change_url, g.head_sha, 'merged', $5, g.enqueued_at
      FROM gone g
     WHERE ($4 = 'always' OR ($4 = 'if_done' AND g.ticket_status = 'done'))
       AND NOT EXISTS (SELECT 1 FROM server_merge_outcomes o
                        WHERE o.ticket_id = g.ticket_id AND o.outcome IN ('merged', 'already_merged'))
    RETURNING ticket_id
)
SELECT EXISTS (SELECT 1 FROM gone)`

// Dequeue removes the request's ticket's row if the runner has not begun it,
// and reports whether it did. q is the caller's transaction, so the row and
// the ticket's move stay atomic.
func (*Dequeuer) Dequeue(ctx context.Context, q db.Querier, req *ticket.DequeueRequest) (bool, error) {
	var cond, record string
	switch req.Reason {
	case ticket.DequeueMergedOutside:
		cond, record = "any", "always"
	case ticket.DequeueLeftQueue:
		cond, record = "left_queue", "never"
	case ticket.DequeueFinished:
		cond, record = "finished", "if_done"
	default:
		return false, fmt.Errorf("outcome: dequeue: unknown reason %d", req.Reason)
	}
	// The delete is the guarded queued -> merged or dequeued move; the table
	// is checked here because the statement cannot consult it.
	to, cause := PhaseDequeued, CauseDequeued
	if req.Reason == ticket.DequeueMergedOutside {
		to, cause = PhaseMerged, CauseMergedOutside
	}
	if err := checkMove(PhaseQueued, to, cause); err != nil {
		return false, err
	}
	var removed bool
	if err := q.QueryRow(ctx, dequeueSQL, req.TicketID, req.StaleFor.String(), cond, record, req.Detail).Scan(&removed); err != nil {
		return false, fmt.Errorf("outcome: dequeue #%d: %w", req.TicketID, err)
	}
	return removed, nil
}
