package ticket

import (
	"context"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

// ScopeNarrowing is what to give up of a held scope so that it names the
// files a change touches rather than the directories it was claimed under.
// changed are workspace-relative file paths.
//
// A declared file stays, and so does a path inside a shared path (it blocks
// nothing anyway). Each declared directory is dropped for the changed files
// under it, which is also how a file the change creates is named. A
// directory the change touches nothing under is dropped with nothing kept.
// Nothing is returned when that would leave the ticket with no path: a
// change that touches none of its scope is not evidence of what to release.
func ScopeNarrowing(held, changed []string, shared *scope.SharedPaths) (remove, keep []string) {
	var kept, left int
	for _, p := range held {
		if _, isShared := shared.Covers(p); isShared || !strings.HasSuffix(p, "/") {
			left++
			continue
		}
		remove = append(remove, p)
		for _, f := range changed {
			if f != p && strings.HasPrefix(f, p) {
				keep = append(keep, f)
				kept++
			}
		}
	}
	if len(remove) == 0 || left+kept == 0 {
		return nil, nil
	}
	slices.Sort(keep)
	return remove, slices.Compact(keep)
}

// NarrowToChange gives up the directories of the ticket the agent holds that
// its change only touches some files of, as ScopeNarrowing says. It is for the
// moments a worker hands the ticket on, never mid-run. Nil, with nothing
// changed, when there is nothing to narrow. The agent's ticket is narrowed
// as ticket_scope_narrow does, so the comment on it reads the same.
func (s *Service) NarrowToChange(ctx context.Context, agentID uuid.UUID, ticketID int64, changed []string, shared *scope.SharedPaths) (*NarrowResult, error) {
	lease, err := s.store.LeaseFor(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if lease.TicketID != ticketID {
		return nil, nil // the lease moved on while the change was read
	}
	t, err := s.store.GetTicket(ctx, lease.TicketID)
	if err != nil {
		return nil, err
	}
	held := make([]string, len(t.Paths))
	for i, p := range t.Paths {
		held[i] = p.Path
	}
	remove, keep := ScopeNarrowing(held, changed, shared)
	if len(remove) == 0 {
		return nil, nil
	}
	return s.store.NarrowScope(ctx, agentID, remove, keep)
}
