package ticket

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
)

// QueueBlockage is the board's answer to "why is nothing being picked up":
// agents are idle and work is waiting, but every ticket they could take
// collides with path scopes held elsewhere. It is the Claim/Peek blocked
// result, asked of every idle agent at once.
type QueueBlockage struct {
	// Blocked is the first ticket found waiting, and what it waits on.
	Blocked      Blocked
	TicketTitle  string
	HolderTitle  string
	HolderStatus Status
	// HolderAgentName is empty when no agent is known to hold the scope: a
	// ticket in review that was never handed over by one, say.
	HolderAgentName string
}

// QueueBlockage reports nil unless there is an idle agent, work it could
// take, and no idle agent that could take any of it. Not blocked while
// processing is switched off: nothing is meant to be picked up then.
//
// Console agents and conductors are left out: a person's own session, or one
// started from the board for them, does not drain the queue. An agent
// holding a lease is not idle, and what it holds is how everyone else comes
// to be blocked, so its busyness is no reason to hide it.
func (s *PostgresStore) QueueBlockage(ctx context.Context) (*QueueBlockage, error) {
	var out *QueueBlockage
	shared, err := s.sharedPaths(ctx)
	if err != nil {
		return nil, err
	}
	err = s.db.InTx(ctx, func(q db.Querier) error {
		enabled, err := settings.EnabledIn(ctx, q, settings.TicketProcessingEnabled)
		if err != nil {
			return err
		}
		if !enabled {
			return nil
		}
		held, err := activeScopes(ctx, q)
		if err != nil {
			return err
		}
		locking, err := scopeLocking(ctx, q)
		if err != nil {
			return err
		}
		rows, err := q.Query(ctx, `
			SELECT a.id FROM agents a
			 WHERE a.disabled_at IS NULL AND a.kind = 'worker'
			   AND NOT EXISTS (SELECT 1 FROM leases l WHERE l.agent_id = a.id AND `+activeLeaseClause+`)
			 ORDER BY a.name`)
		if err != nil {
			return fmt.Errorf("load idle agents: %w", err)
		}
		idle, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return err
		}

		var first *Blocked
		for _, agentID := range idle {
			candidates, err := candidateTickets(ctx, q, agentID, nil, true)
			if err != nil {
				return err
			}
			for _, candidateID := range candidates {
				paths, err := pathsForTicket(ctx, q, candidateID)
				if err != nil {
					return err
				}
				blocked := firstConflict(candidateID, paths, held, shared, locking)
				if blocked == nil {
					return nil
				}
				if first == nil {
					first = blocked
				}
			}
		}
		if first == nil {
			return nil
		}
		out = &QueueBlockage{Blocked: *first}
		return describeBlockage(ctx, q, out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func describeBlockage(ctx context.Context, q db.Querier, b *QueueBlockage) error {
	if err := q.QueryRow(ctx, `SELECT title FROM tickets WHERE id = $1`, b.Blocked.TicketID).Scan(&b.TicketTitle); err != nil {
		return fmt.Errorf("load blocked ticket: %w", err)
	}
	if err := q.QueryRow(ctx, `
		SELECT t.title, t.status, COALESCE(a.name, '')
		  FROM tickets t
		  LEFT JOIN agents a ON a.id = $2
		 WHERE t.id = $1`, b.Blocked.ConflictingTicketID, b.Blocked.HolderAgentID).
		Scan(&b.HolderTitle, &b.HolderStatus, &b.HolderAgentName); err != nil {
		return fmt.Errorf("load holding ticket: %w", err)
	}
	return nil
}
