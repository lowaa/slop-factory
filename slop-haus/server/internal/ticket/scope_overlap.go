package ticket

import (
	"context"
	"fmt"
	"slices"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

// ScopeOverlap is a live ticket whose scopes conflict with another's, and
// the first pair of paths that does.
type ScopeOverlap struct {
	TicketID int64
	Title    string
	Status   Status
	// Path is the asked-about ticket's own path, Other the live ticket's.
	Path  string
	Other string
	// Resolve is set when every conflict between the two lies inside a shared
	// path: the overlap is only a notice, and Resolve says how a clash there
	// is settled when the base branch is merged. Empty for a real conflict.
	Resolve string
}

// liveStatuses are the statuses whose scopes can keep another ticket from
// being claimed or worked: everything but the ones a human has finished,
// cancelled or set aside.
var liveStatuses = []Status{
	StatusPreparing, StatusOpen, StatusInProgress, StatusInMergeQueue, StatusBlocked, StatusDiscussion, StatusReview,
}

// ScopeOverlaps lists the live tickets whose scopes conflict with ticket
// id's, oldest first. It is feedback for whoever filed or edited the scopes:
// a conflict is sometimes real, so nothing here refuses a ticket.
//
// It runs the same Conflicts rule a claim does, in Go over the loaded
// scopes, rather than a second statement of the rule in SQL.
func (s *Service) ScopeOverlaps(ctx context.Context, id int64) ([]ScopeOverlap, error) {
	t, err := s.store.GetTicket(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.ScopeOverlapsWith(ctx, id, t.Paths)
}

// ScopeOverlapsWith is ScopeOverlaps as it would be if ticket id's paths
// were paths: what an edit of its scopes would overlap, before it is made.
func (s *Service) ScopeOverlapsWith(ctx context.Context, id int64, paths []model.Path) ([]ScopeOverlap, error) {
	shared, err := s.sharedPaths(ctx)
	if err != nil {
		return nil, err
	}
	live, err := s.store.ListTickets(ctx, &ListFilter{Statuses: liveStatuses})
	if err != nil {
		return nil, err
	}
	overlaps := []ScopeOverlap{}
	for i := range live {
		other := &live[i]
		if other.ID == id {
			continue
		}
		if mine, theirs, resolve, ok := firstScopeConflict(paths, other.Paths, shared); ok {
			overlaps = append(overlaps, ScopeOverlap{
				TicketID: other.ID, Title: other.Title, Status: other.Status, Path: mine, Other: theirs, Resolve: resolve,
			})
		}
	}
	slices.SortFunc(overlaps, func(a, b ScopeOverlap) int { return int(a.TicketID - b.TicketID) })
	return overlaps, nil
}

// firstScopeConflict is the first pair of paths that really conflict, or
// failing one, the first that conflicts only inside a shared path, with its
// resolve kind.
func firstScopeConflict(mine, theirs []model.Path, shared *scope.SharedPaths) (string, string, string, bool) {
	var sharedMine, sharedTheirs, sharedResolve string
	found := false
	for _, m := range mine {
		for _, o := range theirs {
			conflict, resolve := scope.SharedClash(m.Path, o.Path, shared)
			switch {
			case !conflict:
			case resolve == "":
				return m.Path, o.Path, "", true
			case !found:
				sharedMine, sharedTheirs, sharedResolve, found = m.Path, o.Path, resolve, true
			}
		}
	}
	return sharedMine, sharedTheirs, sharedResolve, found
}

// SharedOverlapsHint is said instead when every overlap lies inside a shared
// path: there is nothing to narrow.
const SharedOverlapsHint = "Every overlap lies inside a shared path of the workspace: none blocks a claim, " +
	"and a clash there is resolved when the base branch is merged (see the resolve kind)."

// ScopeOverlapsHint is said to whoever sees overlaps, so the cost of a broad
// scope is in front of them at the moment they can narrow it.
const ScopeOverlapsHint = "Overlapping scopes keep these tickets from being claimed in parallel; " +
	"narrower paths (leaf directories or files) avoid that. Real conflicts are fine. " +
	"An overlap marked shared lies inside a shared path of the workspace: it blocks nothing, " +
	"and the clash is resolved when the base branch is merged (see the resolve kind)."

// sharedPaths is the manifest's shared paths, or none without a workspace.
func (s *Service) sharedPaths(ctx context.Context) (*scope.SharedPaths, error) {
	if s.workspace == nil {
		return nil, nil
	}
	pl, err := s.workspace.Place(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("load shared paths: %w", err)
	}
	return scope.NewSharedPaths(pl.All), nil
}
