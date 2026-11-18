package attemptsignals

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Store keeps one row of signals per attempt. Row-level security scopes every
// read and write to the caller's tenant.
type Store struct {
	db *db.DB
}

func NewStore(database *db.DB) *Store { return &Store{db: database} }

// Get reads the stored signals; attempts with none are absent.
func (s *Store) Get(ctx context.Context, refs []ticket.AttemptRef) (map[ticket.AttemptRef]*ticket.AttemptSignalsBody, error) {
	tickets := make([]int64, len(refs))
	numbers := make([]int32, len(refs))
	for i, r := range refs {
		tickets[i], numbers[i] = r.TicketID, int32(r.Number)
	}
	out := map[ticket.AttemptRef]*ticket.AttemptSignalsBody{}
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, `
			SELECT ticket_id, attempt_number, detail FROM attempt_signals
			 WHERE (ticket_id, attempt_number) IN (SELECT * FROM unnest($1::bigint[], $2::int[]))`, tickets, numbers)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ref ticket.AttemptRef
			var number int32
			var raw []byte
			if err := rows.Scan(&ref.TicketID, &number, &raw); err != nil {
				return err
			}
			ref.Number = int(number)
			var body ticket.AttemptSignalsBody
			if err := json.Unmarshal(raw, &body); err != nil {
				return fmt.Errorf("detail of %d/%d: %w", ref.TicketID, ref.Number, err)
			}
			out[ref] = &body
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("attemptsignals: read: %w", err)
	}
	return out, nil
}

// Put writes the attempt's signals. A final row is never rewritten: the
// figures of a closed attempt must not move, whatever recomputes them later.
// Nor is a row replaced by a computation that started before the stored one
// did, so overlapping refreshes cannot roll the evidence back.
func (s *Store) Put(ctx context.Context, ref ticket.AttemptRef, c *Computed) error {
	detail, err := json.Marshal(c.Body)
	if err != nil {
		return err
	}
	u := c.Usage
	_, err = s.db.Exec(ctx, `
		INSERT INTO attempt_signals (ticket_id, attempt_number, final, computed_at, head_sha,
			tests_deleted, skips_added, assertions_removed, out_of_scope_files, lines_added, lines_removed, files_touched,
			checks_passed, reruns, regressions, own_test_failures, own_tests_flagged,
			runs, turns, wall_clock_seconds, discussions, scope_extension_denials, blocked_handbacks, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24)
		ON CONFLICT (tenant_id, ticket_id, attempt_number) DO UPDATE SET
			final = EXCLUDED.final, computed_at = EXCLUDED.computed_at, head_sha = EXCLUDED.head_sha,
			tests_deleted = EXCLUDED.tests_deleted, skips_added = EXCLUDED.skips_added,
			assertions_removed = EXCLUDED.assertions_removed, out_of_scope_files = EXCLUDED.out_of_scope_files,
			lines_added = EXCLUDED.lines_added, lines_removed = EXCLUDED.lines_removed, files_touched = EXCLUDED.files_touched,
			checks_passed = EXCLUDED.checks_passed, reruns = EXCLUDED.reruns, regressions = EXCLUDED.regressions,
			own_test_failures = EXCLUDED.own_test_failures, own_tests_flagged = EXCLUDED.own_tests_flagged,
			runs = EXCLUDED.runs, turns = EXCLUDED.turns, wall_clock_seconds = EXCLUDED.wall_clock_seconds,
			discussions = EXCLUDED.discussions, scope_extension_denials = EXCLUDED.scope_extension_denials,
			blocked_handbacks = EXCLUDED.blocked_handbacks, detail = EXCLUDED.detail
		WHERE NOT attempt_signals.final AND attempt_signals.computed_at <= EXCLUDED.computed_at`,
		ref.TicketID, ref.Number, c.Body.Final, c.Body.ComputedAt, c.Body.HeadSHA,
		c.TestsDeleted, c.SkipsAdded, c.AssertionsRemoved, c.OutOfScopeFiles, c.LinesAdded, c.LinesRemoved, c.FilesTouched,
		c.ChecksPassed, c.Reruns, c.Regressions, c.OwnTestFailures, c.OwnTestsFlagged,
		u.Runs, u.Turns, c.Body.Time.WallClockSeconds, u.Discussions, u.ScopeExtensionDenials, u.BlockedHandbacks, detail)
	if err != nil {
		return fmt.Errorf("attemptsignals: write: %w", err)
	}
	return nil
}
