package change

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Record is a change slop-haus opened for a ticket.
type Record struct {
	ID       int64
	TicketID int64
	Attempt  int
	Change   codehost.ChangeRef
	// Head is the branch the change was opened from, Base the one it merges
	// into.
	Head, Base string
	Draft      bool
	OpenedBy   uuid.UUID
	OpenedAt   time.Time
	MergedAt   *time.Time
	MergedBy   *uuid.UUID
	MergeSHA   string
	// ReviewedHead is the head commit the change had when it was last handed
	// to a human for review; empty if it never was.
	ReviewedHead string
	// ReviewedDiff is the fingerprint of the diff the human was asked to
	// review and ReviewedBase the base tip it was read against; empty when
	// the host's diff could not be compared, so only ReviewedHead is covered.
	ReviewedDiff, ReviewedBase string
	// Merging is whether a merge of this change is in flight right now.
	Merging bool
	// ClosedAt is when slop-haus closed the change on the host without
	// merging it, and ClosedReason why (closedByRule); zero when it did not.
	ClosedAt     *time.Time
	ClosedReason string
	// SupersededAt is when a newer change for the same attempt replaced this
	// one.
	SupersededAt *time.Time
}

// closedByRule is the reason recorded for a change the closing rule closed.
const closedByRule = "closing_rule"

// Reviewed is what a hand-over for review put in front of the human.
type Reviewed struct {
	// HeadSHA is the head commit; always set.
	HeadSHA string
	// DiffFingerprint and BaseSHA are the digest of the change's diff
	// (codehost.Fingerprint) and the base tip it was read against; empty
	// when the diff could not be compared.
	DiffFingerprint, BaseSHA string
}

// Store is where opened changes are kept. Only a row here is a change
// change_merge will act on.
type Store interface {
	// Current is the latest change of one attempt of the ticket that was not
	// superseded: the live one, or the merged one it ended with. ErrNoChange
	// when the attempt has none. A ticket's attempts each have their own, so
	// a lease asks for its attempt's and never reads a sibling's.
	Current(ctx context.Context, ticketID int64, attempt int) (*Record, error)
	// Open records a new change for the opener's attempt of the ticket, in the
	// transaction q belongs to, superseding that attempt's live one if it has
	// one: an attempt never has two live changes, and never replaces a
	// sibling's.
	Open(ctx context.Context, q db.Querier, rec *Record) (*Record, error)
	// SetBaseCommit records, in the transaction q belongs to, the main commit
	// the ticket's work started from. A ticket that has one keeps it: the
	// base is the branch point, which later opens must not move.
	SetBaseCommit(ctx context.Context, q db.Querier, ticketID int64, sha string) error
	// SetBase records the branch the change now targets on the host, after a
	// retarget. A change that is no longer live is left alone.
	SetBase(ctx context.Context, id int64, base string) error
	// MarkReady records that the change has left draft.
	MarkReady(ctx context.Context, id int64) error
	// SetReviewed records, in the transaction q belongs to, what the change
	// was handed over for review as. It is the one write of it: an approval
	// is only ever of what was recorded last, and each field is replaced, an
	// empty one cleared.
	SetReviewed(ctx context.Context, q db.Querier, id int64, rev *Reviewed) error
	// ClearReviewedHead forgets, in the transaction q belongs to, the head
	// commit recorded by an earlier hand-over, for a hand-over that is not of
	// the change: the merge is refused until the change is handed over again.
	// A change that is no longer live has nothing to clear and is not an error.
	ClearReviewedHead(ctx context.Context, q db.Querier, id int64) error
	// BeginMerge claims the change for a merge, returning the claim, or nil
	// when another merge of it is in flight. Open refuses to supersede a
	// change while it is claimed.
	BeginMerge(ctx context.Context, id int64) (*MergeClaim, error)
	// EndMerge gives up a claim for a merge that did not happen. It ends
	// only that claim: one that expired and was taken by another merge is
	// left alone.
	EndMerge(ctx context.Context, claim *MergeClaim) error
	// MarkMerged records the merge. It reports false when the change was
	// not live any more (already merged, or superseded).
	MarkMerged(ctx context.Context, id int64, by uuid.UUID, sha string) (bool, error)

	// RecordDriftDecision keeps what the independent-apps rule said about a
	// head behind the base. The same change, head and tip is kept once.
	RecordDriftDecision(ctx context.Context, d *DriftDecision) error
	// PendingDriftDecisions is how many of the change's decisions, made for a
	// head other than headSHA, have no outcome yet.
	PendingDriftDecisions(ctx context.Context, changeID int64, headSHA string) (int, error)
	// LinkDriftOutcome records, on the change's decisions made for another
	// head and not yet linked, whether the checks passed on headSHA. It
	// returns how many it linked.
	LinkDriftOutcome(ctx context.Context, changeID int64, headSHA string, passed bool) (int, error)

	// DueClosings is the live changes the closing rule says to close: an
	// unpromoted attempt's, whose ticket is done (or parked, for a replay's)
	// and whose attempt has finished (review, closed or done). ticketID 0 means every ticket.
	DueClosings(ctx context.Context, ticketID int64) ([]*Record, error)
	// MarkClosed records the change as closed by reason, once. It reports
	// false when it was no longer live or already closed.
	MarkClosed(ctx context.Context, id int64, reason string) (bool, error)
	// DeletableBases is the ticket base branches that may be deleted from the
	// host: the ticket is done, nothing live targets the branch, and no
	// unfinished unpromoted attempt can still need it. ticketID 0 means every
	// ticket.
	DeletableBases(ctx context.Context, ticketID int64) ([]BaseBranch, error)
	// MarkBaseDeleted records that the host no longer has the ticket's base
	// branch, on every change that targeted it.
	MarkBaseDeleted(ctx context.Context, b BaseBranch) error
}

// BaseBranch is a ticket's base branch in a repository.
type BaseBranch struct {
	TicketID int64
	Host     string
	Repo     codehost.Repo
	Name     string
}

// MergeClaim is one claim of a change for a merge.
type MergeClaim struct {
	ID    int64
	Since time.Time
}

// ErrMergeInFlight is a change that is being merged: it is neither replaced
// nor merged a second time until that merge ends.
var ErrMergeInFlight = errors.New("change: a merge of this ticket's change is in progress")

// mergeClaimBound is how long a merge claim holds: a merge that never ended
// (the process died between the claim and its end) stops blocking after it.
// It is far longer than the call to the host the claim covers, which has a
// timeout of seconds, so a claim does not expire under a merge still running.
const mergeClaimBound = "5 minutes"

// ErrNoChange is a ticket with no change slop-haus opened for it.
var ErrNoChange = errors.New("change: slop-haus has not opened a change for this ticket")

// PostgresStore is Store over the ticket_changes table, scoped to the
// tenant on the context by row-level security.
type PostgresStore struct{ db *db.DB }

func NewPostgresStore(database *db.DB) *PostgresStore { return &PostgresStore{db: database} }

var _ Store = (*PostgresStore)(nil)

const recordColumns = `id, ticket_id, host, owner, repo, number, head_branch, base_branch, draft,
	opened_by_agent_id, opened_at, merged_at, merged_by_agent_id, COALESCE(merge_sha, ''), COALESCE(reviewed_head_sha, ''),
	COALESCE(reviewed_diff_fingerprint, ''), COALESCE(reviewed_base_sha, ''),
	COALESCE(merging_since > now() - interval '` + mergeClaimBound + `', false),
	closed_at, COALESCE(closed_reason, ''), attempt_number, superseded_at`

func scanRecord(row pgx.Row) (*Record, error) {
	var r Record
	err := row.Scan(&r.ID, &r.TicketID, &r.Change.Host, &r.Change.Owner, &r.Change.Repo, &r.Change.Number,
		&r.Head, &r.Base, &r.Draft, &r.OpenedBy, &r.OpenedAt, &r.MergedAt, &r.MergedBy, &r.MergeSHA, &r.ReviewedHead, &r.ReviewedDiff, &r.ReviewedBase, &r.Merging, &r.ClosedAt, &r.ClosedReason, &r.Attempt, &r.SupersededAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *PostgresStore) Current(ctx context.Context, ticketID int64, attempt int) (*Record, error) {
	r, err := scanRecord(s.db.QueryRow(ctx, `
		SELECT `+recordColumns+` FROM ticket_changes
		 WHERE ticket_id = $1 AND attempt_number = $2 AND superseded_at IS NULL
		 ORDER BY id DESC LIMIT 1`, ticketID, attempt))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoChange
	}
	if err != nil {
		return nil, fmt.Errorf("change: load: %w", err)
	}
	return r, nil
}

func (s *PostgresStore) Open(ctx context.Context, q db.Querier, rec *Record) (*Record, error) {
	// The attempt the opening agent's lease is for: only its live change is
	// replaced.
	attempt, err := ticket.HeldAttemptOf(ctx, q, rec.OpenedBy, rec.TicketID)
	if err != nil {
		return nil, fmt.Errorf("change: record: %w", err)
	}
	// The live change is locked first, which is what orders this against a
	// merge claiming it.
	var merging bool
	err = q.QueryRow(ctx, `
		SELECT COALESCE(merging_since > now() - interval '`+mergeClaimBound+`', false) FROM ticket_changes
		 WHERE ticket_id = $1 AND attempt_number = $2 AND superseded_at IS NULL AND merged_at IS NULL FOR UPDATE`,
		rec.TicketID, attempt.Number).Scan(&merging)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("change: record: %w", err)
	}
	if merging {
		return nil, ErrMergeInFlight
	}
	if _, err := q.Exec(ctx, `
		UPDATE ticket_changes SET superseded_at = now()
		 WHERE ticket_id = $1 AND attempt_number = $2 AND superseded_at IS NULL AND merged_at IS NULL`,
		rec.TicketID, attempt.Number); err != nil {
		return nil, fmt.Errorf("change: record: %w", err)
	}
	out, err := scanRecord(q.QueryRow(ctx, `
		INSERT INTO ticket_changes (ticket_id, attempt_number, host, owner, repo, number, head_branch, base_branch, draft, opened_by_agent_id)
		VALUES ($1, $10, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING `+recordColumns,
		rec.TicketID, rec.Change.Host, rec.Change.Owner, rec.Change.Repo, rec.Change.Number,
		rec.Head, rec.Base, rec.Draft, rec.OpenedBy, attempt.Number))
	if err != nil {
		return nil, fmt.Errorf("change: record: %w", err)
	}
	return out, nil
}

func (s *PostgresStore) SetBaseCommit(ctx context.Context, q db.Querier, ticketID int64, sha string) error {
	if _, err := q.Exec(ctx, `UPDATE tickets SET base_commit = $2 WHERE id = $1 AND base_commit IS NULL`, ticketID, sha); err != nil {
		return fmt.Errorf("change: record base commit: %w", err)
	}
	return nil
}

func (s *PostgresStore) SetBase(ctx context.Context, id int64, base string) error {
	if _, err := s.db.Exec(ctx, `UPDATE ticket_changes SET base_branch = $2 WHERE id = $1 AND merged_at IS NULL AND superseded_at IS NULL`, id, base); err != nil {
		return fmt.Errorf("change: record base: %w", err)
	}
	return nil
}

func (s *PostgresStore) SetReviewed(ctx context.Context, q db.Querier, id int64, rev *Reviewed) error {
	tag, err := q.Exec(ctx, `
		UPDATE ticket_changes SET reviewed_head_sha = $2, reviewed_diff_fingerprint = NULLIF($3, ''), reviewed_base_sha = NULLIF($4, '')
		 WHERE id = $1 AND merged_at IS NULL AND superseded_at IS NULL`, id, rev.HeadSHA, rev.DiffFingerprint, rev.BaseSHA)
	if err != nil {
		return fmt.Errorf("change: record reviewed head: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNoChange
	}
	return nil
}

func (s *PostgresStore) ClearReviewedHead(ctx context.Context, q db.Querier, id int64) error {
	if _, err := q.Exec(ctx, `UPDATE ticket_changes SET reviewed_head_sha = NULL, reviewed_diff_fingerprint = NULL, reviewed_base_sha = NULL WHERE id = $1 AND merged_at IS NULL AND superseded_at IS NULL`, id); err != nil {
		return fmt.Errorf("change: clear reviewed head: %w", err)
	}
	return nil
}

func (s *PostgresStore) BeginMerge(ctx context.Context, id int64) (*MergeClaim, error) {
	claim := &MergeClaim{ID: id}
	err := s.db.QueryRow(ctx, `
		UPDATE ticket_changes SET merging_since = now()
		 WHERE id = $1 AND merged_at IS NULL AND superseded_at IS NULL
		   AND (merging_since IS NULL OR merging_since <= now() - interval '`+mergeClaimBound+`')
		RETURNING merging_since`, id).Scan(&claim.Since)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("change: claim merge: %w", err)
	}
	return claim, nil
}

func (s *PostgresStore) EndMerge(ctx context.Context, claim *MergeClaim) error {
	if _, err := s.db.Exec(ctx, `UPDATE ticket_changes SET merging_since = NULL WHERE id = $1 AND merging_since = $2`, claim.ID, claim.Since); err != nil {
		return fmt.Errorf("change: end merge: %w", err)
	}
	return nil
}

func (s *PostgresStore) RecordDriftDecision(ctx context.Context, d *DriftDecision) error {
	dirs := d.Dirs
	if dirs == nil {
		dirs = []string{}
	}
	if _, err := s.db.Exec(ctx, `
		INSERT INTO merge_drift_decisions (ticket_id, change_id, decision, reason, drift_dirs, head_sha, base_tip_sha, enforced)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (tenant_id, change_id, head_sha, base_tip_sha) DO NOTHING`,
		d.TicketID, d.ChangeID, d.Decision, d.Reason, dirs, d.HeadSHA, d.TipSHA, d.Enforced); err != nil {
		return fmt.Errorf("change: record drift decision: %w", err)
	}
	return nil
}

func (s *PostgresStore) PendingDriftDecisions(ctx context.Context, changeID int64, headSHA string) (int, error) {
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM merge_drift_decisions WHERE change_id = $1 AND head_sha <> $2 AND ci_passed IS NULL`, changeID, headSHA).Scan(&n); err != nil {
		return 0, fmt.Errorf("change: count drift decisions: %w", err)
	}
	return n, nil
}

func (s *PostgresStore) LinkDriftOutcome(ctx context.Context, changeID int64, headSHA string, passed bool) (int, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE merge_drift_decisions SET ci_head_sha = $2, ci_passed = $3, ci_recorded_at = now()
		 WHERE change_id = $1 AND head_sha <> $2 AND ci_passed IS NULL`, changeID, headSHA, passed)
	if err != nil {
		return 0, fmt.Errorf("change: link drift outcome: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (s *PostgresStore) MarkReady(ctx context.Context, id int64) error {
	if _, err := s.db.Exec(ctx, `UPDATE ticket_changes SET draft = false WHERE id = $1`, id); err != nil {
		return fmt.Errorf("change: mark ready: %w", err)
	}
	return nil
}

func (s *PostgresStore) MarkMerged(ctx context.Context, id int64, by uuid.UUID, sha string) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE ticket_changes SET merged_at = now(), merged_by_agent_id = $2, merge_sha = $3, merging_since = NULL
		 WHERE id = $1 AND merged_at IS NULL AND superseded_at IS NULL`, id, by, sha)
	if err != nil {
		return false, fmt.Errorf("change: mark merged: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// dueExpr is the closing rule over a ticket_changes row aliased c. The attempt
// must have finished itself: a running sibling keeps its change, its CI and
// its agent. The promoted attempt's change is never due. A parked ticket is
// never done, so only a replay's change is due there: nothing else of a parked
// ticket's is an experiment to clean up.
var dueExpr = `((c.superseded_at IS NULL AND c.merged_at IS NULL AND c.closed_at IS NULL
	AND EXISTS (SELECT 1 FROM tickets t WHERE t.tenant_id = c.tenant_id AND t.id = c.ticket_id AND t.status = ` + ticket.SQLStatusDone + `)
	AND EXISTS (SELECT 1 FROM attempts a WHERE a.tenant_id = c.tenant_id AND a.ticket_id = c.ticket_id AND a.number = c.attempt_number
	            AND a.promoted_at IS NULL AND a.status IN ` + ticket.AttemptFinishedStatusesSQL + `))
	OR (c.superseded_at IS NULL AND c.merged_at IS NULL AND c.closed_at IS NULL
	AND EXISTS (SELECT 1 FROM tickets t WHERE t.tenant_id = c.tenant_id AND t.id = c.ticket_id AND t.status = 'parked')
	AND EXISTS (SELECT 1 FROM attempts a WHERE a.tenant_id = c.tenant_id AND a.ticket_id = c.ticket_id AND a.number = c.attempt_number
	            AND a.replay AND a.promoted_at IS NULL AND a.status IN ` + ticket.AttemptFinishedStatusesSQL + `)))`

func (s *PostgresStore) DueClosings(ctx context.Context, ticketID int64) ([]*Record, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+recordColumns+` FROM ticket_changes c
		 WHERE (c.ticket_id = $1 OR $1 = 0) AND `+dueExpr+` ORDER BY c.id`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("change: list due closings: %w", err)
	}
	defer rows.Close()
	var out []*Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("change: list due closings: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *PostgresStore) MarkClosed(ctx context.Context, id int64, reason string) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE ticket_changes SET closed_at = now(), closed_reason = $2
		 WHERE id = $1 AND closed_at IS NULL AND merged_at IS NULL AND superseded_at IS NULL`, id, reason)
	if err != nil {
		return false, fmt.Errorf("change: mark closed: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *PostgresStore) DeletableBases(ctx context.Context, ticketID int64) ([]BaseBranch, error) {
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT b.ticket_id, b.host, b.owner, b.repo, b.base_branch
		  FROM ticket_changes b
		  JOIN tickets t ON t.tenant_id = b.tenant_id AND t.id = b.ticket_id AND t.status = `+ticket.SQLStatusDone+`
		 WHERE (b.ticket_id = $1 OR $1 = 0)
		   AND b.base_branch = 'slop-haus-base-ticket-' || b.ticket_id
		   AND b.base_deleted_at IS NULL
		   AND (b.superseded_at IS NOT NULL OR b.merged_at IS NOT NULL OR b.closed_at IS NOT NULL)
		   AND NOT EXISTS (
		       SELECT 1 FROM ticket_changes c
		        WHERE c.ticket_id = t.id AND c.base_branch = 'slop-haus-base-ticket-' || t.id
		          AND c.superseded_at IS NULL AND c.merged_at IS NULL AND c.closed_at IS NULL)
		   AND NOT EXISTS (
		       SELECT 1 FROM attempts a WHERE a.tenant_id = t.tenant_id AND a.ticket_id = t.id
		          AND a.promoted_at IS NULL AND a.status IN `+ticket.AttemptRunningStatusesSQL+`)
		 ORDER BY 1`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("change: list deletable bases: %w", err)
	}
	defer rows.Close()
	var out []BaseBranch
	for rows.Next() {
		var b BaseBranch
		if err := rows.Scan(&b.TicketID, &b.Host, &b.Repo.Owner, &b.Repo.Name, &b.Name); err != nil {
			return nil, fmt.Errorf("change: list deletable bases: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *PostgresStore) MarkBaseDeleted(ctx context.Context, b BaseBranch) error {
	if _, err := s.db.Exec(ctx, `
		UPDATE ticket_changes SET base_deleted_at = now()
		 WHERE ticket_id = $1 AND base_branch = $2 AND host = $3 AND owner = $4 AND repo = $5 AND base_deleted_at IS NULL`,
		b.TicketID, b.Name, b.Host, b.Repo.Owner, b.Repo.Name); err != nil {
		return fmt.Errorf("change: mark base deleted: %w", err)
	}
	return nil
}

// Tenants lists every tenant, for the reconciler's pass over all of them.
func (s *PostgresStore) Tenants(ctx context.Context) ([]uuid.UUID, error) {
	return tenantstate.ActiveIDs(ctx, s.db.Global())
}
