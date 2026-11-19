package ticket

import (
	"context"
	"fmt"
	"slices"
)

// ScopeLocking is whether the tenant's overlapping scopes block.
func (s *Service) ScopeLocking(ctx context.Context) (bool, error) {
	return s.store.ScopeLocking(ctx)
}

// QueueBlockage reports whether the queue is waiting on path scopes held
// elsewhere, for the board to say so. Read-only, and not gated by the
// processing switch for the same reason Peek is not: it reports the true
// state, and the store itself answers nothing while processing is off.
func (s *Service) QueueBlockage(ctx context.Context) (*QueueBlockage, error) {
	return s.store.QueueBlockage(ctx)
}

// Graph returns the blocking graph with everything needed to draw it
// already worked out -- layers, transitive blockers, and how much each
// ticket is holding up.
//
// It is computed here rather than in the browser so there is one
// definition of "blocked", shared with the claim path, instead of a second
// one in TypeScript free to drift from it.
func (s *Service) Graph(ctx context.Context, query *GraphQuery) (*Graph, error) {
	if err := query.Filter.Validate(); err != nil {
		return nil, err
	}
	if query.Limit < 0 || query.Limit > MaxGraphLimit {
		return nil, fmt.Errorf("%w: limit must be 1 to %d", ErrInvalidPage, MaxGraphLimit)
	}
	in, err := s.store.LoadGraph(ctx, query)
	if err != nil {
		return nil, err
	}
	graph := buildGraph(*in)
	return &graph, nil
}

// Blockers reports what is holding one ticket up, as a graph of that ticket
// and everything unfinished upstream of it. It reads only those tickets, not
// the board, so it costs what the ticket's own blockers do.
func (s *Service) Blockers(ctx context.Context, ticketID int64) (*Graph, error) {
	in, err := s.store.LoadGraph(ctx, &GraphQuery{Filter: ListFilter{IDs: []int64{ticketID}}})
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(in.Tickets, func(t Ticket) bool { return t.ID == ticketID }) {
		return nil, fmt.Errorf("%w: #%d", ErrNotFound, ticketID)
	}
	// LoadGraph marks the blockers it pulled in as hidden by a filter; here
	// they are the answer, not an excuse for one.
	in.Hidden = nil
	graph := buildGraph(*in)
	return &graph, nil
}

// BlockerCandidates lists the tickets that may be added as blockers of
// this one. The rule is the graph's, so a picker built on this offers only
// edges AddDependency will accept -- rather than letting a human find the
// cycle rule by being refused.
func (s *Service) BlockerCandidates(ctx context.Context, ticketID int64) ([]BlockerCandidate, error) {
	in, err := s.store.LoadGraph(ctx, &GraphQuery{})
	if err != nil {
		return nil, err
	}
	candidates, found := blockerCandidates(*in, ticketID)
	if !found {
		return nil, fmt.Errorf("%w: #%d", ErrNotFound, ticketID)
	}
	return candidates, nil
}
