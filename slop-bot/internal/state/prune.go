package state

import (
	"context"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/lowaa/slop-factory/slop-bot/internal/sqlitex"
)

// PruneCutoffs says what Prune deletes: rows older than each cutoff. A zero
// cutoff leaves that table alone. The sweep computes them from the
// configured retention and its clock.
type PruneCutoffs struct {
	// Events deletes events appended before it.
	Events time.Time
	// StateChanges deletes agent state history recorded before it.
	StateChanges time.Time
	// ReadMessages deletes messages read before it. An open ask is kept
	// however old: it is still the user's to answer.
	ReadMessages time.Time
	// EndedAgents deletes agents that ended before it, with their history.
	// One that still has worktrees recorded is kept, so no worktree slop-bot
	// made is orphaned: cleanup removes the worktree first.
	EndedAgents time.Time
}

// PruneResult counts the rows Prune deleted.
type PruneResult struct {
	Events       int64
	StateChanges int64
	Messages     int64
	Agents       int64
}

// Pruner deletes old rows from state.db.
type Pruner struct {
	db *sqlitex.DB
}

// Prune deletes the rows older than c's cutoffs, in one transaction.
func (p *Pruner) Prune(ctx context.Context, c *PruneCutoffs) (*PruneResult, error) {
	var r PruneResult
	err := p.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		steps := []struct {
			name   string
			cutoff time.Time
			n      *int64
			query  string
		}{
			{"events", c.Events, &r.Events, "DELETE FROM events WHERE at < ?"},
			{"agent state changes", c.StateChanges, &r.StateChanges, "DELETE FROM agent_state_changes WHERE at < ?"},
			{"read messages", c.ReadMessages, &r.Messages, `DELETE FROM messages WHERE status = 'read' AND read_at < ?
				AND id NOT IN (SELECT message_id FROM asks WHERE closed_at IS NULL)`},
			{"ended agents", c.EndedAgents, &r.Agents, `DELETE FROM agents WHERE ended_at < ?
				AND NOT EXISTS (SELECT 1 FROM agent_worktrees w WHERE w.agent_id = agents.id)`},
		}
		for _, s := range steps {
			if s.cutoff.IsZero() {
				continue
			}
			res, err := tx.NewRaw(s.query, toMillis(s.cutoff)).Exec(ctx)
			if err != nil {
				return fmt.Errorf("state: pruning %s: %w", s.name, err)
			}
			if *s.n, err = res.RowsAffected(); err != nil {
				return fmt.Errorf("state: pruning %s: %w", s.name, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &r, nil
}
