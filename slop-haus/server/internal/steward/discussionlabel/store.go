package discussionlabel

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

type Label string

const (
	LabelRoutine     Label = "routine"
	LabelYours       Label = "yours"
	LabelNotAssessed Label = "not_assessed"
	// LabelPending is a claimed round whose classification is running.
	LabelPending Label = "pending"
)

var (
	// ErrNotPending is completing a label that no run holds.
	ErrNotPending = errors.New("discussionlabel: the label is not awaiting a run")
	// ErrNotFound is a round with no label.
	ErrNotFound = errors.New("discussionlabel: no label for the round")
	// ErrNotAssessable is assessing a label that is not not_assessed.
	ErrNotAssessable = errors.New("discussionlabel: only a not-assessed label can be assessed now")
	// ErrStale is completing a label for a discussion that moved since the
	// label was made: its ticket revision or change head differ.
	ErrStale = errors.New("discussionlabel: the discussion has moved since the label was queued")
	// ErrDailyCap is a run refused because the steward's daily cap is spent.
	ErrDailyCap = steward.ErrDailyCap
)

// CapReason is the reason a not-assessed label carries.
const CapReason = "daily steward cap reached"

// Record is one stored label.
type Record struct {
	ID             uuid.UUID
	TicketID       int64
	Attempt        int
	Round          int
	Label          Label
	Category       string
	Reason         string
	Reasoning      string
	Evidence       []steward.Evidence
	AssessmentID   *uuid.UUID
	TicketRevision *int
	HeadSHA        *string
	AssessedAt     *time.Time
	CreatedAt      time.Time
}

// Assessable says whether assessing now may run the round: its label is
// not_assessed, or a claimed run outlived PendingTTL.
func (r *Record) Assessable(now time.Time) bool {
	return r.Label == LabelNotAssessed ||
		(r.Label == LabelPending && r.AssessedAt != nil && r.AssessedAt.Before(now.Add(-PendingTTL)))
}

// Outcome is a label as the service decided it, before it is stored.
type Outcome struct {
	Label     Label
	Category  string
	Reason    string
	Reasoning string
	Evidence  []steward.Evidence
}

// Target is the discussion round (ticket, attempt, round number) and what the
// label is made against.
type Target struct {
	TicketID int64
	Attempt  int
	Round    int
	Revision int
	HeadSHA  string
}

// Store persists labels for the tenant in ctx; row level security enforces
// that as well.
type Store struct{ db *db.DB }

func NewStore(database *db.DB) *Store { return &Store{db: database} }

const columns = `id, ticket_id, attempt, round, label, category, reason, reasoning, evidence, assessment_id, ticket_revision, head_sha, assessed_at, created_at`

func scan(row pgx.Row) (*Record, error) {
	var r Record
	var evidence []byte
	if err := row.Scan(&r.ID, &r.TicketID, &r.Attempt, &r.Round, &r.Label, &r.Category, &r.Reason, &r.Reasoning, &evidence,
		&r.AssessmentID, &r.TicketRevision, &r.HeadSHA, &r.AssessedAt, &r.CreatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(evidence, &r.Evidence); err != nil {
		return nil, fmt.Errorf("discussionlabel: read evidence: %w", err)
	}
	return &r, nil
}

func encodeEvidence(ev []steward.Evidence) ([]byte, error) {
	if ev == nil {
		ev = []steward.Evidence{}
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return nil, fmt.Errorf("discussionlabel: encode evidence: %w", err)
	}
	return raw, nil
}

// Get reads the label of one round of one attempt's discussion.
func (s *Store) Get(ctx context.Context, ticketID int64, attempt, round int) (*Record, error) {
	r, err := scan(s.db.QueryRow(ctx, `SELECT `+columns+` FROM steward_discussion_labels WHERE ticket_id = $1 AND attempt = $2 AND round = $3`,
		ticketID, attempt, round))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("discussionlabel: get: %w", err)
	}
	return r, nil
}

// Latest reads the label of the attempt's newest labelled round.
func (s *Store) Latest(ctx context.Context, ticketID int64, attempt int) (*Record, error) {
	r, err := scan(s.db.QueryRow(ctx, `SELECT `+columns+` FROM steward_discussion_labels WHERE ticket_id = $1 AND attempt = $2 ORDER BY round DESC LIMIT 1`,
		ticketID, attempt))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("discussionlabel: latest: %w", err)
	}
	return r, nil
}

// lockAndCount takes the tenant's steward lock, shared with assessments, and
// counts the runs since the given time: assessments, plus classification runs
// that no assessment anchors (an anchored run is already one assessment).
func lockAndCount(ctx context.Context, q db.Querier, since time.Time) (int, error) {
	if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_tenant()::text, 0))`); err != nil {
		return 0, err
	}
	var used int
	err := q.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM steward_assessments WHERE created_at >= $1)
		     + (SELECT count(*) FROM steward_discussion_labels WHERE assessed_at >= $1 AND assessment_id IS NULL)`, since).Scan(&used)
	return used, err
}

// Claim takes the round for one classification run, before the classifier is
// asked, so duplicate deliveries and replicas race to a single run. The
// winner gets a pending label (counted against the cap now, so a race cannot
// spend it twice) and claimed is true; with the cap spent the row is
// not_assessed and no run follows. The loser gets the winner's row and
// claimed is false.
func (s *Store) Claim(ctx context.Context, t *Target, now, since time.Time, dailyCap int) (rec *Record, claimed bool, err error) {
	err = s.db.InTx(ctx, func(q db.Querier) error {
		used, err := lockAndCount(ctx, q, since)
		if err != nil {
			return err
		}
		label, reason, assessedAt := LabelPending, "", &now
		if used >= dailyCap {
			label, reason, assessedAt = LabelNotAssessed, CapReason, nil
		}
		rec, err = scan(q.QueryRow(ctx, `
			INSERT INTO steward_discussion_labels (id, ticket_id, attempt, round, label, reason, ticket_revision, head_sha, assessed_at, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (tenant_id, ticket_id, attempt, round) DO NOTHING
			RETURNING `+columns,
			uuid.New(), t.TicketID, t.Attempt, t.Round, label, reason, t.Revision, nullString(t.HeadSHA), assessedAt, now))
		if errors.Is(err, pgx.ErrNoRows) {
			// Held under the same lock as the claim, so the winner's row is
			// committed and visible.
			rec, err = scan(q.QueryRow(ctx, `SELECT `+columns+` FROM steward_discussion_labels WHERE ticket_id = $1 AND attempt = $2 AND round = $3`,
				t.TicketID, t.Attempt, t.Round))
			return err
		}
		claimed = label == LabelPending
		return err
	})
	if err != nil {
		return nil, false, fmt.Errorf("discussionlabel: claim: %w", err)
	}
	return rec, claimed, nil
}

// Skip records the round as not assessed for a reason that is not the cap, so
// the board has a label to show and assessing now can run it later. It is
// idempotent on the round: an existing label is returned as it is.
func (s *Store) Skip(ctx context.Context, t *Target, now time.Time, reason string) (*Record, error) {
	var rec *Record
	err := s.db.InTx(ctx, func(q db.Querier) error {
		var err error
		rec, err = scan(q.QueryRow(ctx, `
			INSERT INTO steward_discussion_labels (id, ticket_id, attempt, round, label, reason, ticket_revision, head_sha, created_at)
			VALUES ($1, $2, $3, $4, 'not_assessed', $5, $6, $7, $8)
			ON CONFLICT (tenant_id, ticket_id, attempt, round) DO NOTHING
			RETURNING `+columns,
			uuid.New(), t.TicketID, t.Attempt, t.Round, reason, t.Revision, nullString(t.HeadSHA), now))
		if errors.Is(err, pgx.ErrNoRows) {
			rec, err = scan(q.QueryRow(ctx, `SELECT `+columns+` FROM steward_discussion_labels WHERE ticket_id = $1 AND attempt = $2 AND round = $3`,
				t.TicketID, t.Attempt, t.Round))
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("discussionlabel: skip: %w", err)
	}
	return rec, nil
}

// Complete stores the outcome of the run that claimed the label.
func (s *Store) Complete(ctx context.Context, id uuid.UUID, o *Outcome) (*Record, error) {
	raw, err := encodeEvidence(o.Evidence)
	if err != nil {
		return nil, err
	}
	rec, err := scan(s.db.QueryRow(ctx, `
		UPDATE steward_discussion_labels
		   SET label = $2, category = $3, reason = $4, reasoning = $5, evidence = $6
		 WHERE id = $1 AND label = 'pending'
		RETURNING `+columns,
		id, o.Label, o.Category, o.Reason, o.Reasoning, raw))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotPending
	}
	if err != nil {
		return nil, fmt.Errorf("discussionlabel: complete: %w", err)
	}
	return rec, nil
}

// CompleteAgainst is Complete for a run that was claimed earlier, fenced to the
// revision and head the label was claimed against in t: a label that has since
// timed out and been queued again for a newer state is not completed by the
// older run (ErrNotPending).
func (s *Store) CompleteAgainst(ctx context.Context, id uuid.UUID, t *Target, o *Outcome) (*Record, error) {
	raw, err := encodeEvidence(o.Evidence)
	if err != nil {
		return nil, err
	}
	rec, err := scan(s.db.QueryRow(ctx, `
		UPDATE steward_discussion_labels
		   SET label = $2, category = $3, reason = $4, reasoning = $5, evidence = $6
		 WHERE id = $1 AND label = 'pending' AND ticket_revision = $7 AND head_sha IS NOT DISTINCT FROM $8
		RETURNING `+columns,
		id, o.Label, o.Category, o.Reason, o.Reasoning, raw, t.Revision, nullString(t.HeadSHA)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotPending
	}
	if err != nil {
		return nil, fmt.Errorf("discussionlabel: complete: %w", err)
	}
	return rec, nil
}

// ExpirePending turns the pending labels claimed before cutoff into
// not_assessed with the reason, so a round the steward never labelled shows
// as such and assessing now can queue it again. A not_assessed label has no
// assessed_at (the table requires it), so the claim stops counting against the
// cap: no run happened. It reports how many it turned.
func (s *Store) ExpirePending(ctx context.Context, cutoff time.Time, reason string) (int, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE steward_discussion_labels SET label = 'not_assessed', reason = $2, assessed_at = NULL
		 WHERE label = 'pending' AND assessed_at < $1`, cutoff, reason)
	if err != nil {
		return 0, fmt.Errorf("discussionlabel: expire pending: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// PendingTTL is how long a claimed run is given to finish. A process that died
// between Claim and Complete leaves the label pending; past this it may be
// run again by assessing now.
const PendingTTL = 10 * time.Minute

// ClaimNotAssessed turns a not-assessed label, or one whose run was claimed
// more than PendingTTL ago and never finished, into a pending one for a new
// run, counted against the cap of the day it is made, against the revision and
// head now in t. ErrDailyCap when the cap is spent, ErrNotAssessable when the
// label is neither.
func (s *Store) ClaimNotAssessed(ctx context.Context, id uuid.UUID, t *Target, now, since time.Time, dailyCap int) (*Record, error) {
	var rec *Record
	err := s.db.InTx(ctx, func(q db.Querier) error {
		used, err := lockAndCount(ctx, q, since)
		if err != nil {
			return err
		}
		if used >= dailyCap {
			return ErrDailyCap
		}
		rec, err = scan(q.QueryRow(ctx, `
			UPDATE steward_discussion_labels
			   SET label = 'pending', reason = '', ticket_revision = $2, head_sha = $3, assessed_at = $4
			 WHERE id = $1 AND (label = 'not_assessed' OR (label = 'pending' AND assessed_at < $5))
			RETURNING `+columns,
			id, t.Revision, nullString(t.HeadSHA), now, now.Add(-PendingTTL)))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotAssessable
		}
		return err
	})
	if errors.Is(err, ErrDailyCap) || errors.Is(err, ErrNotAssessable) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("discussionlabel: assess now: %w", err)
	}
	return rec, nil
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Round names a discussion round of an attempt.
type Round struct {
	TicketID int64
	Attempt  int
	Round    int
}

// Unlabelled lists up to limit open discussion rounds with no label, oldest
// ticket first. A round opened before labels existed has discussion_round 0
// and is read as round 1, as the board reads it.
func (s *Store) Unlabelled(ctx context.Context, limit int) ([]Round, error) {
	rows, err := s.db.Query(ctx, `
		SELECT a.ticket_id, a.number, GREATEST(a.discussion_round, 1)
		  FROM attempts a
		  LEFT JOIN steward_discussion_labels l
		    ON l.tenant_id = a.tenant_id AND l.ticket_id = a.ticket_id AND l.attempt = a.number AND l.round = GREATEST(a.discussion_round, 1)
		 WHERE a.status = 'discussion' AND l.id IS NULL
		 ORDER BY a.ticket_id, a.number
		 LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("discussionlabel: list unlabelled rounds: %w", err)
	}
	defer rows.Close()
	var out []Round
	for rows.Next() {
		var r Round
		if err := rows.Scan(&r.TicketID, &r.Attempt, &r.Round); err != nil {
			return nil, fmt.Errorf("discussionlabel: list unlabelled rounds: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
