package ticket

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// ExtendScope grants extra paths to the ticket the agent holds, or denies
// the extension and names what conflicted.
//
// Denial is expected, not exceptional: the agent narrows its approach,
// releases with a comment, or files a follow-up depending on the
// conflicting ticket.
func (s *PostgresStore) ExtendScope(ctx context.Context, agentID uuid.UUID, paths []model.Path, check func(held []string) error) (*ExtendResult, error) {
	result := &ExtendResult{}
	shared, err := s.sharedPaths(ctx)
	if err != nil {
		return nil, err
	}

	err = s.withHeldLease(ctx, agentID, func(q db.Querier, _, ticketID int64) error {
		// After the lease lookup, which it cannot change: a lease it would
		// expire is one the lookup already does not count as held.
		if err := expireStale(ctx, q); err != nil {
			return err
		}

		held, err := activeScopes(ctx, q)
		if err != nil {
			return err
		}
		others := heldByOthers(held, ticketID)

		if check != nil {
			if err := check(heldBy(held, ticketID)); err != nil {
				return err
			}
		}

		for _, p := range paths {
			blocked, sharedOverlaps := scanConflicts(ticketID, []string{p.Path}, others, shared)
			if blocked != nil {
				result.Denied = blocked
				return recordExtensionDenied(ctx, q, agentID, ticketID, p.Path, blocked)
			}
			result.SharedOverlaps = append(result.SharedOverlaps, sharedOverlaps...)
		}

		for _, p := range paths {
			if _, err := q.Exec(ctx, `
				INSERT INTO ticket_paths (ticket_id, path, source)
				VALUES ($1, $2, 'extended') ON CONFLICT (tenant_id, ticket_id, path) DO NOTHING`,
				ticketID, p.Path); err != nil {
				return fmt.Errorf("insert extended path: %w", err)
			}
		}
		result.Granted = true
		return resolveExtensionDenials(ctx, q, agentID, ticketID, "granted", scopePaths(paths))
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// narrowBarrier is a test-only seam, like claimBarrier. Production leaves it
// nil. It runs after the narrowing is written and before the transaction
// commits, so a test can show that a claim cannot scan scopes in between.
var narrowBarrier func()

// NarrowScope removes remove from the ticket the agent holds and adds keep,
// every one of which must lie inside a removed path.
//
// It takes the claim lock like ExtendScope, though it can only free scope:
// a claim's conflict scan must see the ticket's paths either entirely
// before or entirely after the change, never a removal without the kept
// sub-paths that replace it. A kept path inside a removed one was already
// held by this ticket, so no other ticket can hold anything that overlaps
// it, and it needs no conflict check.
//
// Removed paths are deleted whatever their source, and a kept path is
// stored as extended, so what the agent narrowed to outlives a release as
// extensions do. Waiters need no wake-up of their own: the worker poll polls
// the same conflict scan, and the board revision is bumped by the trigger on
// ticket_paths.
func (s *PostgresStore) NarrowScope(ctx context.Context, agentID uuid.UUID, remove, keep []string) (*NarrowResult, error) {
	var result *NarrowResult
	err := s.withHeldLease(ctx, agentID, func(q db.Querier, _, ticketID int64) error {
		live, err := liveAttemptCount(ctx, q, ticketID)
		if err != nil {
			return err
		}
		if live > 1 {
			return ErrNarrowWithSiblings
		}
		current, err := pathsForTicket(ctx, q, ticketID)
		if err != nil {
			return err
		}
		have := make(map[string]struct{}, len(current))
		for _, p := range current {
			have[p] = struct{}{}
		}
		for _, p := range remove {
			if _, ok := have[p]; !ok {
				return fmt.Errorf("%w: %q", ErrNarrowUnknownPath, p)
			}
		}
		for _, k := range keep {
			if !insideAny(k, remove) {
				return fmt.Errorf("%w: %q", ErrNarrowAdds, k)
			}
		}

		remaining := make([]string, 0, len(current))
		dropped := make(map[string]struct{}, len(remove))
		for _, p := range remove {
			dropped[p] = struct{}{}
		}
		for _, p := range current {
			if _, gone := dropped[p]; !gone {
				remaining = append(remaining, p)
			}
		}
		keep = dedupe(keep)
		remaining = dedupe(append(remaining, keep...))
		if len(remaining) == 0 {
			return ErrNarrowEmpty
		}

		if _, err := q.Exec(ctx,
			`DELETE FROM ticket_paths WHERE ticket_id = $1 AND path = ANY($2)`, ticketID, remove); err != nil {
			return fmt.Errorf("delete narrowed paths: %w", err)
		}
		for _, k := range keep {
			if _, err := q.Exec(ctx, `
				INSERT INTO ticket_paths (ticket_id, path, source)
				VALUES ($1, $2, 'extended') ON CONFLICT (tenant_id, ticket_id, path) DO NOTHING`,
				ticketID, k); err != nil {
				return fmt.Errorf("insert kept path: %w", err)
			}
		}

		if narrowBarrier != nil {
			narrowBarrier()
		}

		sort.Strings(remaining)
		result = &NarrowResult{Removed: remove, Kept: keep, Remaining: remaining}
		if err := narrowComment(ctx, q, agentID, ticketID, result); err != nil {
			return err
		}
		return recordNarrowing(ctx, q, agentID, ticketID, len(remove), len(keep))
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// dedupe returns the distinct values of ps. A kept path can repeat or already
// be held, and the insert's ON CONFLICT hides that from the database but not
// from the result and comment.
func dedupe(ps []string) []string {
	seen := make(map[string]struct{}, len(ps))
	out := ps[:0]
	for _, p := range ps {
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	return out
}

// insideAny reports whether p lies strictly inside one of the directories in
// dirs: equal to one would keep what was just removed.
func insideAny(p string, dirs []string) bool {
	for _, d := range dirs {
		if strings.HasSuffix(d, "/") && p != d && strings.HasPrefix(p, d) {
			return true
		}
	}
	return false
}

func narrowComment(ctx context.Context, q db.Querier, agentID uuid.UUID, ticketID int64, r *NarrowResult) error {
	body := "Narrowed scope. Dropped: " + joinQuoted(r.Removed) + "."
	if len(r.Kept) > 0 {
		body += " Kept inside them: " + joinQuoted(r.Kept) + "."
	}
	body += " Now holds: " + joinQuoted(r.Remaining) + "."
	if _, err := insertComment(ctx, q, ticketID, AgentAuthor(agentID), body); err != nil {
		return fmt.Errorf("comment narrowing: %w", err)
	}
	return nil
}
