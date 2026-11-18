package reviewreport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/lowaa/slop-factory/slop-review/report"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Store is the persistence Service needs. Every call runs in the context's
// tenant; row level security hides every other tenant's rows.
type Store interface {
	// InsertRun stores a validated run as poster's, attributed to the attempt
	// its branch names if that attempt is held now by the poster (an agent)
	// or by an agent the poster runs (a supervisor). A run already stored as
	// poster's is left as it is (Created false); ErrRunTaken if it is
	// another poster's, ErrFindingTaken if one of its finding ids is another
	// run's.
	InsertRun(ctx context.Context, poster *Poster, run *report.Run) (*Inserted, error)
	// LabelFinding appends agentID's label to a finding whose run's attempt
	// agentID holds now, or whose run agentID posted; ErrNoFinding for any
	// other finding.
	LabelFinding(ctx context.Context, agentID, findingID uuid.UUID, l *Label) error
	// AttemptRuns is the attempt's newest runs, at most limit, each with its
	// findings and their current labels; ticket.ErrNoSuchAttempt for an
	// attempt the tenant does not have.
	AttemptRuns(ctx context.Context, ref ticket.AttemptRef, limit int) ([]AttemptRun, error)
	// RequestReview records agentID's request for a review of headSHA of the
	// attempt it holds, replacing the attempt's last request.
	// ErrNoHeldAttempt when it holds none, ErrNotSupervised when no
	// supervisor runs it.
	RequestReview(ctx context.Context, agentID uuid.UUID, headSHA, intent string) (*Request, error)
	// OpenRequest is agentID's request on the attempt while it is open; nil
	// when there is none, or it has closed.
	OpenRequest(ctx context.Context, agentID uuid.UUID, ticketID int64, attempt int) (*Request, error)
	// HeldAttemptRuns are the runs, at most limit, of the attempt agentID
	// holds at head, or at the head it last requested a review of when head
	// is empty; Truncated when there were more. ErrNoHeldAttempt when it
	// holds none, ErrNoRequest when head is empty and it never requested one.
	HeldAttemptRuns(ctx context.Context, agentID uuid.UUID, head string, limit int) (*HeadReview, error)
	HeadRunReader
}

type PostgresStore struct {
	db *db.DB
}

func NewPostgresStore(database *db.DB) *PostgresStore {
	return &PostgresStore{db: database}
}

var _ Store = (*PostgresStore)(nil)

const (
	statusCompleted = "completed"
	statusFailed    = "failed"
)

// minHeadPrefix is the shortest abbreviated sha a head matches by.
const minHeadPrefix = 7

// currentLabel joins each finding f to its newest label as l.
const currentLabel = `LEFT JOIN LATERAL (
	SELECT lb.verdict, lb.reason, lb.fixed_in, lb.labelled_at FROM review_finding_labels lb
	 WHERE lb.tenant_id = f.tenant_id AND lb.finding_id = f.id
	 ORDER BY lb.id DESC LIMIT 1) l ON true`

// findingRow is one finding as jsonb_to_recordset reads it: findings go in as
// one JSON document, since a finding's personas are an array and unnest would
// flatten an array of them.
type findingRow struct {
	ID       uuid.UUID `json:"id"`
	Position int       `json:"position"`
	File     string    `json:"file"`
	Line     int       `json:"line"`
	Severity string    `json:"severity"`
	Summary  string    `json:"summary"`
	Detail   string    `json:"detail"`
	Personas []string  `json:"personas"`
}

func (s *PostgresStore) InsertRun(ctx context.Context, poster *Poster, run *report.Run) (*Inserted, error) {
	// head_author names a person; the server has no use for it, so it is
	// never stored (the table refuses a report that has it).
	stored := *run
	stored.HeadAuthor = ""
	raw, err := json.Marshal(&stored)
	if err != nil {
		return nil, fmt.Errorf("encode run: %w", err)
	}
	rows := make([]findingRow, len(run.Findings))
	for i := range run.Findings {
		f := &run.Findings[i]
		rows[i] = findingRow{ID: f.ID, Position: i, File: f.File, Line: f.Line, Severity: f.Severity,
			Summary: f.Summary, Detail: f.Detail, Personas: nonNil(f.Personas)}
	}
	findings, err := json.Marshal(rows)
	if err != nil {
		return nil, fmt.Errorf("encode findings: %w", err)
	}
	status := statusCompleted
	if run.Status == report.StatusFailed {
		status = statusFailed
	}

	var out *Inserted
	err = s.db.InTx(ctx, func(q db.Querier) error {
		attribution, err := attribute(ctx, q, poster, run.Branch)
		if err != nil {
			return err
		}
		var ticketID *int64
		var attempt *int
		if attribution != nil {
			ticketID, attempt = &attribution.TicketID, &attribution.Attempt
		}
		tag, err := q.Exec(ctx, `
			INSERT INTO review_runs (id, posted_by_kind, posted_by_id, ticket_id, attempt_number, kind, status, branch, head_sha,
				personas, verdict, incomplete, provider, model, started_at, finished_at, report)
			VALUES ($1, $17, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16::jsonb)
			ON CONFLICT (tenant_id, id) DO NOTHING`,
			run.ID, poster.ID, ticketID, attempt, string(run.Kind), status, run.Branch, run.HeadSHA, nonNil(run.Personas),
			run.Verdict, run.Incomplete, run.Provider, run.Model, run.StartedAt, run.FinishedAt, string(raw), string(poster.Kind))
		if err != nil {
			return fmt.Errorf("store review run: %w", err)
		}
		if tag.RowsAffected() == 0 {
			out, err = existingRun(ctx, q, poster, run.ID)
			return err
		}
		if len(rows) > 0 {
			if _, err := q.Exec(ctx, `
				INSERT INTO review_findings (id, run_id, position, file, line, severity, summary, detail, personas)
				SELECT f.id, $1, f.position, f.file, f.line, f.severity, f.summary, f.detail, f.personas
				  FROM jsonb_to_recordset($2::jsonb) AS f(id uuid, position integer, file text, line integer,
				       severity text, summary text, detail text, personas text[])`, run.ID, string(findings)); err != nil {
				if isUniqueViolation(err) {
					return ErrFindingTaken
				}
				return fmt.Errorf("store review findings: %w", err)
			}
		}
		out = &Inserted{Created: true, Attribution: attribution}
		return nil
	})
	if err != nil {
		// Text Postgres cannot hold (a NUL) is the report's fault, and
		// answering it as a server error would have the client retry it
		// forever.
		if isUnstorableText(err) {
			return nil, fmt.Errorf("%w: the report holds text that cannot be stored (a NUL character or invalid encoding)", ErrInvalid)
		}
		return nil, err
	}
	return out, nil
}

// attribute is the attempt branch names, if it is held now by the posting
// agent or by an agent the posting supervisor runs. Anything else -- a lease
// on another attempt of the ticket, another's lease, none -- leaves the run
// unattributed.
func attribute(ctx context.Context, q db.Querier, poster *Poster, branch string) (*Attribution, error) {
	ticketID, attempt, ok := ticket.ParseBranchName(branch)
	if !ok {
		return nil, nil
	}
	var held bool
	var err error
	switch poster.Kind {
	case PostedByAgent:
		held, err = agentHolds(ctx, q, poster.ID, ticketID, attempt)
	case PostedBySupervisor:
		held, err = supervisedAgentHolds(ctx, q, poster.ID, ticketID, attempt)
	default:
		return nil, fmt.Errorf("attribute review run: unknown poster kind %q", poster.Kind)
	}
	if err != nil || !held {
		return nil, err
	}
	return &Attribution{TicketID: ticketID, Attempt: attempt}, nil
}

func agentHolds(ctx context.Context, q db.Querier, agentID uuid.UUID, ticketID int64, attempt int) (bool, error) {
	ref, err := ticket.HeldAttemptOf(ctx, q, agentID, ticketID)
	if errors.Is(err, ticket.ErrNoLease) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("attribute review run: %w", err)
	}
	return ref.Number == attempt, nil
}

// supervisedAgentHolds is whether one of the supervisor's own slots holds the
// attempt: a worker whose supervisor_agent_id is the supervisor, which the
// schema never lets change once registered.
func supervisedAgentHolds(ctx context.Context, q db.Querier, supervisorID uuid.UUID, ticketID int64, attempt int) (bool, error) {
	var held bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM leases l
			  JOIN agents a ON a.tenant_id = l.tenant_id AND a.id = l.agent_id
			 WHERE l.ticket_id = $1 AND l.attempt_number = $2 AND a.supervisor_agent_id = $3
			   AND `+ticket.ActiveLeaseClause+`)`, ticketID, attempt, supervisorID).Scan(&held)
	if err != nil {
		return false, fmt.Errorf("attribute review run: %w", err)
	}
	return held, nil
}

// existingRun answers a re-post of a stored run.
func existingRun(ctx context.Context, q db.Querier, poster *Poster, runID uuid.UUID) (*Inserted, error) {
	var stored Poster
	var ticketID *int64
	var attempt *int
	if err := q.QueryRow(ctx, `SELECT posted_by_kind, posted_by_id, ticket_id, attempt_number FROM review_runs WHERE id = $1`, runID).
		Scan(&stored.Kind, &stored.ID, &ticketID, &attempt); err != nil {
		return nil, fmt.Errorf("read stored review run: %w", err)
	}
	if stored != *poster {
		return nil, ErrRunTaken
	}
	out := &Inserted{}
	if ticketID != nil && attempt != nil {
		out.Attribution = &Attribution{TicketID: *ticketID, Attempt: *attempt}
	}
	return out, nil
}

func (s *PostgresStore) LabelFinding(ctx context.Context, agentID, findingID uuid.UUID, l *Label) error {
	var fixedIn *string
	if l.FixedIn != "" {
		fixedIn = &l.FixedIn
	}
	// The finding is found through who may label it, so a finding the agent
	// may not label is no different from one that does not exist.
	tag, err := s.db.Exec(ctx, `
		INSERT INTO review_finding_labels (finding_id, agent_id, verdict, reason, fixed_in)
		SELECT f.id, $2, $3, $4, $5 FROM review_findings f
		  JOIN review_runs r ON r.tenant_id = f.tenant_id AND r.id = f.run_id
		 WHERE f.id = $1
		   AND ((r.posted_by_kind = 'agent' AND r.posted_by_id = $2)
		        OR EXISTS (SELECT 1 FROM leases l
		                    WHERE l.tenant_id = r.tenant_id AND l.ticket_id = r.ticket_id
		                      AND l.attempt_number = r.attempt_number AND l.agent_id = $2
		                      AND `+ticket.ActiveLeaseClause+`))`, findingID, agentID, l.Verdict, l.Reason, fixedIn)
	if err != nil {
		if isUnstorableText(err) {
			return fmt.Errorf("%w: the label holds text that cannot be stored (a NUL character or invalid encoding)", ErrInvalid)
		}
		return fmt.Errorf("label review finding: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNoFinding
	}
	return nil
}

func (s *PostgresStore) AttemptRuns(ctx context.Context, ref ticket.AttemptRef, limit int) ([]AttemptRun, error) {
	var runs []AttemptRun
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		var exists bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM attempts WHERE ticket_id = $1 AND number = $2)`,
			ref.TicketID, ref.Number).Scan(&exists); err != nil {
			return fmt.Errorf("find attempt: %w", err)
		}
		if !exists {
			return fmt.Errorf("%s: %w", &ref, ticket.ErrNoSuchAttempt)
		}
		found, err := listRuns(ctx, q, ref.TicketID, ref.Number, "", limit)
		runs = found
		return err
	})
	if err != nil {
		return nil, err
	}
	return runs, nil
}

// listRuns is the attempt's newest runs, at most limit, at head when it is
// not empty (as headMatches matches it), each with its findings.
func listRuns(ctx context.Context, q db.Querier, ticketID int64, attempt int, head string, limit int) ([]AttemptRun, error) {
	rows, err := q.Query(ctx, `
		SELECT r.id, r.posted_by_kind, r.posted_by_id, a.name, r.kind, r.status,
		       COALESCE(r.report->'failure'->>'class', ''), COALESCE(r.report->'failure'->>'message', ''),
		       r.branch, r.head_sha, r.personas, r.verdict, COALESCE(r.report->>'summary', ''), r.incomplete,
		       r.provider, r.model, r.started_at, r.finished_at, r.received_at
		  FROM review_runs r
		  JOIN agents a ON a.tenant_id = r.tenant_id AND a.id = r.posted_by_id
		 WHERE r.ticket_id = $1 AND r.attempt_number = $2
		   AND ($4 = '' OR `+headMatches("r.head_sha", "$4")+`)
		 ORDER BY r.finished_at DESC, r.received_at DESC, r.id
		 LIMIT $3`, ticketID, attempt, limit, head)
	if err != nil {
		return nil, fmt.Errorf("list review runs: %w", err)
	}
	runs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AttemptRun, error) {
		var r AttemptRun
		err := row.Scan(&r.ID, &r.Poster.Kind, &r.Poster.ID, &r.PosterName, &r.Kind, &r.Status, &r.FailureClass, &r.FailureMessage,
			&r.Branch, &r.HeadSHA, &r.Personas, &r.Verdict, &r.Summary, &r.Incomplete,
			&r.Provider, &r.Model, &r.StartedAt, &r.FinishedAt, &r.ReceivedAt)
		return r, err
	})
	if err != nil {
		return nil, fmt.Errorf("list review runs: %w", err)
	}
	if err := attachFindings(ctx, q, runs); err != nil {
		return nil, err
	}
	return runs, nil
}

// headMatches is true when the sha in column col and the one in arg (SQL
// expressions) name the same commit: the full sha, or a prefix of at least
// minHeadPrefix characters either way round, ignoring case.
func headMatches(col, arg string) string {
	return fmt.Sprintf(`(length(%[1]s) >= %[3]d AND length(%[2]s) >= %[3]d
		AND (starts_with(lower(%[1]s), lower(%[2]s)) OR starts_with(lower(%[2]s), lower(%[1]s))))`, col, arg, minHeadPrefix)
}

func attachFindings(ctx context.Context, q db.Querier, runs []AttemptRun) error {
	if len(runs) == 0 {
		return nil
	}
	byID := make(map[uuid.UUID]*AttemptRun, len(runs))
	ids := make([]uuid.UUID, len(runs))
	for i := range runs {
		byID[runs[i].ID] = &runs[i]
		ids[i] = runs[i].ID
	}
	rows, err := q.Query(ctx, `
		SELECT f.run_id, f.id, f.file, f.line, f.severity, f.summary, f.detail, f.personas,
		       l.verdict, l.reason, l.fixed_in, l.labelled_at
		  FROM review_findings f `+currentLabel+`
		 WHERE f.run_id = ANY($1)
		 ORDER BY f.run_id, f.position`, ids)
	if err != nil {
		return fmt.Errorf("list review findings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var runID uuid.UUID
		var f AttemptFinding
		var verdict, reason, fixedIn *string
		var labelledAt *time.Time
		if err := rows.Scan(&runID, &f.ID, &f.File, &f.Line, &f.Severity, &f.Summary, &f.Detail, &f.Personas,
			&verdict, &reason, &fixedIn, &labelledAt); err != nil {
			return fmt.Errorf("scan review finding: %w", err)
		}
		if verdict != nil {
			f.Label = &CurrentLabel{Label: Label{Verdict: *verdict, Reason: deref(reason), FixedIn: deref(fixedIn)}}
			if labelledAt != nil {
				f.Label.LabelledAt = *labelledAt
			}
		}
		r := byID[runID]
		r.Findings = append(r.Findings, f)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list review findings: %w", err)
	}
	return nil
}

func (s *PostgresStore) RunsForAttemptHead(ctx context.Context, ticketID int64, attempt int, headSHA string, postedBy PostedBy) ([]HeadRun, error) {
	if len(headSHA) < minHeadPrefix {
		return nil, nil
	}
	var runs []HeadRun
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, `
			SELECT r.id, r.posted_by_kind, r.posted_by_id, r.head_sha, r.personas, r.verdict, r.incomplete, r.finished_at
			  FROM review_runs r
			 WHERE r.ticket_id = $1 AND r.attempt_number = $2 AND r.kind = 'review' AND r.status = 'completed'
			   AND `+headMatches("r.head_sha", "$3")+`
			   AND (NOT $4::boolean OR r.posted_by_kind = 'supervisor')
			 ORDER BY r.finished_at DESC, r.received_at DESC, r.id`,
			ticketID, attempt, headSHA, postedBy == PostedBySupervisorOnly)
		if err != nil {
			return fmt.Errorf("list head review runs: %w", err)
		}
		runs, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (HeadRun, error) {
			var r HeadRun
			err := row.Scan(&r.RunID, &r.Poster.Kind, &r.Poster.ID, &r.HeadSHA, &r.Personas, &r.Verdict, &r.Incomplete, &r.FinishedAt)
			return r, err
		})
		if err != nil {
			return fmt.Errorf("list head review runs: %w", err)
		}
		return countBlocking(ctx, q, runs)
	})
	if err != nil {
		return nil, err
	}
	return runs, nil
}

func countBlocking(ctx context.Context, q db.Querier, runs []HeadRun) error {
	if len(runs) == 0 {
		return nil
	}
	byID := make(map[uuid.UUID]*HeadRun, len(runs))
	ids := make([]uuid.UUID, len(runs))
	for i := range runs {
		byID[runs[i].RunID] = &runs[i]
		ids[i] = runs[i].RunID
	}
	rows, err := q.Query(ctx, `
		SELECT f.run_id, count(*)
		  FROM review_findings f
		 WHERE f.run_id = ANY($1) AND f.severity IN ('blocker', 'major')
		 GROUP BY f.run_id`, ids)
	if err != nil {
		return fmt.Errorf("count blocking findings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var runID uuid.UUID
		var n int
		if err := rows.Scan(&runID, &n); err != nil {
			return fmt.Errorf("count blocking findings: %w", err)
		}
		byID[runID].Blocking = n
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("count blocking findings: %w", err)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// isUnstorableText is Postgres refusing text it cannot hold: a NUL or bytes
// that are not UTF-8 in text (22021), or a NUL in a jsonb string (22P05).
func isUnstorableText(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "22021" || pgErr.Code == "22P05")
}

// nonNil keeps a nil slice from being stored as NULL in a NOT NULL array.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
