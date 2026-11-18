package landing

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// PostgresQueue is Queue over the landings table. It orders as the integration
// lock queue does -- ticket priority less one rank per aging step waited, ties
// by seq -- from the same SQL (ticket.PriorityRankSQL, ticket.AgedStepsSQL) and
// the same setting, so the two queues cannot drift apart.
type PostgresQueue struct {
	*LandingStore
	db *db.DB
}

// headOrderSQL is the order the runner takes rows in, over the aliases w (the
// landings row, with queued_at) and t (its ticket), and $2 the aging step in
// seconds. Positions are numbered by it too, so what the board shows is what
// Head will do.
const headOrderSQL = `(w.queue_state <> 'queued') DESC, ` + ticket.PriorityRankSQL + ` - ` + ticket.AgedStepsSQL + `, w.seq`

// serialHeadPhases are the phases the runner takes a head from: queued, or in
// the serial engine's hand. A batch member is its batch runner's, and a
// handed-back row its author's.
const serialHeadPhases = `'queued', 'serial_merging_main', 'serial_waiting_checks', 'serial_merging'`

// stepCaseSQL maps queue_state to the step it reads as (stepOf), NULL for a
// phase with none.
var stepCaseSQL = func() string {
	s := "CASE queue_state"
	for _, p := range Phases() {
		if step := stepOf(p); step != "" {
			s += fmt.Sprintf(" WHEN '%s' THEN '%s'", p, step)
		}
	}
	return s + " END"
}()

func NewPostgresQueue(database *db.DB) *PostgresQueue {
	return &PostgresQueue{LandingStore: NewLandingStore(database), db: database}
}

// A reserved batch member is never a head, the leased first one included: its
// batch's runner lands it, and the server's lease is the same agent's, so a
// second worker taking it as a head would reclaim that lease and merge it on its
// own checks while the batch is still being tested. A batch a stopped runner
// left is settled by BatchRunner.Reconcile, which reads the batches and not the
// head.
//
// A row's wait is from its last (re-)enqueue: Enqueue touches updated_at when a
// handed-back ticket rejoins, and nothing touches a waiting row after. The row
// that has begun is the only one SetStep touches, and it goes first anyway.
func (q *PostgresQueue) Head(ctx context.Context, skip []int64) (*LandingRow, error) {
	minutes, err := settings.IntIn(ctx, q.db, settings.IntegrationLockPriorityAgingMinutes)
	if err != nil {
		return nil, fmt.Errorf("landing: aging setting: %w", err)
	}
	if skip == nil {
		skip = []int64{}
	}
	var r LandingRow
	var phase, reason string
	err = q.db.QueryRow(ctx, `
SELECT w.ticket_id, w.attempt_number, w.change_url, w.head_sha, w.queue_state, coalesce(w.back_reason, ''), w.keeps_place,
       (SELECT count(*) FROM landings WHERE queue_state <> 'back_with_agent')::int - 1, w.enqueued_at
  FROM (SELECT *, updated_at AS queued_at FROM landings) w
  JOIN tickets t ON t.id = w.ticket_id
 WHERE w.queue_state IN (`+serialHeadPhases+`) AND NOT (w.ticket_id = ANY($1))
 ORDER BY `+headOrderSQL+`
 LIMIT 1`, skip, (time.Duration(minutes)*time.Minute).Seconds()).
		Scan(&r.TicketID, &r.AttemptNumber, &r.ChangeURL, &r.HeadSHA, &phase, &reason, &r.KeepsPlace, &r.QueueDepth, &r.EnqueuedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("landing: head: %w", err)
	}
	r.setPhase(Phase(phase))
	r.BackReason = Outcome(reason)
	return &r, nil
}

// Progress is what the queue's health is judged on: whether the landing in
// hand is moving, and how long the others have waited. It deliberately has no
// "age of the head": on a long but healthy queue that grows past any bound.
type Progress struct {
	// Step is the step the landing in hand is on, empty when none has begun.
	// StepAge is how long it has been on it; a begun landing touches its row at
	// every step, so a head stuck on CI or the code host ages and a moving one
	// does not.
	Step    Step
	StepAge time.Duration
	// Landing counts every row waiting or being worked; OldestWait is how long
	// the longest-waiting row not yet begun has waited since it (re-)joined the
	// queue. A begun row is being worked, and its row is touched at every step.
	Landing    int
	OldestWait time.Duration
	// TopWait is how long the row that ranks first among those not yet begun
	// (priority less aging, as Head orders them) has waited. Unlike OldestWait
	// it is a starvation signal: a row ranked first is next to be taken, so a
	// long wait means the runner is passing it over, not that the queue is long.
	TopWait time.Duration
	// BatchesWithoutCI counts testing batches that have no CI pipeline yet, and
	// OldestBatchWithoutCI is how long the longest has been testing.
	BatchesWithoutCI     int
	OldestBatchWithoutCI time.Duration
}

// Progress reads the queue's Progress. It is zero for an empty queue. Where
// several rows have begun (a batch), the one on its step longest is reported.
// A member of a testing batch reads as waiting_checks, not merging main:
// it gets the checks bound, not the short one. A batch past CI's timeout still
// exceeds that bound, as its rows are not touched while it tests.
func (q *PostgresQueue) Progress(ctx context.Context) (Progress, error) {
	var p Progress
	var oldest, stepAge float64
	var step string
	err := q.db.QueryRow(ctx, `
SELECT count(*)::int,
       coalesce(max(extract(epoch FROM now() - updated_at)) FILTER (WHERE queue_state = 'queued'), 0)::float8,
       coalesce((array_agg(eff_step ORDER BY updated_at) FILTER (WHERE eff_step IS NOT NULL))[1], ''),
       coalesce(max(extract(epoch FROM now() - updated_at)) FILTER (WHERE eff_step IS NOT NULL), 0)::float8
  FROM (SELECT updated_at, queue_state, `+stepCaseSQL+` AS eff_step
          FROM landings WHERE queue_state <> 'back_with_agent') l`).Scan(&p.Landing, &oldest, &step, &stepAge)
	if err != nil {
		return Progress{}, fmt.Errorf("landing: progress: %w", err)
	}
	p.Step = Step(step)
	p.StepAge = time.Duration(stepAge * float64(time.Second))
	p.OldestWait = time.Duration(oldest * float64(time.Second))
	if err := q.batchesWithoutCI(ctx, &p); err != nil {
		return Progress{}, err
	}
	if p.TopWait, err = q.topWait(ctx); err != nil {
		return Progress{}, err
	}
	return p, nil
}

func (q *PostgresQueue) batchesWithoutCI(ctx context.Context, p *Progress) error {
	var age float64
	err := q.db.QueryRow(ctx, `
SELECT count(*)::int, coalesce(max(extract(epoch FROM now() - updated_at)), 0)::float8
  FROM batches WHERE state = 'testing' AND ci_pipeline IS NULL`).Scan(&p.BatchesWithoutCI, &age)
	if err != nil {
		return fmt.Errorf("landing: batches without ci: %w", err)
	}
	p.OldestBatchWithoutCI = time.Duration(age * float64(time.Second))
	return nil
}

func (q *PostgresQueue) topWait(ctx context.Context) (time.Duration, error) {
	minutes, err := settings.IntIn(ctx, q.db, settings.IntegrationLockPriorityAgingMinutes)
	if err != nil {
		return 0, fmt.Errorf("landing: aging setting: %w", err)
	}
	var wait float64
	err = q.db.QueryRow(ctx, `
SELECT extract(epoch FROM now() - w.queued_at)::float8
  FROM (SELECT *, updated_at AS queued_at FROM landings) w
  JOIN tickets t ON t.id = w.ticket_id
 WHERE w.queue_state = 'queued' AND NOT (w.ticket_id = ANY($1))
 ORDER BY `+headOrderSQL+`
 LIMIT 1`, []int64{}, (time.Duration(minutes) * time.Minute).Seconds()).Scan(&wait)
	if err == pgx.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("landing: top wait: %w", err)
	}
	return time.Duration(wait * float64(time.Second)), nil
}

// Ordered lists every row for the tenant: the landing ones in the order Head
// takes them, numbered from 1, then those handed back with position 0. Unlike
// LandingStore.Queue, which is FIFO by seq, it counts priority and aging.
func (q *PostgresQueue) Ordered(ctx context.Context) ([]LandingRow, error) {
	minutes, err := settings.IntIn(ctx, q.db, settings.IntegrationLockPriorityAgingMinutes)
	if err != nil {
		return nil, fmt.Errorf("landing: aging setting: %w", err)
	}
	rows, err := q.db.Query(ctx, `
SELECT w.ticket_id, w.attempt_number, w.change_url, w.head_sha, w.queue_state, coalesce(w.back_reason, ''), w.keeps_place,
       CASE WHEN w.queue_state <> 'back_with_agent' THEN row_number() OVER (PARTITION BY w.queue_state = 'back_with_agent' ORDER BY `+headOrderSQL+`) ELSE 0 END AS pos,
       w.batch_id, coalesce(w.batch_position, 0), extract(epoch FROM now() - w.updated_at)::float8
  FROM (SELECT *, updated_at AS queued_at FROM landings) w
  JOIN tickets t ON t.id = w.ticket_id
 WHERE NOT (w.ticket_id = ANY($1))
 ORDER BY (w.queue_state <> 'back_with_agent') DESC, pos, w.seq`, []int64{}, (time.Duration(minutes) * time.Minute).Seconds())
	if err != nil {
		return nil, fmt.Errorf("landing: ordered: %w", err)
	}
	defer rows.Close()
	var out []LandingRow
	for rows.Next() {
		var r LandingRow
		var phase, reason string
		var sinceUpdate float64
		if err := rows.Scan(&r.TicketID, &r.AttemptNumber, &r.ChangeURL, &r.HeadSHA, &phase, &reason, &r.KeepsPlace, &r.Position, &r.BatchID, &r.BatchPosition, &sinceUpdate); err != nil {
			return nil, fmt.Errorf("landing: scan ordered: %w", err)
		}
		r.SinceUpdate = time.Duration(sinceUpdate * float64(time.Second))
		r.setPhase(Phase(phase))
		r.BackReason = Outcome(reason)
		out = append(out, r)
	}
	return out, rows.Err()
}
