package landing

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// BatchState is where a batch stands; the runner writes it before the side
// effect it names.
type BatchState string

const (
	BatchBuilding BatchState = "building"
	BatchTesting  BatchState = "testing"
	BatchMerging  BatchState = "merging"
	BatchDone     BatchState = "done"
)

// BatchResult is how a finished batch ended.
type BatchResult string

const (
	ResultMerged BatchResult = "merged"
	ResultRed    BatchResult = "red"
	ResultVoided BatchResult = "voided"
	// ResultSplit is a batch whose members were landed one at a time.
	ResultSplit BatchResult = "split"
)

func (r BatchResult) valid() bool {
	return r == ResultMerged || r == ResultRed || r == ResultVoided || r == ResultSplit
}

// batchBranchPrefix names a batch's temporary branch with its id.
const batchBranchPrefix = "slop-haus-batch-"

var (
	// ErrNotReservable is a batch with a member that cannot be reserved: it
	// was handed back, removed or taken by another reservation while the
	// batch was built.
	ErrNotReservable = errors.New("landing: a member of the batch cannot be reserved")
	// ErrBatchTransition is a state change the batch does not allow.
	ErrBatchTransition = errors.New("landing: batch state change not allowed")
	// ErrNoBatch is a batch that does not exist.
	ErrNoBatch = errors.New("landing: no such batch")
)

// Batch is a row of the batches table.
type Batch struct {
	ID      int64
	BaseSHA string
	Branch  string
	// Members are the ticket ids the batch was built with, in merge order; a member handed back later stays listed.
	Members []int64
	// Prefixes are the batch branch's commit after each member was merged
	// into it, aligned with Members; empty until the branch is built.
	Prefixes []string
	// Heads are the heads the members were tested at, aligned with Members;
	// empty until the batch is tested.
	Heads      []string
	State      BatchState
	CIPipeline string
	// Change is the draft change opened on the branch so that CI runs; nil
	// until it is opened.
	Change    *codehost.ChangeRef
	CreatedAt time.Time
	UpdatedAt time.Time
}

// allowedFrom is the states each state may be entered from. Done is entered
// from anywhere, which is how a voided batch ends.
var allowedFrom = map[BatchState][]BatchState{
	BatchTesting: {BatchBuilding},
	BatchMerging: {BatchTesting},
	BatchDone:    {BatchBuilding, BatchTesting, BatchMerging},
}

// BatchStore keeps the batches of the tenant in the context, and the
// reservation a batch puts on its members' landing rows.
type BatchStore struct {
	db *db.DB
}

func NewBatchStore(database *db.DB) *BatchStore { return &BatchStore{db: database} }

const batchCols = `id, base_sha, branch, member_ticket_ids, member_prefix_shas, member_head_shas, state, coalesce(ci_pipeline, ''), change_ref, created_at, updated_at`

func scanBatch(row pgx.Row) (*Batch, error) {
	var b Batch
	var state string
	if err := row.Scan(&b.ID, &b.BaseSHA, &b.Branch, &b.Members, &b.Prefixes, &b.Heads, &state, &b.CIPipeline, &b.Change, &b.CreatedAt, &b.UpdatedAt); err != nil {
		return nil, err
	}
	b.State = BatchState(state)
	return &b, nil
}

// Create records a building batch on baseSHA for ticketIDs, in that order. It
// touches no landing row: the members stay queued while the branch is built,
// because building can still fail, and Reserve takes them once it cannot.
func (s *BatchStore) Create(ctx context.Context, baseSHA string, ticketIDs []int64) (*Batch, error) {
	if len(ticketIDs) == 0 {
		return nil, fmt.Errorf("landing: create batch: no members")
	}
	var batch *Batch
	err := s.db.InTx(ctx, func(q db.Querier) error {
		var id int64
		if err := q.QueryRow(ctx, `
INSERT INTO batches (base_sha, branch, member_ticket_ids, state) VALUES ($1, '', $2, 'building') RETURNING id`,
			baseSHA, ticketIDs).Scan(&id); err != nil {
			return fmt.Errorf("landing: create batch: %w", err)
		}
		var err error
		batch, err = scanBatch(q.QueryRow(ctx, `
UPDATE batches SET branch = $2 || id::text WHERE id = $1 RETURNING `+batchCols, id, batchBranchPrefix))
		return err
	})
	if err != nil {
		return nil, err
	}
	return batch, nil
}

// Reserve moves every member of the building batch from queued to testing in
// the batch, in one transaction: all of them or none. Rows another transaction
// holds are not waited for, so a concurrent close or hand back is not raced;
// a member that is not queued any more, or is held, makes the whole batch
// ErrNotReservable, because the branch was built with it in.
func (s *BatchStore) Reserve(ctx context.Context, id int64) (*Batch, error) {
	var batch *Batch
	err := s.db.InTx(ctx, func(q db.Querier) error {
		b, err := scanBatch(q.QueryRow(ctx, `SELECT `+batchCols+` FROM batches WHERE id = $1 FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoBatch
		}
		if err != nil {
			return fmt.Errorf("landing: read batch: %w", err)
		}
		if b.State != BatchBuilding {
			return fmt.Errorf("%w: %s to reserved", ErrBatchTransition, b.State)
		}
		var locked int
		if err := q.QueryRow(ctx, `
SELECT count(*) FROM (SELECT 1 FROM landings WHERE ticket_id = ANY($1) FOR UPDATE SKIP LOCKED) l`, b.Members).Scan(&locked); err != nil {
			return fmt.Errorf("landing: lock members: %w", err)
		}
		if locked != len(b.Members) {
			return fmt.Errorf("%w: a member is held or gone", ErrNotReservable)
		}
		for i, ticketID := range b.Members {
			err := transition(ctx, q, &move{
				TicketID: ticketID, From: PhaseQueued, To: PhaseBatchTesting, Cause: CauseBatchBuilt,
				Set:  `batch_id = $4, batch_position = $5`,
				Args: []any{id, i + 1},
			})
			var conflict *ConflictError
			if errors.As(err, &conflict) {
				return fmt.Errorf("%w: ticket %d is %s", ErrNotReservable, ticketID, conflict.Actual)
			}
			if err != nil {
				return err
			}
		}
		// A reserved batch is live: reconcile leaves a batch alone for a lease.
		if _, err := q.Exec(ctx, `UPDATE batches SET updated_at = now() WHERE id = $1`, id); err != nil {
			return fmt.Errorf("landing: touch batch: %w", err)
		}
		batch = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	return batch, nil
}

// Get reads one batch.
func (s *BatchStore) Get(ctx context.Context, id int64) (*Batch, error) {
	b, err := scanBatch(s.db.QueryRow(ctx, `SELECT `+batchCols+` FROM batches WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoBatch
	}
	if err != nil {
		return nil, fmt.Errorf("landing: get batch: %w", err)
	}
	return b, nil
}

// Live lists the batches that are not done, oldest first: what a restart has
// to reconcile.
func (s *BatchStore) Live(ctx context.Context) ([]Batch, error) {
	rows, err := s.db.Query(ctx, `SELECT `+batchCols+` FROM batches WHERE state <> 'done' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("landing: live batches: %w", err)
	}
	defer rows.Close()
	var out []Batch
	for rows.Next() {
		b, err := scanBatch(rows)
		if err != nil {
			return nil, fmt.Errorf("landing: scan batch: %w", err)
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// SetState moves the batch to a later state. A batch cannot go back, and done
// is final.
func (s *BatchStore) SetState(ctx context.Context, id int64, to BatchState) error {
	from := allowedFrom[to]
	if len(from) == 0 {
		return fmt.Errorf("%w: %q", ErrBatchTransition, to)
	}
	if to == BatchDone {
		return s.Finish(ctx, id, ResultVoided, "ended without a recorded result")
	}
	fromStrs := make([]string, len(from))
	for i, f := range from {
		fromStrs[i] = string(f)
	}
	tag, err := s.db.Exec(ctx, `
UPDATE batches SET state = $2, updated_at = now() WHERE id = $1 AND state = ANY($3)`, id, string(to), fromStrs)
	if err != nil {
		return fmt.Errorf("landing: set batch state: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	b, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: %s to %s", ErrBatchTransition, b.State, to)
}

// Finish ends the batch with its result. Like SetState to done, it is allowed
// from any state.
func (s *BatchStore) Finish(ctx context.Context, id int64, result BatchResult, reason string) error {
	if !result.valid() {
		return fmt.Errorf("%w: %q is not a batch result", ErrInvalid, result)
	}
	tag, err := s.db.Exec(ctx, `
UPDATE batches SET state = 'done', result = $2, result_reason = nullif($3, ''), updated_at = now()
 WHERE id = $1 AND state <> 'done'`, id, string(result), reason)
	if err != nil {
		return fmt.Errorf("landing: finish batch: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	b, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: %s to %s", ErrBatchTransition, b.State, BatchDone)
}

// SetPipeline records the CI pipeline testing the batch head.
func (s *BatchStore) SetPipeline(ctx context.Context, id int64, pipeline string) error {
	tag, err := s.db.Exec(ctx, `UPDATE batches SET ci_pipeline = $2, updated_at = now() WHERE id = $1`, id, pipeline)
	if err != nil {
		return fmt.Errorf("landing: set batch pipeline: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNoBatch
	}
	return nil
}

// SetChange records the change opened on the batch's branch.
func (s *BatchStore) SetChange(ctx context.Context, id int64, ref codehost.ChangeRef) error {
	tag, err := s.db.Exec(ctx, `UPDATE batches SET change_ref = $2, updated_at = now() WHERE id = $1`, id, ref)
	if err != nil {
		return fmt.Errorf("landing: set batch change: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNoBatch
	}
	return nil
}

// Drop takes a member out of a batch that is still being built, so that it is
// no longer built with it. The row is the queue's already: the members were
// not reserved. A batch left with no member is done.
func (s *BatchStore) Drop(ctx context.Context, id, ticketID int64) (*Batch, error) {
	var b *Batch
	err := s.db.InTx(ctx, func(q db.Querier) error {
		cur, err := scanBatch(q.QueryRow(ctx, `SELECT `+batchCols+` FROM batches WHERE id = $1 FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoBatch
		}
		if err != nil {
			return fmt.Errorf("landing: read batch: %w", err)
		}
		pos := slices.Index(cur.Members, ticketID) + 1
		if pos == 0 {
			return fmt.Errorf("landing: ticket %d is not a member of batch %d", ticketID, id)
		}
		var reserved int
		if err := q.QueryRow(ctx, `SELECT count(*) FROM landings WHERE batch_id = $1`, id).Scan(&reserved); err != nil {
			return fmt.Errorf("landing: read reservations: %w", err)
		}
		if cur.State != BatchBuilding || reserved > 0 {
			return fmt.Errorf("%w: members of a %s batch are released, not dropped", ErrBatchTransition, cur.State)
		}
		// The array keeps at least one member, as the table requires; an empty
		// batch is done and keeps the last member it had.
		b, err = scanBatch(q.QueryRow(ctx, `
UPDATE batches SET member_ticket_ids = CASE WHEN cardinality(member_ticket_ids) > 1 THEN array_remove(member_ticket_ids, $2) ELSE member_ticket_ids END,
       member_prefix_shas = CASE WHEN cardinality(member_ticket_ids) > 1 AND cardinality(member_prefix_shas) >= $3
                                 THEN member_prefix_shas[1:$3 - 1] || member_prefix_shas[$3 + 1:] ELSE member_prefix_shas END,
       member_head_shas = CASE WHEN cardinality(member_ticket_ids) > 1 AND cardinality(member_head_shas) >= $3
                               THEN member_head_shas[1:$3 - 1] || member_head_shas[$3 + 1:] ELSE member_head_shas END,
       state = CASE WHEN cardinality(member_ticket_ids) > 1 THEN state ELSE 'done' END,
       result = CASE WHEN cardinality(member_ticket_ids) > 1 THEN result ELSE 'voided' END,
       result_reason = CASE WHEN cardinality(member_ticket_ids) > 1 THEN result_reason ELSE 'every member left the batch' END,
       updated_at = now()
 WHERE id = $1 RETURNING `+batchCols, id, ticketID, pos))
		return err
	})
	if err != nil {
		return nil, err
	}
	return b, nil
}

// releaseMember returns one reserved member to the queue as the table has it:
// a merging member whose merge is refused falls back to testing, a testing one
// is released, and a released one is queued. The row's place is its seq, which
// is untouched. It is a compare-and-set on the phase it was read in.
func releaseMember(ctx context.Context, q db.Querier, id, ticketID int64, touch bool) error {
	from, err := currentState(ctx, q, ticketID)
	if errors.Is(err, ErrNoLanding) || (err == nil && from.BatchID != id) {
		return fmt.Errorf("landing: ticket %d is not a member of batch %d", ticketID, id)
	}
	if err != nil {
		return err
	}
	phase := from.Phase
	if phase == PhaseBatchMerging {
		if err := checkMove(phase, PhaseBatchTesting, CauseMainMoved); err != nil {
			return err
		}
		phase = PhaseBatchTesting
	}
	if phase != PhaseBatchTesting {
		return fmt.Errorf("%w: %s cannot be released", ErrIllegalTransition, from.Phase)
	}
	if err := checkMove(PhaseBatchTesting, PhaseReleased, CauseBatchEnded); err != nil {
		return err
	}
	if err := checkMove(PhaseReleased, PhaseQueued, CauseRequeued); err != nil {
		return err
	}
	bump := ""
	if touch {
		bump = ", updated_at = now()"
	}
	tag, err := q.Exec(ctx, `
UPDATE landings SET batch_id = NULL, batch_position = NULL, queue_state = 'queued'`+bump+`
 WHERE ticket_id = $1 AND batch_id = $2 AND `+guardedPhase+` = $3`, ticketID, id, string(from.Phase))
	if err != nil {
		return fmt.Errorf("landing: release member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return conflictOf(ctx, q, &move{TicketID: ticketID, From: from.Phase, To: PhaseQueued})
	}
	return nil
}

// Release clears a member's reservation once the batch is built, leaving the
// batch's record of its members, their tested commits and heads as they were
// tested: the record is what landing_batches and a restart read, and a member
// handed back or requeued was still a member of the run.
func (s *BatchStore) Release(ctx context.Context, id, ticketID int64) error {
	return s.db.InTx(ctx, func(q db.Querier) error { return releaseMember(ctx, q, id, ticketID, true) })
}

// Void ends the batch with the reason and releases every member still in it.
// They stay queued with their place, which is their seq: the queue's order
// within a priority is unchanged. A batch that has ended is refused, so a
// second ending cannot release members a later reservation took. Their wait
// for priority aging restarts at the reservation, because a reserved row's
// updated_at must be fresh for the step-age metric (a stale one would read as
// a stuck step).
func (s *BatchStore) Void(ctx context.Context, id int64, reason string) error {
	return s.db.InTx(ctx, func(q db.Querier) error {
		var state string
		err := q.QueryRow(ctx, `SELECT state FROM batches WHERE id = $1 FOR UPDATE`, id).Scan(&state)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoBatch
		}
		if err != nil {
			return fmt.Errorf("landing: read batch: %w", err)
		}
		if state == string(BatchDone) {
			return fmt.Errorf("%w: %s to %s", ErrBatchTransition, state, BatchDone)
		}
		// A row handed back by a release that predates the clearing keeps its
		// reservation; it is not a phase change.
		if _, err := q.Exec(ctx, `UPDATE landings SET batch_id = NULL, batch_position = NULL WHERE batch_id = $1 AND queue_state = 'back_with_agent'`, id); err != nil {
			return fmt.Errorf("landing: clear stale reservations: %w", err)
		}
		rows, err := q.Query(ctx, `SELECT ticket_id FROM landings WHERE batch_id = $1 ORDER BY batch_position FOR UPDATE`, id)
		if err != nil {
			return fmt.Errorf("landing: lock members: %w", err)
		}
		var members []int64
		for rows.Next() {
			var t int64
			if err := rows.Scan(&t); err != nil {
				rows.Close()
				return fmt.Errorf("landing: scan member: %w", err)
			}
			members = append(members, t)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("landing: lock members: %w", err)
		}
		for _, t := range members {
			if err := releaseMember(ctx, q, id, t, false); err != nil {
				return err
			}
		}
		if _, err := q.Exec(ctx, `
UPDATE batches SET state = 'done', result = 'voided', result_reason = nullif($2, ''), updated_at = now() WHERE id = $1`, id, reason); err != nil {
			return fmt.Errorf("landing: void batch: %w", err)
		}
		return nil
	})
}

// SetTested records what the batch's run tests, in the order of its members:
// the batch branch's commit after each member, and the head each member was
// merged into it at. They are written together because recovery needs both to
// present the run as evidence.
func (s *BatchStore) SetTested(ctx context.Context, id int64, prefixes, heads []string) error {
	tag, err := s.db.Exec(ctx, `UPDATE batches SET member_prefix_shas = $2, member_head_shas = $3, updated_at = now() WHERE id = $1`, id, prefixes, heads)
	if err != nil {
		return fmt.Errorf("landing: set batch tested commits: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNoBatch
	}
	return nil
}

// MemberRows reads the landing rows still reserved by the batch, in merge
// order. A member of the batch with no row in it has left the queue: merged,
// or cancelled.
func (s *BatchStore) MemberRows(ctx context.Context, id int64) ([]LandingRow, error) {
	rows, err := s.db.Query(ctx, `
SELECT ticket_id, attempt_number, change_url, head_sha, queue_state, coalesce(back_reason, ''), keeps_place, enqueued_at, batch_id, batch_position
  FROM landings WHERE batch_id = $1 AND queue_state <> 'back_with_agent' ORDER BY batch_position`, id)
	if err != nil {
		return nil, fmt.Errorf("landing: batch rows: %w", err)
	}
	defer rows.Close()
	var out []LandingRow
	for rows.Next() {
		var r LandingRow
		var phase, reason string
		if err := rows.Scan(&r.TicketID, &r.AttemptNumber, &r.ChangeURL, &r.HeadSHA, &phase, &reason, &r.KeepsPlace, &r.EnqueuedAt, &r.BatchID, &r.BatchPosition); err != nil {
			return nil, fmt.Errorf("landing: scan batch row: %w", err)
		}
		r.setPhase(Phase(phase))
		r.BackReason = Outcome(reason)
		out = append(out, r)
	}
	return out, rows.Err()
}

// FinishedSince lists the batches that ended after since, with a row of the
// attempt of one member that has an outcome recorded: enough to name the
// repository whose branch to delete. A batch with no outcome has none.
func (s *BatchStore) FinishedSince(ctx context.Context, since time.Time) ([]FinishedBatch, error) {
	rows, err := s.db.Query(ctx, `
SELECT b.id, b.branch, o.ticket_id, o.attempt_number
  FROM batches b
  JOIN LATERAL (SELECT ticket_id, attempt_number FROM server_merge_outcomes WHERE batch_id = b.id ORDER BY id DESC LIMIT 1) o ON true
 WHERE b.state = 'done' AND b.updated_at > $1 ORDER BY b.id`, since)
	if err != nil {
		return nil, fmt.Errorf("landing: finished batches: %w", err)
	}
	defer rows.Close()
	var out []FinishedBatch
	for rows.Next() {
		var f FinishedBatch
		if err := rows.Scan(&f.ID, &f.Branch, &f.Sample.TicketID, &f.Sample.AttemptNumber); err != nil {
			return nil, fmt.Errorf("landing: scan finished batch: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FinishedBatch is an ended batch whose branch may be left on the host.
type FinishedBatch struct {
	ID     int64
	Branch string
	// Sample names the repository: a row of a member's attempt.
	Sample LandingRow
}

// Sample is a row of an attempt of one member of the batch that has an outcome
// recorded, which names the repository whose branch to delete; nil when none
// has. The members' own rows are gone once they merge.
func (s *BatchStore) Sample(ctx context.Context, id int64) (*LandingRow, error) {
	var r LandingRow
	err := s.db.QueryRow(ctx, `SELECT ticket_id, attempt_number FROM server_merge_outcomes WHERE batch_id = $1 ORDER BY id DESC LIMIT 1`, id).Scan(&r.TicketID, &r.AttemptNumber)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("landing: sample a batch member: %w", err)
	}
	return &r, nil
}

// BatchSummary is a batch as the history shows it.
type BatchSummary struct {
	Batch
	// Reason is why a batch ended without merging all its members: the latest
	// outcome recorded against it that is not a merge, as "outcome: detail".
	// Empty when there is none.
	Reason string
}

// Recent lists the tenant's newest batches, done ones included, newest first.
// since, when set, keeps only the batches created at or after it.
func (s *BatchStore) Recent(ctx context.Context, limit int, since *time.Time) ([]BatchSummary, error) {
	rows, err := s.db.Query(ctx, `
SELECT `+batchCols+`, coalesce(r.reason, CASE WHEN b.result IN ('voided', 'red') THEN b.result_reason END, '')
  FROM batches b
  LEFT JOIN LATERAL (
       SELECT CASE WHEN detail = '' THEN outcome ELSE outcome || ': ' || detail END AS reason
         FROM server_merge_outcomes WHERE batch_id = b.id AND outcome <> 'merged' ORDER BY id DESC LIMIT 1) r ON true
 WHERE tenant_id = current_tenant() AND ($2::timestamptz IS NULL OR b.created_at >= $2)
 ORDER BY b.id DESC LIMIT $1`, limit, since)
	if err != nil {
		return nil, fmt.Errorf("landing: recent batches: %w", err)
	}
	defer rows.Close()
	var out []BatchSummary
	for rows.Next() {
		var b BatchSummary
		var state string
		if err := rows.Scan(&b.ID, &b.BaseSHA, &b.Branch, &b.Members, &b.Prefixes, &b.Heads, &state, &b.CIPipeline, &b.Change, &b.CreatedAt, &b.UpdatedAt, &b.Reason); err != nil {
			return nil, fmt.Errorf("landing: scan recent batch: %w", err)
		}
		b.State = BatchState(state)
		out = append(out, b)
	}
	return out, rows.Err()
}

// LandingOf is the landing row of the first of ticketIDs that still has one,
// enough to name the repository whose branch to delete; nil when none has.
func (s *BatchStore) LandingOf(ctx context.Context, ticketIDs []int64) (*LandingRow, error) {
	var r LandingRow
	err := s.db.QueryRow(ctx, `
SELECT ticket_id, attempt_number FROM landings WHERE ticket_id = ANY($1) ORDER BY array_position($1, ticket_id) LIMIT 1`, ticketIDs).Scan(&r.TicketID, &r.AttemptNumber)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("landing: find a member's landing: %w", err)
	}
	return &r, nil
}
