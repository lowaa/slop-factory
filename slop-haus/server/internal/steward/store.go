package steward

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lowaa/slop-factory/go-db/pgerr"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Decision is what the owner did with a proposal the steward assessed.
type Decision string

const (
	DecisionApproved  Decision = "approved"
	DecisionRejected  Decision = "rejected"
	DecisionEdited    Decision = "edited"
	DecisionWithdrawn Decision = "withdrawn"
)

// ErrNotFound is an assessment that does not exist in the caller's tenant.
var ErrNotFound = errors.New("steward: assessment not found")

// Record is one stored assessment: the server's verdict on a proposal, and
// later the owner's decision on it.
type Record struct {
	ID              uuid.UUID
	TicketID        *int64
	PendingActionID uuid.UUID
	Category        string
	Tier            Tier
	Verdict         Verdict
	Mode            Mode
	Reason          string
	Evidence        []Evidence
	TicketRevision  *int
	HeadSHA         *string
	AppliedAt       *time.Time
	VetoDeadline    *time.Time
	VetoedAt        *time.Time
	VetoedBy        *uuid.UUID
	UndoneAt        *time.Time
	VoidedAt        *time.Time
	VoidKind        *VoidKind
	VoidReason      string
	HumanDecision   *Decision
	DecidedAt       *time.Time
	AgentID         uuid.UUID
	CreatedAt       time.Time
	// StewardInstructionsVersionID is the steward instructions version pinned
	// when the assessment was made; nil when none was pinned (or for
	// assessments made before they were recorded).
	StewardInstructionsVersionID *uuid.UUID
	// Model and Effort are what the steward's harness config gave its run
	// when the assessment was made; nil when it set none.
	Model  *string
	Effort *string
}

// NewRecord is an assessment as it is first stored. Mode is shadow or live;
// off assesses nothing.
type NewRecord struct {
	TicketID                     *int64
	PendingActionID              uuid.UUID
	AgentID                      uuid.UUID
	Mode                         Mode
	Assessment                   *Assessment
	TicketRevision               *int
	HeadSHA                      string
	CreatedAt                    time.Time
	StewardInstructionsVersionID *uuid.UUID
	Model                        *string
	Effort                       *string
}

// Store persists assessments for the tenant in ctx; row level security
// enforces that as well.
type Store struct {
	db *db.DB
}

func NewStore(database *db.DB) *Store { return &Store{db: database} }

const recordColumns = `id, ticket_id, pending_action_id, category, tier, verdict, mode, reason, evidence, ticket_revision, head_sha,
	applied_at, veto_deadline, vetoed_at, vetoed_by, undone_at, voided_at, void_kind, void_reason, human_decision, decided_at, agent_id, created_at, steward_instructions_version_id, model, effort`

func scanRecord(row pgx.Row) (*Record, error) {
	var r Record
	var evidence []byte
	if err := row.Scan(&r.ID, &r.TicketID, &r.PendingActionID, &r.Category, &r.Tier, &r.Verdict, &r.Mode, &r.Reason, &evidence,
		&r.TicketRevision, &r.HeadSHA, &r.AppliedAt, &r.VetoDeadline, &r.VetoedAt, &r.VetoedBy, &r.UndoneAt,
		&r.VoidedAt, &r.VoidKind, &r.VoidReason, &r.HumanDecision, &r.DecidedAt, &r.AgentID, &r.CreatedAt, &r.StewardInstructionsVersionID, &r.Model, &r.Effort); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(evidence, &r.Evidence); err != nil {
		return nil, fmt.Errorf("steward: read evidence: %w", err)
	}
	return &r, nil
}

// Create stores the assessment of a proposal. A proposal is assessed once:
// a second Create for it is an error.
func (s *Store) Create(ctx context.Context, in *NewRecord) (*Record, error) {
	evidence := in.Assessment.Evidence
	if evidence == nil {
		evidence = []Evidence{}
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		return nil, fmt.Errorf("steward: encode evidence: %w", err)
	}
	var head *string
	if in.HeadSHA != "" {
		head = &in.HeadSHA
	}
	created, err := scanRecord(s.db.QueryRow(ctx, `
		INSERT INTO steward_assessments (id, ticket_id, pending_action_id, category, tier, verdict, mode, reason, evidence,
			ticket_revision, head_sha, agent_id, created_at, steward_instructions_version_id, model, effort)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		RETURNING `+recordColumns,
		uuid.New(), in.TicketID, in.PendingActionID, in.Assessment.Category, in.Assessment.Tier, in.Assessment.Verdict, in.Mode,
		in.Assessment.Reason, raw, in.TicketRevision, head, in.AgentID, in.CreatedAt, in.StewardInstructionsVersionID, in.Model, in.Effort))
	if err != nil {
		return nil, fmt.Errorf("steward: create assessment: %w", err)
	}
	return created, nil
}

// CreateWithinCap stores the assessment of a proposal, if the proposal is
// still pending and fewer than dailyCap assessments have been made since the
// given time. A proposal is assessed once.
//
// The count, the pending check and the insert share one transaction behind a
// per-tenant advisory lock, so concurrent assessments cannot both pass the
// cap, and the proposal's row is share-locked so it cannot be decided between
// the check and the insert.
func (s *Store) CreateWithinCap(ctx context.Context, in *NewRecord, since time.Time, dailyCap int) (*Record, error) {
	evidence := in.Assessment.Evidence
	if evidence == nil {
		evidence = []Evidence{}
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		return nil, fmt.Errorf("steward: encode evidence: %w", err)
	}
	var head *string
	if in.HeadSHA != "" {
		head = &in.HeadSHA
	}
	var created *Record
	err = s.db.InTx(ctx, func(q db.Querier) error {
		if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_tenant()::text, 0))`); err != nil {
			return err
		}
		var used int
		if err := q.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM steward_assessments WHERE created_at >= $1)
			     + (SELECT count(*) FROM steward_discussion_labels WHERE assessed_at >= $1 AND assessment_id IS NULL)`, since).Scan(&used); err != nil {
			return err
		}
		if used >= dailyCap {
			return ErrDailyCap
		}
		var pending bool
		err := q.QueryRow(ctx, `SELECT true FROM pending_actions WHERE id = $1 AND status = 'pending' AND expires_at > now() FOR SHARE`,
			in.PendingActionID).Scan(&pending)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotPending
		}
		if err != nil {
			return err
		}
		created, err = scanRecord(q.QueryRow(ctx, `
			INSERT INTO steward_assessments (id, ticket_id, pending_action_id, category, tier, verdict, mode, reason, evidence,
				ticket_revision, head_sha, agent_id, created_at, steward_instructions_version_id, model, effort)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
			RETURNING `+recordColumns,
			uuid.New(), in.TicketID, in.PendingActionID, in.Assessment.Category, in.Assessment.Tier, in.Assessment.Verdict, in.Mode,
			in.Assessment.Reason, raw, in.TicketRevision, head, in.AgentID, in.CreatedAt, in.StewardInstructionsVersionID, in.Model, in.Effort))
		return err
	})
	if pgerr.IsUniqueViolation(err) {
		return nil, ErrAlreadyAssessed
	}
	if errors.Is(err, ErrDailyCap) || errors.Is(err, ErrNotPending) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("steward: create assessment: %w", err)
	}
	return created, nil
}

// GetByActions reads the assessments of several proposals, keyed by proposal.
func (s *Store) GetByActions(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]*Record, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+recordColumns+` FROM steward_assessments WHERE pending_action_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("steward: get assessments: %w", err)
	}
	defer rows.Close()
	out := make(map[uuid.UUID]*Record, len(ids))
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("steward: get assessments: %w", err)
		}
		out[r.PendingActionID] = r
	}
	return out, rows.Err()
}

// ListFilter narrows List. Zero fields match everything.
type ListFilter struct {
	Category string
	// Since and Until bound created_at: [Since, Until).
	Since, Until time.Time
	Limit        int
}

const defaultListLimit = 100

// List returns assessments newest first.
func (s *Store) List(ctx context.Context, f *ListFilter) ([]Record, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	rows, err := s.db.Query(ctx, `
		SELECT `+recordColumns+` FROM steward_assessments
		 WHERE ($1 = '' OR category = $1)
		   AND ($2::timestamptz IS NULL OR created_at >= $2)
		   AND ($3::timestamptz IS NULL OR created_at < $3)
		 ORDER BY created_at DESC, id
		 LIMIT $4`, f.Category, nullTime(f.Since), nullTime(f.Until), limit)
	if err != nil {
		return nil, fmt.Errorf("steward: list assessments: %w", err)
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("steward: list assessments: %w", err)
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// RecordDecision notes what the owner did with a proposal. A proposal the
// steward never assessed, or whose decision is already recorded, is left
// alone: the first decision stands. It reports whether a row was updated.
func (s *Store) RecordDecision(ctx context.Context, pendingActionID uuid.UUID, d Decision, at time.Time) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE steward_assessments SET human_decision = $2, decided_at = $3
		 WHERE pending_action_id = $1 AND human_decision IS NULL`, pendingActionID, d, at)
	if err != nil {
		return false, fmt.Errorf("steward: record decision: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// DecisionRecorder adapts the store to pendingaction.DecisionRecorder, which
// speaks in strings so that package need not import this one.
type DecisionRecorder struct{ store *Store }

func NewDecisionRecorder(store *Store) *DecisionRecorder { return &DecisionRecorder{store: store} }

func (r *DecisionRecorder) RecordDecision(ctx context.Context, id uuid.UUID, decision string, at time.Time) error {
	_, err := r.store.RecordDecision(ctx, id, Decision(decision), at)
	return err
}
