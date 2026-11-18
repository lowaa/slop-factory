package ticket

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// What exclusive scopes cost is recorded where the decisions are made, under
// the claim lock, so the Reports page can say how often and for how long
// tickets were held back. The tables are append-only history for reporting:
// nothing here changes what a claim or an extension decides.

// Why a scope_blocks interval ended.
const (
	scopeBlockClaimed = "claimed"
	scopeBlockCleared = "cleared"
)

type scopeBlockKey struct {
	ticketID, blockingID int64
	path                 string
}

// recordScopeBlocks reconciles the open intervals of the tickets a claim
// evaluated with what it found: each ticket in blocked has an interval open
// for the scope that held it back (opened now if it was not already, so a
// ticket held across many polls is one row), and an interval of an evaluated
// ticket that is no longer held back, by that scope, is closed.
//
// Tickets the claim did not evaluate are left alone: it stops at the first
// ticket it can grant, so the ones after that say nothing new.
func recordScopeBlocks(ctx context.Context, q db.Querier, evaluated []int64, blocked map[int64]*Blocked) error {
	if len(evaluated) == 0 {
		return nil
	}
	rows, err := q.Query(ctx, `
		SELECT id, ticket_id, blocking_ticket_id, path FROM scope_blocks
		 WHERE ended_at IS NULL AND ticket_id = ANY($1)`, evaluated)
	if err != nil {
		return fmt.Errorf("load open scope blocks: %w", err)
	}
	open := map[scopeBlockKey]int64{}
	for rows.Next() {
		var id int64
		var k scopeBlockKey
		if err := rows.Scan(&id, &k.ticketID, &k.blockingID, &k.path); err != nil {
			rows.Close()
			return fmt.Errorf("scan open scope block: %w", err)
		}
		open[k] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, b := range blocked {
		k := scopeBlockKey{b.TicketID, b.ConflictingTicketID, b.ConflictingPath}
		if _, ok := open[k]; ok {
			delete(open, k)
			continue
		}
		if _, err := q.Exec(ctx, `
			INSERT INTO scope_blocks (ticket_id, blocking_ticket_id, path, started_at)
			VALUES ($1, $2, $3, clock_timestamp())`, k.ticketID, k.blockingID, k.path); err != nil {
			return fmt.Errorf("open scope block: %w", err)
		}
	}
	// What is left was open and is no longer found.
	stale := make([]int64, 0, len(open))
	for _, id := range open {
		stale = append(stale, id)
	}
	return endScopeBlocks(ctx, q, `id = ANY($2)`, scopeBlockCleared, stale)
}

// endScopeBlocksOf closes every open interval of a ticket.
func endScopeBlocksOf(ctx context.Context, q db.Querier, ticketID int64, reason string) error {
	return endScopeBlocks(ctx, q, `ticket_id = $2`, reason, ticketID)
}

// endScopeBlocksOfSettled closes the open intervals of tickets that have left
// the queue -- cancelled, parked, back in preparing, cooling down until a
// time, or waiting on a dependency -- so they do not read as still held back,
// which no claim would ever evaluate them to close. It mirrors the
// eligibility candidateTickets applies to every agent; role requirements
// are per agent and are left to the claims that evaluate the ticket.
func endScopeBlocksOfSettled(ctx context.Context, q db.Querier) error {
	_, err := q.Exec(ctx, `
		UPDATE scope_blocks b SET ended_at = clock_timestamp(), end_reason = $1
		 WHERE b.ended_at IS NULL
		   AND NOT EXISTS (SELECT 1 FROM tickets t WHERE t.id = b.ticket_id
		        AND t.status IN `+holdingStatusesSQL+`
		        AND (t.blocked_until IS NULL OR t.blocked_until <= now())
		        AND (t.status = 'done' OR NOT EXISTS (
		            SELECT 1 FROM ticket_deps d JOIN tickets dep ON dep.id = d.depends_on_ticket_id
		             WHERE d.ticket_id = t.id AND dep.status NOT IN `+TerminalStatusesSQL+`)))`,
		scopeBlockCleared)
	if err != nil {
		return fmt.Errorf("end settled scope blocks: %w", err)
	}
	return nil
}

func endScopeBlocks(ctx context.Context, q db.Querier, where, reason string, arg any) error {
	if _, err := q.Exec(ctx, `
		UPDATE scope_blocks SET ended_at = clock_timestamp(), end_reason = $1
		 WHERE ended_at IS NULL AND `+where, reason, arg); err != nil {
		return fmt.Errorf("end scope blocks: %w", err)
	}
	return nil
}

func recordExtensionDenied(ctx context.Context, q db.Querier, agentID uuid.UUID, ticketID int64, path string, b *Blocked) error {
	if _, err := q.Exec(ctx, `
		INSERT INTO scope_extension_denials (ticket_id, agent_id, path, blocking_ticket_id, blocking_path, denied_at)
		VALUES ($1, $2, $3, $4, $5, clock_timestamp())`,
		ticketID, agentID, path, b.ConflictingTicketID, b.ConflictingPath); err != nil {
		return fmt.Errorf("record scope extension denial: %w", err)
	}
	return nil
}

// resolveExtensionDenials marks the agent's open denials on the ticket
// resolved: another agent's are its own to resolve or give up. A grant
// resolves the denials of paths it granted, whether as that path or a
// directory containing it; a narrowing (paths nil) resolves all.
func resolveExtensionDenials(ctx context.Context, q db.Querier, agentID uuid.UUID, ticketID int64, resolution string, paths []string) error {
	if _, err := q.Exec(ctx, `
		UPDATE scope_extension_denials SET resolved_at = clock_timestamp(), resolution = $2
		 WHERE ticket_id = $1 AND agent_id = $4 AND resolved_at IS NULL AND ($3::text[] IS NULL OR EXISTS (
		       SELECT 1 FROM unnest($3::text[]) g WHERE path = g OR (g LIKE '%/' AND starts_with(path, g))))`,
		ticketID, resolution, paths, agentID); err != nil {
		return fmt.Errorf("resolve scope extension denials: %w", err)
	}
	return nil
}

func recordNarrowing(ctx context.Context, q db.Querier, agentID uuid.UUID, ticketID int64, removed, kept int) error {
	if _, err := q.Exec(ctx, `
		INSERT INTO scope_narrowings (ticket_id, agent_id, removed, kept, narrowed_at)
		VALUES ($1, $2, $3, $4, clock_timestamp())`, ticketID, agentID, removed, kept); err != nil {
		return fmt.Errorf("record scope narrowing: %w", err)
	}
	return resolveExtensionDenials(ctx, q, agentID, ticketID, "narrowed", nil)
}
