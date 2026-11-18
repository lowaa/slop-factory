package seeddata

import (
	"fmt"
	"strings"
	"time"
)

// movedFields are the columns that moved from tickets to attempts.
var movedFields = []string{
	"resume_agent_id", "affinity_until", "pr_url", "approved_at", "approved_by_user_id",
	"changes_requested_at", "review_started_at",
}

// moveToFirstAttempt writes the moved fields a ticket's values describe onto
// the ticket's attempt 1, which the ticket's insert made.
func (s *seed) moveToFirstAttempt(ticket row, values map[string]any) {
	var sets []string
	var args []any
	for _, name := range movedFields {
		if v, ok := values[name]; ok {
			args = append(args, v)
			sets = append(sets, fmt.Sprintf("%s = $%d", name, len(args)))
		}
	}
	if len(sets) == 0 {
		return
	}
	args = append(args, ticket["id"])
	if _, err := s.q.Exec(s.ctx, fmt.Sprintf(`UPDATE attempts SET %s WHERE ticket_id = $%d AND number = 1`,
		strings.Join(sets, ", "), len(args)), args...); err != nil {
		panic(seedError{fmt.Errorf("seed the first attempt of ticket %v at version %d: %w", ticket["id"], s.version, err)})
	}
}

// recordFirstAttemptSession records a run's session on the ticket's attempt 1,
// so the pair of columns is one a migration is tested on.
func (s *seed) recordFirstAttemptSession(ticket row) {
	if _, err := s.q.Exec(s.ctx, `UPDATE attempts SET harness = 'claude', harness_session_id = $1 WHERE ticket_id = $2 AND number = 1`,
		s.tag("session"), ticket["id"]); err != nil {
		panic(seedError{fmt.Errorf("seed the session of ticket %v at version %d: %w", ticket["id"], s.version, err)})
	}
}

// promoteFirstAttempt records a person promoting the ticket's attempt 1, so
// the person column is one a migration is tested on.
func (s *seed) promoteFirstAttempt(ticket row, by any) {
	if _, err := s.q.Exec(s.ctx, `UPDATE attempts SET promoted_at = $1, promoted_by_user_id = $2 WHERE ticket_id = $3 AND number = 1`,
		s.at(5*time.Hour), by, ticket["id"]); err != nil {
		panic(seedError{fmt.Errorf("seed the promotion of ticket %v at version %d: %w", ticket["id"], s.version, err)})
	}
}

// Seeders is one per table the migrations have created since the baseline,
// dropped ones included: the walk applies them at every version the table
// exists at. Each
// describes its rows against the latest schema; the seed helper leaves out
// whichever columns an older version lacks. A table that gains a column the
// seeder does not name gets the column's default, so a seeder only needs
// touching when a column that is NOT NULL without a default appears, or one
// worth varying -- a nullable column left NULL is a column no backfill is
// tested on.
//
// Each domain file registers its tables from an init, so a feature edits its
// own file rather than one shared literal.
var Seeders = Registry{}

func register(r Registry) {
	for table, sd := range r {
		if _, dup := Seeders[table]; dup {
			panic("seeddata: two seeders for table " + table)
		}
		Seeders[table] = sd
	}
}
