package discussionanswer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lowaa/slop-factory/go-db/pgerr"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

type Action string

const (
	ActionApprove Action = "approve"
	ActionEdit    Action = "edit"
	ActionReject  Action = "reject"
)

// ErrAlreadyDecided is a second decision on a label.
var ErrAlreadyDecided = errors.New("discussionanswer: the draft has already been decided")

// ErrNoDecision is a label the owner has not decided.
var ErrNoDecision = errors.New("discussionanswer: no decision for the label")

// Decision is what the owner did with one label's draft.
type Decision struct {
	ID           uuid.UUID
	LabelID      uuid.UUID
	Action       Action
	OriginalBody string
	FinalBody    string
	Reason       string
	Actor        string
	CreatedAt    time.Time
	// PostedAt is when the answer was known to be on the ticket; nil is a
	// claim whose post may or may not have happened. A reject posts nothing
	// and is posted when claimed.
	PostedAt *time.Time
}

// Store persists decisions for the tenant in ctx; row level security enforces
// that as well.
type Store struct{ db *db.DB }

func NewStore(database *db.DB) *Store { return &Store{db: database} }

const columns = `id, label_id, action, original_body, final_body, reason, actor, created_at, posted_at`

func scan(row pgx.Row) (*Decision, error) {
	var d Decision
	if err := row.Scan(&d.ID, &d.LabelID, &d.Action, &d.OriginalBody, &d.FinalBody, &d.Reason, &d.Actor, &d.CreatedAt, &d.PostedAt); err != nil {
		return nil, err
	}
	return &d, nil
}

// Claim records the decision before anything is posted. The unique index on
// the label decides a race: the loser gets ErrAlreadyDecided, and reads the
// winner's claim with ForLabel to see whether its post is still owed.
func (s *Store) Claim(ctx context.Context, d *Decision) (*Decision, error) {
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}
	var postedAt *time.Time
	if d.FinalBody == "" {
		postedAt = &d.CreatedAt
	}
	rec, err := scan(s.db.QueryRow(ctx, `
		INSERT INTO steward_discussion_decisions (id, label_id, action, original_body, final_body, reason, actor, created_at, posted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING `+columns,
		d.ID, d.LabelID, d.Action, d.OriginalBody, d.FinalBody, d.Reason, d.Actor, d.CreatedAt, postedAt))
	if pgerr.IsUniqueViolation(err) {
		return nil, ErrAlreadyDecided
	}
	if err != nil {
		return nil, fmt.Errorf("discussionanswer: claim: %w", err)
	}
	return rec, nil
}

// MarkPosted records that the claim's answer is on the ticket. Repeating it
// keeps the first time.
func (s *Store) MarkPosted(ctx context.Context, id uuid.UUID, at time.Time) error {
	if _, err := s.db.Exec(ctx, `UPDATE steward_discussion_decisions SET posted_at = $2 WHERE id = $1 AND posted_at IS NULL`, id, at); err != nil {
		return fmt.Errorf("discussionanswer: mark posted: %w", err)
	}
	return nil
}

// ForLabel reads the decision on a label.
func (s *Store) ForLabel(ctx context.Context, labelID uuid.UUID) (*Decision, error) {
	d, err := scan(s.db.QueryRow(ctx, `SELECT `+columns+` FROM steward_discussion_decisions WHERE label_id = $1`, labelID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoDecision
	}
	if err != nil {
		return nil, fmt.Errorf("discussionanswer: for label: %w", err)
	}
	return d, nil
}
