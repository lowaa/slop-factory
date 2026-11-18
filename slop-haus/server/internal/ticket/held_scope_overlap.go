package ticket

import (
	"context"
	"fmt"
	"slices"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

// HeldScope is one path of a scope that is held: by an agent working the
// ticket, or by the ticket waiting in review.
type HeldScope struct {
	TicketID int64
	Title    string
	Status   Status
	Path     string
}

// HeldScopeOverlap is a path someone changed outside any ticket's claim -- a
// conductor's conversation editing the owner's checkout -- that lies inside a
// scope a ticket holds.
type HeldScopeOverlap struct {
	TicketID int64
	Title    string
	Status   Status
	// Path is the changed path, Held the scope it lies inside.
	Path string
	Held string
	// Resolve is set when the overlap lies inside a shared path: a notice
	// like the rest, and how a clash there is settled.
	Resolve string
}

// HeldScopeOverlaps lists the held scopes the changed paths overlap, by
// ticket, then path. It is a notice and no more: nothing here stops anyone, as
// nothing can stop a push from the owner's machine. It applies the same
// Conflicts rule a claim does, in Go over the loaded scopes.
//
// Changed paths are file paths ("a/b.go"); one that is a directory (ending in
// "/") conflicts with whatever it contains, as a scope would.
func (s *Service) HeldScopeOverlaps(ctx context.Context, changed []string) ([]HeldScopeOverlap, error) {
	if len(changed) == 0 {
		return nil, nil
	}
	held, err := s.store.HeldScopes(ctx)
	if err != nil {
		return nil, fmt.Errorf("load held scopes: %w", err)
	}
	shared, err := s.sharedPaths(ctx)
	if err != nil {
		return nil, err
	}
	var overlaps []HeldScopeOverlap
	type pair struct {
		ticket int64
		path   string
	}
	// One line per changed path and ticket, however many of the ticket's scopes it is in.
	seen := map[pair]bool{}
	for _, h := range held {
		for _, c := range changed {
			key := pair{h.TicketID, c}
			if seen[key] {
				continue
			}
			conflict, resolve := scope.SharedClash(c, h.Path, shared)
			if !conflict {
				continue
			}
			seen[key] = true
			overlaps = append(overlaps, HeldScopeOverlap{
				TicketID: h.TicketID, Title: h.Title, Status: h.Status, Path: c, Held: h.Path, Resolve: resolve,
			})
		}
	}
	slices.SortFunc(overlaps, func(a, b HeldScopeOverlap) int {
		if a.TicketID != b.TicketID {
			return int(a.TicketID - b.TicketID)
		}
		if a.Path != b.Path {
			if a.Path < b.Path {
				return -1
			}
			return 1
		}
		return 0
	})
	return overlaps, nil
}
