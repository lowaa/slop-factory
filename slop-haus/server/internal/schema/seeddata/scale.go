package seeddata

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Scale sizes the data SeedScale writes. The mix is fixed -- a share of
// every status, a few comments on most tickets, a handoff on one in ten --
// so a budget measured on one size says something about another; only the
// ticket count moves.
type Scale struct {
	Tickets int
}

// DefaultScale is the size the lists-at-scale guard runs at: a few times
// the largest tenant seen, so a list that walks everything shows.
var DefaultScale = Scale{Tickets: 5000}

// ScaleSummary is what SeedScale wrote, for a test to size its expectations
// by instead of hard-coding the mix.
type ScaleSummary struct {
	Tickets        int
	ByStatus       map[string]int
	Comments       int
	Handoffs       int
	Blockers       int
	Proposals      int
	UsageRows      int
	AwaitingReview int
	// SearchWord is a word found in about one ticket in a hundred, and
	// CommonWord one found in most: the rare and the broad search.
	SearchWord string
	CommonWord string
}

// The status of ticket n is statusMix[n mod 100]. Done and cancelled are
// most of a long-lived board, which is why they are the case to get right.
var statusMix = []struct {
	status string
	share  int
}{
	{"done", 40}, {"cancelled", 8}, {"open", 8}, {"in_progress", 5}, {"in_merge_queue", 4}, {"blocked", 8},
	{"discussion", 4}, {"review", 6}, {"preparing", 10}, {"parked", 7},
}

const (
	scaleSearchWord = "zebrafish"
	scaleCommonWord = "importer"
	scaleAgents     = 20
	scaleConsoles   = 3
)

// SeedScale fills a tenant with sc.Tickets tickets across every status, with
// comments, handoffs, blocking links, proposals and LLM usage, in a handful
// of set-based statements: row-at-a-time through the service would take
// minutes at this size, and what is measured afterwards is the reads.
//
// It writes the tables directly, so it leaves out the side effects of the
// service (status history, board revisions): the lists under test read the
// rows, not the history. The tenant must exist (global_tenants). Run it
// into an empty tenant: ids come from the tenant's sequence, and the
// blocking links and proposals are placed by position.
func SeedScale(ctx context.Context, app *db.DB, tenant uuid.UUID, sc Scale) (*ScaleSummary, error) {
	if sc.Tickets < 100 {
		return nil, fmt.Errorf("seed scale: %d tickets is below the 100 the mix is built from", sc.Tickets)
	}
	ctx = db.WithTenant(ctx, tenant)
	sum := &ScaleSummary{Tickets: sc.Tickets, ByStatus: map[string]int{}, SearchWord: scaleSearchWord, CommonWord: scaleCommonWord}

	err := app.InTx(ctx, func(q db.Querier) error {
		// Seeding one tenant twice at once would give each call's children to
		// both calls' tickets; the second waits here and then finds tickets.
		if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "seed-scale-"+tenant.String()); err != nil {
			return fmt.Errorf("seed scale: lock: %w", err)
		}
		var existing int
		if err := q.QueryRow(ctx, `SELECT count(*) FROM tickets`).Scan(&existing); err != nil {
			return fmt.Errorf("seed scale: %w", err)
		}
		if existing > 0 {
			return fmt.Errorf("seed scale: the tenant already has %d tickets; seed an empty one", existing)
		}

		// The person who reviewed the done tickets that have been.
		reviewer := uuid.New()
		if _, err := q.Exec(ctx,
			`INSERT INTO global_users (id, email, password_hash) VALUES ($1, $2, 'unused')`,
			reviewer, "scale-reviewer-"+reviewer.String()+"@example.invalid"); err != nil {
			return fmt.Errorf("seed scale: reviewer: %w", err)
		}

		exec := func(label, sql string, args ...any) (int, error) {
			tag, err := q.Exec(ctx, sql, args...)
			if err != nil {
				return 0, fmt.Errorf("seed scale: %s: %w", label, err)
			}
			return int(tag.RowsAffected()), nil
		}

		if _, err := exec("agents", `
			INSERT INTO agents (id, name, kind)
			SELECT gen_random_uuid(), 'scale-agent-' || n, CASE WHEN n > $1::int THEN 'console' ELSE 'worker' END FROM generate_series(1, $1::int + $2::int) n`,
			scaleAgents, scaleConsoles); err != nil {
			return err
		}

		// A CASE over n mod 100 is the status mix.
		statusCase := "CASE"
		upto := 0
		for _, m := range statusMix {
			upto += m.share
			statusCase += fmt.Sprintf(" WHEN n %% 100 < %d THEN '%s'", upto, m.status)
		}
		statusCase += " END"

		if _, err := exec("tickets", fmt.Sprintf(`
			INSERT INTO tickets (title, body, priority, status, created_at, updated_at, closed_at,
			                     requires_approval, post_reviewed_at, post_reviewed_by_user_id)
			SELECT 'Scale ticket ' || n || CASE WHEN n %% 100 = 7 THEN ' %[2]s' ELSE '' END,
			       'Body of scale ticket ' || n || ': the %[3]s needs a fix. ' || repeat('Some detail about the work. ', 8),
			       (ARRAY['low','normal','normal','high','urgent'])[1 + n %% 5],
			       status,
			       now() - make_interval(mins => $1 - n),
			       now() - make_interval(mins => $1 - n),
			       CASE WHEN status IN ('done','cancelled') THEN now() - make_interval(mins => $1 - n) END,
			       status = 'review' OR (status = 'done' AND n %% 3 = 0),
			       CASE WHEN status = 'done' AND n %% 3 <> 0 AND n %% 4 = 0 THEN now() - make_interval(mins => $1 - n) END,
			       CASE WHEN status = 'done' AND n %% 3 <> 0 AND n %% 4 = 0 THEN $2::uuid END
			  FROM (SELECT n, %[1]s AS status FROM generate_series(1, $1) n) s
			 ORDER BY n`, statusCase, scaleSearchWord, scaleCommonWord), sc.Tickets, reviewer); err != nil {
			return err
		}
		// The fields that moved to attempts go on the ticket's attempt 1, which
		// the insert above made; n is the ticket's place in the order it was
		// numbered in.
		if _, err := exec("attempts", `
			UPDATE attempts a
			   SET pr_url = CASE WHEN s.status IN ('review','done','in_merge_queue') THEN 'https://github.com/example/repo/pull/' || s.n END,
			       approved_at = CASE WHEN s.status = 'done' AND s.n % 3 = 0 THEN s.created_at END,
			       review_started_at = CASE WHEN s.status = 'review' THEN s.created_at END
			  FROM (SELECT id, status, created_at, row_number() OVER (ORDER BY id) AS n FROM tickets) s
			 WHERE a.ticket_id = s.id AND a.number = 1`); err != nil {
			return err
		}
		// A ticket in the merge queue has its row in the landing queue.
		if _, err := exec("landings", `
			INSERT INTO landings (ticket_id, attempt_number, change_url, head_sha)
			SELECT t.id, 1, a.pr_url, repeat('a', 40)
			  FROM tickets t JOIN attempts a ON a.ticket_id = t.id AND a.number = 1
			 WHERE t.status = 'in_merge_queue'
			 ORDER BY t.id`); err != nil {
			return err
		}
		return seedScaleChildren(ctx, q, sum)
	})
	if err != nil {
		return nil, err
	}
	return sum, nil
}
