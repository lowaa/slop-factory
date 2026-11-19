package reports

import (
	"context"
	"fmt"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
)

const (
	// A block overlaps the period if it began before its end and has not
	// ended before its start.
	queryScopeBlocks = `
SELECT b.ticket_id, t.title, b.blocking_ticket_id, bt.title, b.path, b.started_at, b.ended_at, COALESCE(b.end_reason, '')
  FROM scope_blocks b
  JOIN tickets t ON t.tenant_id = b.tenant_id AND t.id = b.ticket_id
  JOIN tickets bt ON bt.tenant_id = b.tenant_id AND bt.id = b.blocking_ticket_id
 WHERE b.started_at < $2 AND (b.ended_at IS NULL OR b.ended_at > $1)
 ORDER BY b.started_at, b.id`

	// A denial no grant or narrowing resolved ends with the lease it was made
	// under, if that has ended: the agent gave up.
	queryScopeDenials = `
SELECT d.ticket_id, t.title, d.path, d.blocking_ticket_id, d.blocking_path, d.denied_at,
       COALESCE(d.resolved_at, l.ended), COALESCE(d.resolution, CASE WHEN l.ended IS NOT NULL THEN 'gave_up' ELSE '' END)
  FROM scope_extension_denials d
  JOIN tickets t ON t.tenant_id = d.tenant_id AND t.id = d.ticket_id
  LEFT JOIN LATERAL (
      SELECT CASE WHEN l.released_at IS NOT NULL THEN l.released_at
                  WHEN l.expires_at < clock_timestamp() THEN l.expires_at END AS ended
        FROM leases l
       WHERE l.tenant_id = d.tenant_id AND l.ticket_id = d.ticket_id AND l.agent_id = d.agent_id
         AND l.acquired_at <= d.denied_at
       ORDER BY l.acquired_at DESC LIMIT 1) l ON d.resolved_at IS NULL
 WHERE d.denied_at >= $1 AND d.denied_at < $2
 ORDER BY d.denied_at, d.id`

	queryScopeNarrowings = `SELECT count(*) FROM scope_narrowings WHERE narrowed_at >= $1 AND narrowed_at < $2`

	// Workers exist from creation until disabled; consoles and conductors do
	// not take work from the queue.
	queryScopeWorkers = `
SELECT created_at, disabled_at FROM agents
 WHERE kind = 'worker' AND created_at < $2 AND (disabled_at IS NULL OR disabled_at > $1)`

	// A lease that was never released and has lapsed ended at its expiry.
	queryScopeBusy = `
SELECT l.acquired_at,
       CASE WHEN l.released_at IS NOT NULL THEN l.released_at
            WHEN l.expires_at < clock_timestamp() THEN l.expires_at END
  FROM leases l JOIN agents a ON a.tenant_id = l.tenant_id AND a.id = l.agent_id
 WHERE a.kind = 'worker' AND l.acquired_at < $2
   AND (l.released_at IS NULL OR l.released_at > $1) AND (l.released_at IS NOT NULL OR l.expires_at > $1)`
)

// ScopeCostRows reads the rows a scope cost report over [from, to) is
// computed from.
func (s *PostgresStore) ScopeCostRows(ctx context.Context, from, to time.Time) (*ScopeCostInput, error) {
	in := &ScopeCostInput{From: from, To: to}
	err := s.readTx(ctx, func(q db.Querier) error {
		locking, err := settings.EnabledIn(ctx, q, settings.TicketScopeLocking)
		if err != nil {
			return fmt.Errorf("reports: scope locking: %w", err)
		}
		in.LockingOff = !locking

		rows, err := q.Query(ctx, queryScopeBlocks, from, to)
		if err != nil {
			return fmt.Errorf("reports: scope blocks: %w", err)
		}
		for rows.Next() {
			var b ScopeBlock
			if err := rows.Scan(&b.TicketID, &b.TicketTitle, &b.BlockerID, &b.BlockerTitle, &b.Path, &b.Start, &b.End, &b.EndReason); err != nil {
				rows.Close()
				return fmt.Errorf("reports: scan scope block: %w", err)
			}
			in.Blocks = append(in.Blocks, b)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		rows, err = q.Query(ctx, queryScopeDenials, from, to)
		if err != nil {
			return fmt.Errorf("reports: scope denials: %w", err)
		}
		for rows.Next() {
			var d ScopeDenial
			if err := rows.Scan(&d.TicketID, &d.TicketTitle, &d.Path, &d.BlockerID, &d.BlockingPath, &d.DeniedAt, &d.ResolvedAt, &d.Resolution); err != nil {
				rows.Close()
				return fmt.Errorf("reports: scan scope denial: %w", err)
			}
			in.Denials = append(in.Denials, d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		if err := q.QueryRow(ctx, queryScopeNarrowings, from, to).Scan(&in.Narrowings); err != nil {
			return fmt.Errorf("reports: scope narrowings: %w", err)
		}
		if in.Workers, err = queryScopeSpans(ctx, q, queryScopeWorkers, from, to); err != nil {
			return err
		}
		in.Busy, err = queryScopeSpans(ctx, q, queryScopeBusy, from, to)
		return err
	})
	if err != nil {
		return nil, err
	}
	return in, nil
}

func queryScopeSpans(ctx context.Context, q db.Querier, sql string, from, to time.Time) ([]Span, error) {
	rows, err := q.Query(ctx, sql, from, to)
	if err != nil {
		return nil, fmt.Errorf("reports: scope spans: %w", err)
	}
	defer rows.Close()
	var out []Span
	for rows.Next() {
		var s Span
		if err := rows.Scan(&s.Start, &s.End); err != nil {
			return nil, fmt.Errorf("reports: scan scope span: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
