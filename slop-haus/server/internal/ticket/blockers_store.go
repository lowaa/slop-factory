package ticket

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lowaa/slop-factory/go-db/pgerr"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// depsBarrier is a test-only seam, like claimBarrier. Production leaves it
// nil. It sits between the cycle check and the insert, which is the window
// the advisory lock closes and the only place a test can force two edge
// writers to genuinely overlap.
var depsBarrier func()

// AddDependency is Service.AddDependency's write: the permission check, the
// edge, the status it implies and the comment, in one transaction.
//
// Under the claim lock because the cycle invariant is graph-wide, and no
// constraint can express it: two opposite edges added at the same moment
// would each look acyclic on its own, and both would commit. The claim lock
// rather than a lock of its own -- these edges are exactly what the claim
// predicate reads, and the release path already writes one under it, so
// one serialization domain means there is no lock order to get wrong. It
// also keeps the agent's permission, which reads its lease, true until the
// write commits.
func (s *PostgresStore) AddDependency(ctx context.Context, by Author, ticketID, dependsOnID int64) (*DependencyChange, error) {
	change := &DependencyChange{}
	err := s.db.InTx(ctx, func(q db.Querier) error {
		if err := db.TakeAdvisoryLock(ctx, q, claimAdvisoryKey); err != nil {
			return err
		}
		if by.AgentID != nil && by.onBehalf == nil {
			if err := agentMayEditBlockers(ctx, q, *by.AgentID, ticketID); err != nil {
				return err
			}
		}
		var err error
		if change.Changed, err = addBlocker(ctx, q, by, ticketID, dependsOnID, ""); err != nil {
			return err
		}
		change.Ticket, err = loadTicket(ctx, q, ticketID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return change, nil
}

// RemoveDependency lifts the edge, and -- since removing the last
// unfinished blocker is what the board's "remove" is for, the human's
// recovery path -- actually unblocks the ticket.
func (s *PostgresStore) RemoveDependency(ctx context.Context, by Author, ticketID, dependsOnID int64) (*DependencyChange, error) {
	change := &DependencyChange{}
	err := s.db.InTx(ctx, func(q db.Querier) error {
		if err := db.TakeAdvisoryLock(ctx, q, claimAdvisoryKey); err != nil {
			return err
		}
		if by.AgentID != nil && by.onBehalf == nil {
			if err := agentMayEditBlockers(ctx, q, *by.AgentID, ticketID); err != nil {
				return err
			}
			if err := agentMayRemoveEdge(ctx, q, *by.AgentID, ticketID, dependsOnID); err != nil {
				return err
			}
		}
		var err error
		if change.Changed, err = removeBlocker(ctx, q, by, ticketID, dependsOnID); err != nil {
			return err
		}
		change.Ticket, err = loadTicket(ctx, q, ticketID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return change, nil
}

// removeBlocker deletes one edge and records who did it, reporting whether
// there was an edge to delete. Callers hold the claim advisory lock.
func removeBlocker(ctx context.Context, q db.Querier, by Author, ticketID, dependsOnID int64) (bool, error) {
	tag, err := q.Exec(ctx, `
		DELETE FROM ticket_deps
		WHERE ticket_id = $1 AND depends_on_ticket_id = $2`, ticketID, dependsOnID)
	if err != nil {
		return false, fmt.Errorf("remove dependency: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if err := unblockSatisfied(ctx, q); err != nil {
		return false, err
	}
	title, err := blockerTitle(ctx, q, dependsOnID)
	if err != nil {
		return false, err
	}
	_, err = insertComment(ctx, q, ticketID, by, by.onBehalf.annotate(fmt.Sprintf("No longer blocked on #%d: %s", dependsOnID, title)))
	return true, err
}

// agentMayEditBlockers is the rule for which tickets' blockers an agent may
// change: the ticket it holds, or one it filed that nobody has started --
// the follow-ups it splits off. Anything else is another agent's work or a
// person's queue, and re-gating those silently changes what the fleet is
// handed. Blocked is included because adding the first blocker to an open
// ticket makes it blocked, and the agent must still be able to correct its
// own edit.
//
// Callers hold the claim lock, so the lease it reads cannot end before the
// write it guards commits.
func agentMayEditBlockers(ctx context.Context, q db.Querier, agentID uuid.UUID, ticketID int64) error {
	var raw string
	var filedByAgent, held bool
	err := q.QueryRow(ctx, `
		SELECT t.status, t.created_by_agent_id IS NOT DISTINCT FROM $2,
		       EXISTS (SELECT 1 FROM leases l WHERE l.ticket_id = t.id AND l.agent_id = $2 AND `+activeLeaseClause+`)
		  FROM tickets t WHERE t.id = $1`, ticketID, agentID).Scan(&raw, &filedByAgent, &held)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: #%d", ErrNotFound, ticketID)
	}
	if err != nil {
		return fmt.Errorf("check blocker permission: %w", err)
	}
	if held {
		return nil
	}
	status := Status(raw)
	if filedByAgent && (status == StatusPreparing || status == StatusOpen || status == StatusBlocked) {
		return nil
	}
	return fmt.Errorf("%w (#%d is %s)", ErrBlockersNotEditable, ticketID, status)
}

// agentMayRemoveEdge refuses an agent removing an edge somebody else added.
// No edge at all is not refused: the removal then changes nothing.
func agentMayRemoveEdge(ctx context.Context, q db.Querier, agentID uuid.UUID, ticketID, dependsOnID int64) error {
	var addedBy *uuid.UUID
	err := q.QueryRow(ctx, `
		SELECT added_by_agent_id FROM ticket_deps
		 WHERE ticket_id = $1 AND depends_on_ticket_id = $2`, ticketID, dependsOnID).Scan(&addedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read dependency: %w", err)
	}
	if addedBy == nil || *addedBy != agentID {
		return fmt.Errorf("%w (#%d on #%d)", ErrBlockerNotRemovable, ticketID, dependsOnID)
	}
	return nil
}

// addBlocker is one edge added by `by`, with everything that goes with it:
// the cycle check, the ticket moving to blocked if it is open and now
// waiting, and the comment recording who did it. Reports whether the edge
// is new. Every route that adds a blocker -- the board, an agent's tool,
// ticket_create's blocked_on, ticket_release's blocked_on -- comes through
// here, so they cannot disagree about any of it.
//
// Callers hold the claim advisory lock on q's transaction.
func addBlocker(ctx context.Context, q db.Querier, by Author, ticketID, dependsOnID int64, note string) (bool, error) {
	// Read first so a missing blocker is named on its own: the foreign key
	// can only say that one of the two ends is missing.
	title, err := blockerTitle(ctx, q, dependsOnID)
	if err != nil {
		return false, err
	}
	added, err := addDependency(ctx, q, ticketID, dependsOnID, by.AgentID)
	if err != nil || !added {
		return false, err
	}
	// Only open: a held ticket keeps its lease (blockers gate claims, not
	// work in progress), a preparing one is judged when it is marked ready,
	// and one in discussion or review is waiting on a person already.
	if err := authorMover(by, "blocked").set(ctx, q); err != nil {
		return false, err
	}
	// Only the ticket's queue state changes: its attempts are queued either way.
	if _, err := deriveStatus(ctx, q, `t.id = $1 AND t.status = 'open' AND `+isBlocked, ticketID); err != nil {
		return false, fmt.Errorf("block ticket: %w", err)
	}
	body := fmt.Sprintf("Blocked on #%d: %s", dependsOnID, title)
	if note != "" {
		body += "\n\n" + note
	}
	_, err = insertComment(ctx, q, ticketID, by, by.onBehalf.annotate(body))
	return true, err
}

// addDependency inserts the edge ticketID -> dependsOnID, refusing one that
// would close a cycle, and reports whether it is new. addedBy is the agent
// adding it, nil for a person.
//
// Every edge goes through here, including the release path: an agent
// releasing onto a ticket that already waits on the one it is holding would
// otherwise knot the two together, and that route is driven by a model
// rather than by a human reading the board.
//
// Callers must already hold the claim advisory lock on q's transaction.
// The check is only sound if nothing else can insert an edge between it
// and the insert it guards.
func addDependency(ctx context.Context, q db.Querier, ticketID, dependsOnID int64, addedBy *uuid.UUID) (bool, error) {
	if ticketID == dependsOnID {
		return false, fmt.Errorf("%w: #%d cannot depend on itself", ErrDependencyCycle, ticketID)
	}

	// The new edge is ticketID -> dependsOnID. It closes a cycle exactly
	// when dependsOnID already reaches ticketID.
	reaches, err := dependsOnTransitively(ctx, q, dependsOnID, ticketID)
	if err != nil {
		return false, err
	}
	if reaches {
		return false, fmt.Errorf("%w: #%d already depends on #%d", ErrDependencyCycle, dependsOnID, ticketID)
	}

	if depsBarrier != nil {
		depsBarrier()
	}

	// ON CONFLICT DO NOTHING keeps whoever added an existing edge as its
	// owner: an agent re-adding a person's edge must not gain the right to
	// remove it.
	tag, err := q.Exec(ctx, `
		INSERT INTO ticket_deps (ticket_id, depends_on_ticket_id, added_by_agent_id)
		VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING`, ticketID, dependsOnID, addedBy)
	if err != nil {
		if pgerr.IsForeignKeyViolation(err) {
			return false, fmt.Errorf("%w: #%d or #%d", ErrNotFound, ticketID, dependsOnID)
		}
		return false, fmt.Errorf("add dependency: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func blockerTitle(ctx context.Context, q db.Querier, id int64) (string, error) {
	var title string
	err := q.QueryRow(ctx, `SELECT title FROM tickets WHERE id = $1`, id).Scan(&title)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("%w: #%d", ErrNotFound, id)
	}
	if err != nil {
		return "", fmt.Errorf("read blocker title: %w", err)
	}
	return title, nil
}
