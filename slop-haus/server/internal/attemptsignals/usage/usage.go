// Package usage measures what an attempt cost and how much hand-holding it
// needed, from records the server already keeps. It is read-only.
package usage

import (
	"context"
	"fmt"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticketusage"
)

// Signals is one attempt's figures. A nil field is unknown, which is not
// zero: an attempt that never ran has no wall-clock, and one with no usage
// reports has no known cost.
//
// Not measured, because nothing durable records them: when the change was
// last pushed (a push reaches the server only as a webhook that is not
// stored), CI re-runs, and scope extensions that were granted (only refusals
// are kept).
type Signals struct {
	// Cost is what the attempt's workers spent, a figure per billing mode;
	// the modes are never summed. Empty when no usage was reported.
	Cost []ticket.CostFigure
	// Runs is the number of leases the attempt was worked under.
	Runs *int64
	// Turns is the model requests made across the attempt's runs.
	Turns *int64

	FirstClaimAt *time.Time
	// LastRunEndedAt is when the last lease ended; nil while one is live.
	LastRunEndedAt *time.Time
	// WallClock is first claim to the end of the last run.
	WallClock *time.Duration

	// The autonomy counts are nil for an attempt that never ran.
	Discussions           *int64
	ScopeExtensionDenials *int64
	BlockedHandbacks      *int64
}

type Store struct {
	db    *db.DB
	costs *ticketusage.PostgresStore
}

func NewStore(database *db.DB) *Store {
	return &Store{db: database, costs: ticketusage.NewPostgresStore(database)}
}

// A lease ends when released, or when it expired if nobody released it yet
// or the sweep that did came late: either way the worker stopped at
// expires_at at the latest. NULL while the lease is still live.
const leaseEnd = `CASE WHEN released_at IS NOT NULL THEN LEAST(released_at, expires_at)
                        WHEN expires_at < now() THEN expires_at END`

// Leases, one row per attempt. The windows of an attempt's leases attribute
// the records that carry only a ticket (scope denials, status changes).
const queryRuns = `
SELECT ticket_id, attempt_number, count(*), min(acquired_at),
       CASE WHEN bool_or(` + leaseEnd + ` IS NULL) THEN NULL ELSE max(` + leaseEnd + `) END
  FROM leases
 WHERE (ticket_id, attempt_number) IN (SELECT * FROM unnest($1::bigint[], $2::int[]))
 GROUP BY ticket_id, attempt_number`

const queryDiscussions = `
SELECT ticket_id, attempt_number, count(*)
  FROM attempt_status_changes
 WHERE (ticket_id, attempt_number) IN (SELECT * FROM unnest($1::bigint[], $2::int[]))
   AND to_status = 'discussion' AND actor_kind = 'agent'
 GROUP BY ticket_id, attempt_number`

// A release with blocked_on leaves the ticket blocked, by the releasing
// agent, inside its lease.
const queryBlocked = `
SELECT l.ticket_id, l.attempt_number, count(DISTINCT c.id)
  FROM leases l
  JOIN ticket_status_changes c ON c.tenant_id = l.tenant_id AND c.ticket_id = l.ticket_id
       AND c.to_status = 'blocked' AND c.actor_kind = 'agent' AND c.actor_id = l.agent_id
       AND c.changed_at BETWEEN l.acquired_at AND LEAST(COALESCE(l.released_at, l.expires_at), l.expires_at)
 WHERE (l.ticket_id, l.attempt_number) IN (SELECT * FROM unnest($1::bigint[], $2::int[]))
 GROUP BY l.ticket_id, l.attempt_number`

const queryDenials = `
SELECT l.ticket_id, l.attempt_number, count(DISTINCT d.id)
  FROM leases l
  JOIN scope_extension_denials d ON d.tenant_id = l.tenant_id AND d.ticket_id = l.ticket_id
       AND d.agent_id = l.agent_id
       AND d.denied_at BETWEEN l.acquired_at AND LEAST(COALESCE(l.released_at, l.expires_at), l.expires_at)
 WHERE (l.ticket_id, l.attempt_number) IN (SELECT * FROM unnest($1::bigint[], $2::int[]))
 GROUP BY l.ticket_id, l.attempt_number`

// Signals reads the attempts' figures. Refs the caller's tenant has nothing
// for are absent from the result; row-level security scopes every read.
func (s *Store) Signals(ctx context.Context, refs []ticket.AttemptRef) (map[ticket.AttemptRef]*Signals, error) {
	costs, err := s.costs.AttemptCosts(ctx, refs)
	if err != nil {
		return nil, err
	}
	tickets := make([]int64, len(refs))
	numbers := make([]int32, len(refs))
	for i, r := range refs {
		tickets[i], numbers[i] = r.TicketID, int32(r.Number)
	}

	out := map[ticket.AttemptRef]*Signals{}
	get := func(r ticket.AttemptRef) *Signals {
		if out[r] == nil {
			out[r] = &Signals{}
		}
		return out[r]
	}
	err = s.db.InReadTx(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, queryRuns, tickets, numbers)
		if err != nil {
			return fmt.Errorf("usage: runs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var ref ticket.AttemptRef
			var number int32
			var runs int64
			var first time.Time
			var last *time.Time
			if err := rows.Scan(&ref.TicketID, &number, &runs, &first, &last); err != nil {
				return fmt.Errorf("usage: runs: %w", err)
			}
			ref.Number = int(number)
			sig := get(ref)
			sig.Runs, sig.FirstClaimAt, sig.LastRunEndedAt = &runs, &first, last
			if last != nil {
				d := last.Sub(first)
				sig.WallClock = &d
			}
			zero := func() *int64 { return new(int64) }
			sig.Discussions, sig.ScopeExtensionDenials, sig.BlockedHandbacks = zero(), zero(), zero()
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()

		counts := []struct {
			name  string
			query string
			set   func(*Signals, int64)
		}{
			{"discussions", queryDiscussions, func(s *Signals, n int64) { *s.Discussions = n }},
			{"blocked", queryBlocked, func(s *Signals, n int64) { *s.BlockedHandbacks = n }},
			{"scope denials", queryDenials, func(s *Signals, n int64) { *s.ScopeExtensionDenials = n }},
		}
		for _, c := range counts {
			if err := s.eachCount(ctx, q, c.query, tickets, numbers, func(r ticket.AttemptRef, n int64) {
				// A count for an attempt with no lease is a record without a
				// run: leave it unknown rather than invent the run.
				if sig := out[r]; sig != nil {
					c.set(sig, n)
				}
			}); err != nil {
				return fmt.Errorf("usage: %s: %w", c.name, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	for ref, figures := range costs {
		sig := get(ref)
		sig.Cost = figures
		var turns int64
		for _, f := range figures {
			turns += f.Requests
		}
		sig.Turns = &turns
	}
	return out, nil
}

func (s *Store) eachCount(ctx context.Context, q db.Querier, query string, tickets []int64, numbers []int32, f func(ticket.AttemptRef, int64)) error {
	rows, err := q.Query(ctx, query, tickets, numbers)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ref ticket.AttemptRef
		var number int32
		var n int64
		if err := rows.Scan(&ref.TicketID, &number, &n); err != nil {
			return err
		}
		ref.Number = int(number)
		f(ref, n)
	}
	return rows.Err()
}
