package discussionapply

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
)

// ErrNoSchedule is a label the steward is not to apply.
var ErrNoSchedule = errors.New("discussionapply: no schedule for the label")

// Schedule is the steward's plan to post one label's answer.
type Schedule struct {
	LabelID      uuid.UUID
	Tier         steward.Tier
	ApplyAt      time.Time
	VoidedAt     *time.Time
	VoidedReason string
	CreatedAt    time.Time
}

// Due is a schedule that has come due, with the round it is for.
type Due struct {
	Schedule
	TicketID int64
	Attempt  int
	Round    int
}

// Store persists schedules for the tenant in ctx; row level security enforces
// that as well.
type Store struct{ db *db.DB }

func NewStore(database *db.DB) *Store { return &Store{db: database} }

const columns = `label_id, tier, apply_at, voided_at, voided_reason, created_at`

func scan(row pgx.Row) (*Schedule, error) {
	var s Schedule
	if err := row.Scan(&s.LabelID, &s.Tier, &s.ApplyAt, &s.VoidedAt, &s.VoidedReason, &s.CreatedAt); err != nil {
		return nil, err
	}
	return &s, nil
}

// Create records the plan once per label; a repeat returns the first plan, so a
// retried or racing label keeps its original deadline.
func (s *Store) Create(ctx context.Context, labelID uuid.UUID, tier steward.Tier, applyAt, now time.Time) (*Schedule, error) {
	sch, err := scan(s.db.QueryRow(ctx, `
		INSERT INTO steward_discussion_applies (label_id, tier, apply_at, created_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, label_id) DO UPDATE SET label_id = EXCLUDED.label_id
		RETURNING `+columns, labelID, tier, applyAt, now))
	if err != nil {
		return nil, fmt.Errorf("discussionapply: create: %w", err)
	}
	return sch, nil
}

func (s *Store) Get(ctx context.Context, labelID uuid.UUID) (*Schedule, error) {
	sch, err := scan(s.db.QueryRow(ctx, `SELECT `+columns+` FROM steward_discussion_applies WHERE label_id = $1`, labelID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoSchedule
	}
	if err != nil {
		return nil, fmt.Errorf("discussionapply: get: %w", err)
	}
	return sch, nil
}

// Void gives the schedule up, once, leaving the discussion to the owner.
func (s *Store) Void(ctx context.Context, labelID uuid.UUID, at time.Time, reason string) error {
	if _, err := s.db.Exec(ctx, `
		UPDATE steward_discussion_applies SET voided_at = $2, voided_reason = $3
		 WHERE label_id = $1 AND voided_at IS NULL`, labelID, at, reason); err != nil {
		return fmt.Errorf("discussionapply: void: %w", err)
	}
	return nil
}

// ListDue returns the schedules due at now that nobody has voided and whose
// answer is not on the ticket, oldest first. A claim whose post is still owed
// does not count as decided: the sweep finishes it. Only an approve is the
// sweep's to finish; an owner's edit owed its post is theirs to repeat.
func (s *Store) ListDue(ctx context.Context, now time.Time, limit int) ([]Due, error) {
	rows, err := s.db.Query(ctx, `
		SELECT a.label_id, a.tier, a.apply_at, a.voided_at, a.voided_reason, a.created_at, l.ticket_id, l.attempt, l.round
		  FROM steward_discussion_applies a
		  JOIN steward_discussion_labels l ON l.tenant_id = a.tenant_id AND l.id = a.label_id
		 WHERE a.apply_at <= $1 AND a.voided_at IS NULL
		   AND NOT EXISTS (SELECT 1 FROM steward_discussion_decisions d WHERE d.tenant_id = a.tenant_id AND d.label_id = a.label_id AND (d.posted_at IS NOT NULL OR d.action <> 'approve'))
		 ORDER BY a.apply_at LIMIT $2`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("discussionapply: list due: %w", err)
	}
	defer rows.Close()
	var out []Due
	for rows.Next() {
		var d Due
		if err := rows.Scan(&d.LabelID, &d.Tier, &d.ApplyAt, &d.VoidedAt, &d.VoidedReason, &d.CreatedAt, &d.TicketID, &d.Attempt, &d.Round); err != nil {
			return nil, fmt.Errorf("discussionapply: list due: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Unscheduled is a completed routine label that has neither a schedule nor a
// decision.
type Unscheduled struct {
	TicketID int64
	Attempt  int
	Round    int
}

// ListUnscheduled returns the routine labels completed since `since` that have
// no schedule and no decision, newest first. Consider is called once as a
// label completes; one it could not schedule (a failed write, a cancelled
// request) is only found here. The window bounds the scan: a label whose
// category the owner keeps for themselves never gets a schedule and is
// listed on every pass until it ages out.
func (s *Store) ListUnscheduled(ctx context.Context, since time.Time, limit int) ([]Unscheduled, error) {
	rows, err := s.db.Query(ctx, `
		SELECT l.ticket_id, l.attempt, l.round
		  FROM steward_discussion_labels l
		 WHERE l.label = 'routine' AND COALESCE(l.assessed_at, l.created_at) >= $1
		   AND NOT EXISTS (SELECT 1 FROM steward_discussion_applies a WHERE a.tenant_id = l.tenant_id AND a.label_id = l.id)
		   AND NOT EXISTS (SELECT 1 FROM steward_discussion_decisions d WHERE d.tenant_id = l.tenant_id AND d.label_id = l.id)
		 ORDER BY COALESCE(l.assessed_at, l.created_at) DESC LIMIT $2`, since, limit)
	if err != nil {
		return nil, fmt.Errorf("discussionapply: list unscheduled: %w", err)
	}
	defer rows.Close()
	var out []Unscheduled
	for rows.Next() {
		var u Unscheduled
		if err := rows.Scan(&u.TicketID, &u.Attempt, &u.Round); err != nil {
			return nil, fmt.Errorf("discussionapply: list unscheduled: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
