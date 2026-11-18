package assist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
)

var (
	// ErrNotFound is a draft that does not exist in the caller's tenant.
	ErrNotFound = errors.New("assist: draft not found")
	// ErrAlreadyDrafted is a second draft for one proposal.
	ErrAlreadyDrafted = errors.New("assist: the proposal already has a draft")
	// ErrAlreadyApplied is a draft that was applied, or is notes only.
	ErrAlreadyApplied = errors.New("assist: the draft was already applied")
)

// Draft is the decision the steward pre-drafted for one pending approval.
type Draft struct {
	ID              uuid.UUID
	TicketID        int64
	PendingActionID uuid.UUID
	Category        string
	Kind            string
	// Body is the answer text, the review notes or a summary of the edit.
	Body string
	// WouldSendBack marks a review the steward would request changes on: its
	// notes are for the owner to read, and it is never applied.
	WouldSendBack bool
	Reason        string
	// Evidence is every check, pass or fail, as it stood when drafted.
	Evidence []steward.Evidence
	// TicketRevision and HeadSHA are what the draft was read against.
	TicketRevision int
	HeadSHA        string
	AgentID        uuid.UUID
	CreatedAt      time.Time
	AppliedAt      *time.Time
	AppliedBy      *uuid.UUID
}

// Store persists drafts for the tenant in ctx; row level security enforces
// that as well.
type Store struct {
	db *db.DB
}

func NewStore(database *db.DB) *Store { return &Store{db: database} }

const draftColumns = `id, ticket_id, pending_action_id, category, kind, body, would_send_back, reason, evidence,
	ticket_revision, coalesce(head_sha, ''), agent_id, created_at, applied_at, applied_by`

func scanDraft(row pgx.Row) (*Draft, error) {
	var d Draft
	var evidence []byte
	if err := row.Scan(&d.ID, &d.TicketID, &d.PendingActionID, &d.Category, &d.Kind, &d.Body, &d.WouldSendBack, &d.Reason,
		&evidence, &d.TicketRevision, &d.HeadSHA, &d.AgentID, &d.CreatedAt, &d.AppliedAt, &d.AppliedBy); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(evidence, &d.Evidence); err != nil {
		return nil, fmt.Errorf("assist: read evidence: %w", err)
	}
	return &d, nil
}

// Create stores a draft; a proposal has one.
func (s *Store) Create(ctx context.Context, d *Draft) (*Draft, error) {
	evidence := d.Evidence
	if evidence == nil {
		evidence = []steward.Evidence{}
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		return nil, fmt.Errorf("assist: encode evidence: %w", err)
	}
	var head *string
	if d.HeadSHA != "" {
		head = &d.HeadSHA
	}
	created, err := scanDraft(s.db.QueryRow(ctx, `
		INSERT INTO steward_drafts (id, ticket_id, pending_action_id, category, kind, body, would_send_back, reason, evidence,
			ticket_revision, head_sha, agent_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (tenant_id, pending_action_id) DO NOTHING
		RETURNING `+draftColumns,
		uuid.New(), d.TicketID, d.PendingActionID, d.Category, d.Kind, d.Body, d.WouldSendBack, d.Reason, raw,
		d.TicketRevision, head, d.AgentID, d.CreatedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAlreadyDrafted
	}
	if err != nil {
		return nil, fmt.Errorf("assist: create draft: %w", err)
	}
	return created, nil
}

// ByAction returns the drafts of the given proposals, keyed by proposal.
func (s *Store) ByAction(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]*Draft, error) {
	rows, err := s.db.Query(ctx, `SELECT `+draftColumns+` FROM steward_drafts WHERE pending_action_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("assist: list drafts: %w", err)
	}
	defer rows.Close()
	out := map[uuid.UUID]*Draft{}
	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			return nil, fmt.Errorf("assist: list drafts: %w", err)
		}
		out[d.PendingActionID] = d
	}
	return out, rows.Err()
}

// Get returns the draft made for a proposal.
func (s *Store) Get(ctx context.Context, proposalID uuid.UUID) (*Draft, error) {
	got, err := s.ByAction(ctx, []uuid.UUID{proposalID})
	if err != nil {
		return nil, err
	}
	d, ok := got[proposalID]
	if !ok {
		return nil, ErrNotFound
	}
	return d, nil
}

// Claim marks a draft applied by the owner, once: a second call is
// ErrAlreadyApplied, so two clicks cannot both approve. A claim older than
// staleBefore whose proposal is still pending was interrupted before its
// approval, and is taken over.
func (s *Store) Claim(ctx context.Context, id, owner uuid.UUID, at, staleBefore time.Time) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE steward_drafts d SET applied_at = $3, applied_by = $2
		 WHERE d.id = $1 AND NOT d.would_send_back
		   AND (d.applied_at IS NULL OR (d.applied_at < $4 AND EXISTS (SELECT 1 FROM pending_actions pa
		        WHERE pa.tenant_id = d.tenant_id AND pa.id = d.pending_action_id AND pa.status = 'pending')))`,
		id, owner, at, staleBefore)
	if err != nil {
		return fmt.Errorf("assist: claim draft: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrAlreadyApplied
	}
	return nil
}

// Unclaim reverses a claim whose approval did not go through.
func (s *Store) Unclaim(ctx context.Context, id uuid.UUID) error {
	if _, err := s.db.Exec(ctx, `UPDATE steward_drafts SET applied_at = NULL, applied_by = NULL WHERE id = $1`, id); err != nil {
		return fmt.Errorf("assist: unclaim draft: %w", err)
	}
	return nil
}
