package ticket

import (
	"context"
	"errors"
)

// DependencyChange is a ticket after a blocker was added or removed, and
// whether that changed anything: re-adding an edge that is there, or
// removing one that isn't, records nothing.
type DependencyChange struct {
	Ticket  *Ticket
	Changed bool
}

var (
	// ErrBlockersNotEditable refuses an agent editing the blockers of a
	// ticket that is neither the one it holds nor one it filed that is still
	// waiting to be worked. Gating another agent's work, or a human's, is a
	// person's call.
	ErrBlockersNotEditable = errors.New("ticket: an agent may edit the blockers only of the ticket it holds, " +
		"or of a ticket it filed that is still preparing, open or blocked. To ask for this one, comment on it")
	// ErrBlockerNotRemovable refuses an agent removing an edge it did not
	// add. A person's edge is often a deliberate hold on work.
	ErrBlockerNotRemovable = errors.New("ticket: an agent may remove only a blocker it added itself. " +
		"To ask for this one to be lifted, comment on the ticket")
)

// AddDependency records that ticketID is waiting on dependsOnID, refusing
// any edge that would make the graph cyclic.
//
// The check matters more here than it looks: a ticket that depends
// transitively on itself can never become claimable, and the board stalls
// quietly rather than reporting anything. Removing a blocker is the only
// way back, and that first needs a human to notice and work out which edge
// to cut -- much worse than refusing the edge in the first place.
//
// A person may add a blocker to any ticket; an agent only within
// agentMayEditBlockers. Either way the change is recorded as a comment by
// whoever made it.
func (s *Service) AddDependency(ctx context.Context, by Author, ticketID, dependsOnID int64) (*DependencyChange, error) {
	if !by.valid() {
		return nil, ErrInvalidAuthor
	}
	// Rejecting the cycle is the store's, not because it is a storage
	// detail but because the check is only sound if it commits together
	// with the insert it guards -- and because the release path writes the
	// same edge and must be held to the same rule.
	return s.store.AddDependency(ctx, by, ticketID, dependsOnID)
}

// RemoveDependency lifts a blocker. A person may remove any; an agent only
// one it added itself, on a ticket within agentMayEditBlockers.
func (s *Service) RemoveDependency(ctx context.Context, by Author, ticketID, dependsOnID int64) (*DependencyChange, error) {
	if !by.valid() {
		return nil, ErrInvalidAuthor
	}
	return s.store.RemoveDependency(ctx, by, ticketID, dependsOnID)
}
