// Package landing records what each server-side merge attempt came to.
package landing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Outcome is the closed set of results a merge attempt can have. It mirrors
// the CHECK on server_merge_outcomes.
type Outcome string

const (
	Merged               Outcome = "merged"
	MainMoved            Outcome = "main_moved"
	ChecksRed            Outcome = "checks_red"
	ChecksPendingTimeout Outcome = "checks_pending_timeout"
	HeadChanged          Outcome = "head_changed"
	NotApprovedForHead   Outcome = "not_approved_for_head"
	LockTimeout          Outcome = "lock_timeout"
	MergeRefused         Outcome = "merge_refused"
	CodeHostError        Outcome = "code_host_error"
	// MergeMainConflict: merging main into the head conflicted.
	MergeMainConflict Outcome = "merge_main_conflict"
	// AlreadyMerged: after a restart the runner found the change merged.
	AlreadyMerged Outcome = "already_merged"
	// BatchVoided: a member's batch evidence was voided by another member's
	// drop or by main moving; it re-queues with its place.
	BatchVoided Outcome = "batch_voided"
	// BatchInteraction: green alone, red together with its batch.
	BatchInteraction Outcome = "batch_interaction"
	// ChecksNeverStarted: the head had no checks at all after a short wait.
	ChecksNeverStarted Outcome = "checks_never_started"
	// CIEventMissingRequeued: the base merged into the head got no pipeline from
	// the CI, so the landing merged the base again rather than hand the change
	// back. Not an end of the landing: the same landing goes on.
	CIEventMissingRequeued Outcome = "ci_event_missing_requeued"
)

// All lists every outcome, in the order reports present them.
var All = []Outcome{
	Merged, MainMoved, ChecksRed, ChecksPendingTimeout, HeadChanged,
	NotApprovedForHead, LockTimeout, MergeRefused, CodeHostError,
	MergeMainConflict, AlreadyMerged, BatchVoided, BatchInteraction,
	ChecksNeverStarted, CIEventMissingRequeued,
}

func (o Outcome) Valid() bool {
	for _, k := range All {
		if o == k {
			return true
		}
	}
	return false
}

// ErrInvalid is returned for a record the table could not hold.
var ErrInvalid = errors.New("outcome: invalid record")

// Record is one merge attempt's result.
type Record struct {
	TicketID      int64
	AttemptNumber int
	ChangeURL     string
	HeadSHA       string
	// ApproverUserID and ApprovedAt are nil when no approval was found.
	ApproverUserID *uuid.UUID
	ApprovedAt     *time.Time
	Outcome        Outcome
	Detail         string
	// QueueDepth is how many other landings were waiting when the runner took
	// the row; nil where it was not recorded.
	QueueDepth *int
	// EnqueuedAt is when the landing row joined the queue; nil where it was not
	// recorded.
	EnqueuedAt *time.Time
	// BatchID, BatchSize and BatchPosition place the attempt in the batch it
	// was tested in; nil and zero outside one.
	BatchID       *int64
	BatchSize     int
	BatchPosition int
}

// Count is the number of attempts that ended in one outcome.
type Count struct {
	Outcome Outcome
	N       int64
}

// Store writes and reads outcomes for the tenant in the context.
type Store struct {
	db *db.DB
}

func NewStore(database *db.DB) *Store { return &Store{db: database} }

// Record stores r. An outcome outside the closed set is refused here, before
// the database's own CHECK would be reached.
func (s *Store) Record(ctx context.Context, r *Record) error {
	if !r.Outcome.Valid() {
		return fmt.Errorf("%w: unknown outcome %q", ErrInvalid, r.Outcome)
	}
	_, err := s.db.Exec(ctx, `
INSERT INTO server_merge_outcomes
    (ticket_id, attempt_number, change_url, head_sha, approver_user_id, approved_at, outcome, detail, queue_depth, enqueued_at, batch_id, batch_size, batch_position)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, nullif($12, 0), nullif($13, 0))`,
		r.TicketID, r.AttemptNumber, r.ChangeURL, r.HeadSHA, r.ApproverUserID, r.ApprovedAt, string(r.Outcome), r.Detail, r.QueueDepth, r.EnqueuedAt,
		r.BatchID, r.BatchSize, r.BatchPosition)
	if err != nil {
		return fmt.Errorf("outcome: record: %w", err)
	}
	return nil
}

// CountForHead counts the tenant's attempts of one ticket that ended in o on
// the change head head.
func (s *Store) CountForHead(ctx context.Context, ticketID int64, head string, o Outcome) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `
SELECT count(*)::int FROM server_merge_outcomes
 WHERE ticket_id = $1 AND head_sha = $2 AND outcome = $3`, ticketID, head, string(o)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("outcome: count for head: %w", err)
	}
	return n, nil
}

// CountByOutcome counts the tenant's attempts created in [from, to), grouped
// by outcome. Every outcome appears, with zero when it has none, so a reader
// never mistakes a missing key for an unknown one.
func (s *Store) CountByOutcome(ctx context.Context, from, to time.Time) ([]Count, error) {
	rows, err := s.db.Query(ctx, `
SELECT outcome, count(*) FROM server_merge_outcomes
 WHERE created_at >= $1 AND created_at < $2
 GROUP BY outcome`, from, to)
	if err != nil {
		return nil, fmt.Errorf("outcome: count: %w", err)
	}
	defer rows.Close()
	n := map[Outcome]int64{}
	for rows.Next() {
		var o string
		var c int64
		if err := rows.Scan(&o, &c); err != nil {
			return nil, fmt.Errorf("outcome: scan count: %w", err)
		}
		n[Outcome(o)] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("outcome: count: %w", err)
	}
	out := make([]Count, 0, len(All))
	for _, o := range All {
		out = append(out, Count{Outcome: o, N: n[o]})
	}
	return out, nil
}

// List returns the tenant's attempts created in [from, to), newest first,
// up to limit.
func (s *Store) List(ctx context.Context, from, to time.Time, limit int) ([]Record, error) {
	rows, err := s.db.Query(ctx, `
SELECT ticket_id, attempt_number, change_url, head_sha, approver_user_id, approved_at, outcome, detail, queue_depth, enqueued_at,
       batch_id, coalesce(batch_size, 0), coalesce(batch_position, 0)
  FROM server_merge_outcomes
 WHERE created_at >= $1 AND created_at < $2
 ORDER BY created_at DESC, id DESC
 LIMIT $3`, from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("outcome: list: %w", err)
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		var o string
		if err := rows.Scan(&r.TicketID, &r.AttemptNumber, &r.ChangeURL, &r.HeadSHA, &r.ApproverUserID, &r.ApprovedAt, &o, &r.Detail, &r.QueueDepth, &r.EnqueuedAt, &r.BatchID, &r.BatchSize, &r.BatchPosition); err != nil {
			return nil, fmt.Errorf("outcome: scan: %w", err)
		}
		r.Outcome = Outcome(o)
		out = append(out, r)
	}
	return out, rows.Err()
}
