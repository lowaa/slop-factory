package steward

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// VoidKind is why the server declined to apply an assessment.
type VoidKind string

const (
	// VoidChecks is a category check that no longer held when it was re-run.
	VoidChecks VoidKind = "checks"
	// VoidDailyCap is the tenant's daily cap on applications.
	VoidDailyCap VoidKind = "daily_cap"
	// VoidRefused is the approval itself refusing the proposal (stale, voided
	// or refused by the ticket's rules).
	VoidRefused VoidKind = "refused"
	// VoidNotApplicable is an assessment that may never be applied: a
	// needs_human verdict, a never-automatic category, or a tier now Human.
	VoidNotApplicable VoidKind = "not_applicable"
)

// ErrNotApplicable is an assessment that is no longer waiting to be applied:
// it was applied, voided, vetoed, undone or decided already.
var ErrNotApplicable = errors.New("steward: the assessment is not waiting to be applied")

// waiting is the shape of an assessment the steward may still act on.
const waiting = `mode = 'live' AND applied_at IS NULL AND voided_at IS NULL AND vetoed_at IS NULL
	AND undone_at IS NULL AND human_decision IS NULL AND verdict <> 'needs_human'`

// ListDue returns live assessments the steward should act on now: Veto-tier
// ones whose deadline has passed, and ones never scheduled (the server
// stopped between storing the assessment and acting on it) once they are
// older than grace. None has been vetoed, undone, decided or voided. Oldest
// first.
func (s *Store) ListDue(ctx context.Context, now time.Time, grace time.Duration, limit int) ([]Record, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+recordColumns+` FROM steward_assessments
		 WHERE `+waiting+` AND ((veto_deadline IS NOT NULL AND veto_deadline <= $1)
		                     OR (veto_deadline IS NULL AND created_at <= $1 - $2::interval))
		 ORDER BY coalesce(veto_deadline, created_at), id LIMIT $3`, now, grace, limit)
	if err != nil {
		return nil, fmt.Errorf("steward: list due: %w", err)
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("steward: list due: %w", err)
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// ListStuckClaims returns assessments marked applied before the given time
// whose proposal is still pending: the server stopped between the claim and
// the approval.
func (s *Store) ListStuckClaims(ctx context.Context, before time.Time, limit int) ([]Record, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+qualified(recordColumns, "a")+` FROM steward_assessments a
		  JOIN pending_actions pa ON pa.tenant_id = a.tenant_id AND pa.id = a.pending_action_id
		 WHERE a.applied_at IS NOT NULL AND a.applied_at < $1 AND pa.status = 'pending'
		 ORDER BY a.applied_at LIMIT $2`, before, limit)
	if err != nil {
		return nil, fmt.Errorf("steward: list stuck claims: %w", err)
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("steward: list stuck claims: %w", err)
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func qualified(columns, alias string) string {
	parts := strings.Split(columns, ",")
	for i, c := range parts {
		parts[i] = alias + "." + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}

// SetVetoDeadline opens the owner's window on an assessment that is waiting,
// once: a second call leaves the first deadline.
func (s *Store) SetVetoDeadline(ctx context.Context, id uuid.UUID, deadline time.Time) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE steward_assessments SET veto_deadline = $2
		 WHERE id = $1 AND veto_deadline IS NULL AND `+waiting, id, deadline)
	if err != nil {
		return fmt.Errorf("steward: set veto deadline: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotApplicable
	}
	return nil
}

// Claim marks a waiting assessment applied, unless dailyCap applications have
// already been made since the given time (ErrDailyCap). The count and the mark share one transaction behind
// the per-tenant lock, so concurrent applications cannot both pass the cap.
// The claim is made before the approval, so a crash in between leaves a
// pending proposal marked applied; it never leaves an approval unrecorded
// or the cap exceeded.
func (s *Store) Claim(ctx context.Context, id uuid.UUID, at, since time.Time, dailyCap int) error {
	return s.db.InTx(ctx, func(q db.Querier) error {
		if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_tenant()::text, 1))`); err != nil {
			return err
		}
		var used int
		if err := q.QueryRow(ctx, `SELECT count(*) FROM steward_assessments WHERE applied_at >= $1`, since).Scan(&used); err != nil {
			return err
		}
		if used >= dailyCap {
			return ErrDailyCap
		}
		tag, err := q.Exec(ctx, `UPDATE steward_assessments SET applied_at = $2 WHERE id = $1 AND `+waiting, id, at)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrNotApplicable
		}
		return nil
	})
}

// Void records that the server declined to apply an assessment that has not
// been applied, and why. A voided assessment is the owner's to decide.
func (s *Store) Void(ctx context.Context, id uuid.UUID, kind VoidKind, reason string, at time.Time) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE steward_assessments SET voided_at = $2, void_kind = $3, void_reason = $4
		 WHERE id = $1 AND applied_at IS NULL AND voided_at IS NULL`, id, at, kind, reason)
	if err != nil {
		return fmt.Errorf("steward: void assessment: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotApplicable
	}
	return nil
}

// Unclaim turns a claim into a void when the approval that followed it did
// not go through.
func (s *Store) Unclaim(ctx context.Context, id uuid.UUID, kind VoidKind, reason string, at time.Time) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE steward_assessments SET applied_at = NULL, voided_at = $2, void_kind = $3, void_reason = $4
		 WHERE id = $1 AND applied_at IS NOT NULL AND voided_at IS NULL`, id, at, kind, reason)
	if err != nil {
		return fmt.Errorf("steward: unclaim assessment: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotApplicable
	}
	return nil
}

// Applied counts the decisions the steward applied since the given time.
func (s *Store) Applied(ctx context.Context, since time.Time) (int, error) {
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM steward_assessments WHERE applied_at >= $1`, since).Scan(&n); err != nil {
		return 0, fmt.Errorf("steward: count applied: %w", err)
	}
	return n, nil
}

// CapHits counts the assessments the daily cap stopped from being applied
// since the given time, for the board to say the steward hit its limit.
func (s *Store) CapHits(ctx context.Context, since time.Time) (int, error) {
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM steward_assessments WHERE void_kind = 'daily_cap' AND voided_at >= $1`, since).Scan(&n); err != nil {
		return 0, fmt.Errorf("steward: count cap hits: %w", err)
	}
	return n, nil
}

// GetByAction returns the assessment of a proposal, ErrNotFound when it has
// none.
func (s *Store) GetByAction(ctx context.Context, proposalID uuid.UUID) (*Record, error) {
	found, err := s.GetByActions(ctx, []uuid.UUID{proposalID})
	if err != nil {
		return nil, err
	}
	rec, ok := found[proposalID]
	if !ok {
		return nil, ErrNotFound
	}
	return rec, nil
}

// Veto cancels an assessment the steward has not applied yet: the owner took
// the decision back inside its window. The proposal stays pending for them.
// It is ErrNotApplicable once the steward applied it (that is an Undo), or
// voided, vetoed or decided it already. The mark shares the row with Claim's,
// so an application and a veto cannot both win.
func (s *Store) Veto(ctx context.Context, id, by uuid.UUID, at time.Time) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE steward_assessments SET vetoed_at = $2, vetoed_by = $3
		 WHERE id = $1 AND veto_deadline IS NOT NULL AND `+waiting, id, at, by)
	if err != nil {
		return fmt.Errorf("steward: veto assessment: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotApplicable
	}
	return nil
}

// MarkUndone records that the owner took back an application, once.
func (s *Store) MarkUndone(ctx context.Context, id uuid.UUID, at time.Time) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE steward_assessments SET undone_at = $2
		 WHERE id = $1 AND applied_at IS NOT NULL AND undone_at IS NULL`, id, at)
	if err != nil {
		return fmt.Errorf("steward: mark undone: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotApplicable
	}
	return nil
}

// ListWaiting returns live assessments still waiting to be applied whose
// proposal is still pending, oldest first: what the owner may yet veto.
// Whatever their age: the applier does not drop an old one.
func (s *Store) ListWaiting(ctx context.Context, limit int) ([]Record, error) {
	return s.listRecords(ctx, `SELECT `+recordColumns+` FROM steward_assessments
		 WHERE `+waiting+` AND EXISTS (SELECT 1 FROM pending_actions pa
		        WHERE pa.tenant_id = steward_assessments.tenant_id AND pa.id = steward_assessments.pending_action_id AND pa.status = 'pending')
		 ORDER BY coalesce(veto_deadline, created_at), id LIMIT $1`, limit)
}

// ListRecentlyApplied returns assessments applied since the given time and not
// undone, whose proposal has been approved: an application still in flight
// (claimed, proposal pending) is not one the owner can undo yet. Newest first.
func (s *Store) ListRecentlyApplied(ctx context.Context, since time.Time, limit int) ([]Record, error) {
	return s.listRecords(ctx, `SELECT `+recordColumns+` FROM steward_assessments
		 WHERE applied_at >= $2 AND undone_at IS NULL AND EXISTS (SELECT 1 FROM pending_actions pa
		        WHERE pa.tenant_id = steward_assessments.tenant_id AND pa.id = steward_assessments.pending_action_id AND pa.status <> 'pending')
		 ORDER BY applied_at DESC, id LIMIT $1`, limit, since)
}

func (s *Store) listRecords(ctx context.Context, query string, args ...any) ([]Record, error) {
	rows, err := s.db.Query(ctx, query, args...)
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
