package ticket

import (
	"context"
	"fmt"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// LoadGraph reads the tickets query matches (newest first, at most Limit),
// plus the unfinished tickets that hold them up, in one snapshot.
//
// The snapshot matters because buildGraph treats everything it is given as
// a single state of the world: a status update committing between reads
// would otherwise layer a ticket behind a blocker it also reports finished.
//
// A filter must never make a ticket look free to start when it is not, so
// the graph is closed upstream: from every shown ticket that is itself
// unfinished, each unfinished blocker is loaded -- shown or not -- and so on
// up. A loaded blocker the filter did not match is returned as Hidden. A
// finished blocker holds nothing up (the claim path's rule), so a hidden one
// is dropped with its edge, and a finished ticket pulls nothing in.
func (s *PostgresStore) LoadGraph(ctx context.Context, query *GraphQuery) (*GraphInput, error) {
	in := &GraphInput{Hidden: map[int64]bool{}}
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		matched := query.Filter
		matched.Summary = true
		matched.Sort, _ = SortByName(SortNewest)
		if query.Limit > 0 {
			matched.Limit = query.Limit + 1
		}
		shown, err := listTickets(ctx, q, &matched)
		if err != nil {
			return err
		}
		if query.Limit > 0 && len(shown) > query.Limit {
			shown, in.Truncated = shown[:query.Limit], true
		}
		shownIDs := make([]int64, len(shown))
		isShown := make(map[int64]bool, len(shown))
		for i := range shown {
			shownIDs[i] = shown[i].ID
			isShown[shown[i].ID] = true
		}

		upstream, err := loadUnfinishedUpstream(ctx, q, shownIDs)
		if err != nil {
			return err
		}
		var extra []int64
		for _, id := range upstream {
			if !isShown[id] {
				extra = append(extra, id)
				in.Hidden[id] = true
			}
		}
		in.Tickets = shown
		if len(extra) > 0 {
			hidden, err := listTickets(ctx, q, &ListFilter{IDs: extra, Summary: true})
			if err != nil {
				return err
			}
			in.Tickets = append(in.Tickets, hidden...)
		}

		ids := make([]int64, len(in.Tickets))
		for i := range in.Tickets {
			ids[i] = in.Tickets[i].ID
		}
		if in.Edges, err = loadEdgesAmong(ctx, q, ids); err != nil {
			return err
		}
		if in.Unblocks, err = loadUnblocks(ctx, q, ids); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return in, nil
}

const unfinishedSQL = `NOT IN ('done', 'cancelled')`

// loadUnfinishedUpstream is every unfinished ticket that, through
// unfinished tickets only, holds up an unfinished one of ids.
func loadUnfinishedUpstream(ctx context.Context, q db.Querier, ids []int64) ([]int64, error) {
	rows, err := q.Query(ctx, `
		WITH RECURSIVE up(id) AS (
			SELECT d.depends_on_ticket_id
			  FROM ticket_deps d
			  JOIN tickets t ON t.id = d.ticket_id
			  JOIN tickets b ON b.id = d.depends_on_ticket_id
			 WHERE d.ticket_id = ANY($1) AND t.status `+unfinishedSQL+` AND b.status `+unfinishedSQL+`
			UNION
			SELECT d.depends_on_ticket_id
			  FROM up
			  JOIN ticket_deps d ON d.ticket_id = up.id
			  JOIN tickets b ON b.id = d.depends_on_ticket_id
			 WHERE b.status `+unfinishedSQL+`
		)
		SELECT id FROM up ORDER BY id`, ids)
	if err != nil {
		return nil, fmt.Errorf("load upstream blockers: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan upstream blocker: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func loadEdgesAmong(ctx context.Context, q db.Querier, ids []int64) ([]GraphEdge, error) {
	rows, err := q.Query(ctx, `
		SELECT depends_on_ticket_id, ticket_id FROM ticket_deps
		 WHERE ticket_id = ANY($1) AND depends_on_ticket_id = ANY($1)
		 ORDER BY depends_on_ticket_id, ticket_id`, ids)
	if err != nil {
		return nil, fmt.Errorf("load blocking edges: %w", err)
	}
	defer rows.Close()
	edges := []GraphEdge{}
	for rows.Next() {
		var e GraphEdge
		if err := rows.Scan(&e.Blocker, &e.Blocked); err != nil {
			return nil, fmt.Errorf("scan blocking edge: %w", err)
		}
		edges = append(edges, e)
	}
	return edges, rows.Err()
}

// loadUnblocks counts, for each of ids that is unfinished, the unfinished
// tickets waiting on it through unfinished tickets, over every ticket in the
// database -- the same definition as buildGraph's, which cannot see the
// dependants a filter hid.
func loadUnblocks(ctx context.Context, q db.Querier, ids []int64) (map[int64]int, error) {
	rows, err := q.Query(ctx, `
		WITH RECURSIVE down(root, id) AS (
			SELECT d.depends_on_ticket_id, d.ticket_id
			  FROM ticket_deps d
			  JOIN tickets r ON r.id = d.depends_on_ticket_id
			  JOIN tickets t ON t.id = d.ticket_id
			 WHERE d.depends_on_ticket_id = ANY($1)
			   AND r.status `+unfinishedSQL+` AND t.status `+unfinishedSQL+`
			UNION
			SELECT down.root, d.ticket_id
			  FROM down
			  JOIN ticket_deps d ON d.depends_on_ticket_id = down.id
			  JOIN tickets t ON t.id = d.ticket_id
			 WHERE t.status `+unfinishedSQL+`
		)
		SELECT root, count(*) FROM down GROUP BY root`, ids)
	if err != nil {
		return nil, fmt.Errorf("count unblocked tickets: %w", err)
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("scan unblocked count: %w", err)
		}
		out[id] = n
	}
	return out, rows.Err()
}
