package conductor

import (
	"context"
	"fmt"
	"math"
	"slices"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

func (s *PostgresStore) Events(ctx context.Context, conductorID, id uuid.UUID, after int64, limit int) ([]Event, error) {
	out := []Event{}
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		var exists bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM conductor_conversations WHERE id = $1 AND conductor_id = $2)`,
			id, conductorID).Scan(&exists); err != nil {
			return fmt.Errorf("find conversation: %w", err)
		}
		if !exists {
			return ErrNotFound
		}
		rows, err := q.Query(ctx, `
			SELECT seq, kind, turn, body, block, created_at FROM conductor_conversation_events
			 WHERE conversation_id = $1 AND seq > $2 ORDER BY seq LIMIT $3`, id, after, limit)
		if err != nil {
			return fmt.Errorf("read events: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var e Event
			if err := rows.Scan(&e.Seq, &e.Kind, &e.Turn, &e.Body, &e.Block, &e.CreatedAt); err != nil {
				return fmt.Errorf("scan event: %w", err)
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, wrap(err, "read events")
	}
	return out, nil
}

func (s *PostgresStore) EventsByTurns(ctx context.Context, conductorID, id uuid.UUID, before int64, turns int) (*TranscriptPage, error) {
	page := &TranscriptPage{Events: []Event{}}
	upper := before
	if upper <= 0 {
		upper = math.MaxInt64
	}
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		var exists bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM conductor_conversations WHERE id = $1 AND conductor_id = $2)`,
			id, conductorID).Scan(&exists); err != nil {
			return fmt.Errorf("find conversation: %w", err)
		}
		if !exists {
			return ErrNotFound
		}
		// One more than asked for tells whether the oldest turn found is the
		// conversation's first, which also owns the events before it.
		starts, err := userSeqsBefore(ctx, q, id, upper, turns+1)
		if err != nil {
			return err
		}
		start, hasMore, err := chooseStart(ctx, q, id, upper, starts, turns)
		if err != nil {
			return err
		}
		rows, err := q.Query(ctx, `
			SELECT seq, kind, turn, body, block, created_at FROM conductor_conversation_events
			 WHERE conversation_id = $1 AND seq >= $2 AND seq < $3 ORDER BY seq`, id, start, upper)
		if err != nil {
			return fmt.Errorf("read events: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var e Event
			if err := rows.Scan(&e.Seq, &e.Kind, &e.Turn, &e.Body, &e.Block, &e.CreatedAt); err != nil {
				return fmt.Errorf("scan event: %w", err)
			}
			page.Events = append(page.Events, e)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		page.HasMore, page.NextBefore = hasMore, start
		if before <= 0 {
			page.Prompts, err = recentPrompts(ctx, q, id)
		}
		return err
	})
	if err != nil {
		return nil, wrap(err, "read events by turns")
	}
	return page, nil
}

func userSeqsBefore(ctx context.Context, q db.Querier, id uuid.UUID, upper int64, limit int) ([]int64, error) {
	rows, err := q.Query(ctx, `
		SELECT seq FROM conductor_conversation_events
		 WHERE conversation_id = $1 AND kind = 'user' AND seq < $2 ORDER BY seq DESC LIMIT $3`, id, upper, limit)
	if err != nil {
		return nil, fmt.Errorf("find turns: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			return nil, fmt.Errorf("scan turn: %w", err)
		}
		out = append(out, seq)
	}
	return out, rows.Err()
}

// chooseStart is the seq the page starts at: the start of the oldest turn
// that fits, or 0 when that turn is the conversation's first. starts is the
// user events before upper, newest first, holding one more than turns when
// older turns exist.
func chooseStart(ctx context.Context, q db.Querier, id uuid.UUID, upper int64, starts []int64, turns int) (start int64, hasMore bool, err error) {
	if len(starts) == 0 {
		return 0, false, nil
	}
	candidates := starts[:min(len(starts), turns)]
	// Text per turn, from the lightweight columns only.
	rows, err := q.Query(ctx, `
		SELECT seq, octet_length(body) FROM conductor_conversation_events
		 WHERE conversation_id = $1 AND seq >= $2 AND seq < $3 ORDER BY seq DESC`, id, candidates[len(candidates)-1], upper)
	if err != nil {
		return 0, false, fmt.Errorf("size turns: %w", err)
	}
	defer rows.Close()
	sizes := make([]int, len(candidates))
	turn := 0
	for rows.Next() {
		var seq int64
		var n int
		if err := rows.Scan(&seq, &n); err != nil {
			return 0, false, fmt.Errorf("scan size: %w", err)
		}
		sizes[turn] += n
		if seq == candidates[turn] && turn < len(candidates)-1 {
			turn++
		}
	}
	if err := rows.Err(); err != nil {
		return 0, false, err
	}
	kept, total := 0, 0
	for _, n := range sizes {
		if kept > 0 && total+n > MaxPageBytes {
			break
		}
		kept++
		total += n
	}
	if kept == len(starts) {
		return 0, false, nil
	}
	return starts[kept-1], true, nil
}

func recentPrompts(ctx context.Context, q db.Querier, id uuid.UUID) ([]string, error) {
	rows, err := q.Query(ctx, `
		SELECT body FROM conductor_conversation_events
		 WHERE conversation_id = $1 AND kind = 'user' AND btrim(body) <> '' ORDER BY seq DESC LIMIT $2`, id, recentPromptScan)
	if err != nil {
		return nil, fmt.Errorf("read prompts: %w", err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	newestFirst := []string{}
	for rows.Next() && len(newestFirst) < MaxRecentPrompts {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, fmt.Errorf("scan prompt: %w", err)
		}
		if !seen[body] {
			seen[body] = true
			newestFirst = append(newestFirst, body)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.Reverse(newestFirst)
	return newestFirst, nil
}
