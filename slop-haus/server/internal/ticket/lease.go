package ticket

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Leases live in this package rather than an internal/lease of their own
// because claiming *is* a ticket state transition: it writes tickets,
// ticket_paths, ticket_deps and leases in one transaction. Splitting them
// would give the tickets table two owners, and the invariant this design
// exists to protect spans both.

// DefaultLeaseTTL is generous on purpose. The heartbeat rides on any tool
// call rather than a timer, and an agent thinking hard or running a long
// test suite legitimately makes no calls for a while. A short TTL produces
// spurious expiries, which are worse than slow ones: a spurious expiry
// hands live work to a second agent.
const DefaultLeaseTTL = 45 * time.Minute

// IntegrationLockName is the only lock agents can take (and, as a prefix, its
// per-branch variants, see LockNameFor). The locks table is
// keyed by name so a second one can be added later, but a lock whose
// lifetime derives from a ticket lease only makes sense for a holder that
// has one.
const IntegrationLockName = "integration"

// DefaultLockOfferBound is how long the head of the queue has to take a lock
// that has come free for it before the place passes on. A waiter is told the
// moment it can take the lock, so this only has to outlast one tool call; it
// is longer than that because an agent working to an older briefing polls on
// a timer of its own, and losing its place for being a minute late would
// bring the starvation the queue is there to end.
const DefaultLockOfferBound = 5 * time.Minute

// ClaimResult is what ticket_next returns. Exactly one of Ticket and
// Blocked is set.
//
// Blocked is a real outcome, not an error. An agent that gets a bare
// "nothing for you" when the queue is actually deadlocked will simply
// stop, silently; returning *why* lets it wait for the scope and lets the
// board show it.
// ClaimOptions is what a claim asks for beyond the agent.
type ClaimOptions struct {
	// Only requests one specific ticket.
	Only *int64
	// Harness is the harness a headless run declares it will use, checked
	// against the agent's variant when the agent has no current session to
	// say so. "" for none.
	Harness string
	// HarnessVersion and BundleHash are what the claiming run reports about
	// itself, recorded with the lease. "" for not reported, which is what an
	// older supervisor sends.
	HarnessVersion string
	BundleHash     string
}

type ClaimResult struct {
	Ticket  *Ticket
	Blocked *Blocked
	// Attempt is the attempt the claim's lease is on; nil for a probe (Peek),
	// which grants nothing.
	Attempt *AttemptRef
	// Unpromoted is whether the granted attempt is not the ticket's promoted
	// one (a shadow): it stays on the ticket's base commit and never takes in
	// main. False for a probe.
	Unpromoted bool
	// Session is the harness and session an earlier run left on the granted
	// attempt, nil when none reported one.
	Session *AttemptSession
	// Branch is the attempt's branch, made at the claim; nil when Attempt is.
	Branch *AttemptBranch
	// SharedOverlaps are the held scopes the granted ticket overlaps only
	// inside shared paths, which did not keep it from being granted.
	SharedOverlaps []SharedOverlap
}

// SharedOverlap is a held scope that overlaps another ticket's only inside a
// shared path (see SharedPathRule). It never refuses anything; it tells the
// worker how the clash is resolved when it merges main.
type SharedOverlap struct {
	// TicketID is the ticket holding the other scope.
	TicketID int64
	// Path is what the two scopes share: the narrower of them.
	Path    string
	Resolve string
}

// Blocked names the candidate that could not be granted and what stood in
// its way.
type Blocked struct {
	TicketID            int64
	ConflictingTicketID int64
	ConflictingPath     string
	HolderAgentID       uuid.UUID
}

// ExtendResult reports whether a scope extension was granted, and if not,
// what conflicted. Denial is an expected outcome the agent has to be able
// to act on, not a failure.
type ExtendResult struct {
	Granted bool
	Denied  *Blocked
	// SharedOverlaps are the held scopes the extension overlaps only inside
	// shared paths, which did not stop it.
	SharedOverlaps []SharedOverlap
}

// NarrowResult is what a scope narrowing left the ticket with.
type NarrowResult struct {
	Removed []string
	Kept    []string
	// Remaining is every path the ticket holds afterwards.
	Remaining []string
}

var (
	// ErrNarrowUnknownPath refuses removing a path the ticket does not hold:
	// narrowing only gives up scope, so it has nothing to say about a path
	// that was never there.
	ErrNarrowUnknownPath = errors.New("ticket: that path is not in the ticket's scope")
	// ErrNarrowAdds refuses a kept path that is not inside a removed one.
	// Any other new path could overlap a ticket another agent holds, which
	// is what ticket_scope_extend checks and narrowing does not.
	ErrNarrowAdds = errors.New("ticket: narrowing cannot add a path; keep only sub-paths of the ones removed, or use ticket_scope_extend")
	// ErrNarrowEmpty refuses leaving the ticket with no scope: a ticket with
	// none conflicts with nothing, so emptying it would not narrow it but
	// declare it exclusive of nothing.
	ErrNarrowEmpty = errors.New("ticket: narrowing would leave the ticket with no paths")
	// ErrNarrowWithSiblings refuses narrowing while more than one attempt of
	// the ticket is live. Paths belong to the ticket, so one attempt cannot
	// drop a path a sibling still needs.
	ErrNarrowWithSiblings = errors.New("ticket: scope cannot be narrowed while more than one attempt of the ticket is live: its paths are held for every attempt, so one cannot drop a path a sibling still needs")

	ErrNoLease = errors.New("ticket: agent holds no active lease")
	// ErrConsoleUnnamedClaim is a console asking the queue for work.
	ErrConsoleUnnamedClaim = errors.New("consoles claim a named ticket; the queue is for workers")
	// ErrAlreadyHolding is returned when an agent that already holds a
	// ticket asks for another. One lease per agent is what makes "extend
	// the holder's lease" unambiguous.
	ErrAlreadyHolding = errors.New("ticket: agent already holds a ticket")
	// ErrLeaseLost means the lease was released or expired underneath the
	// caller -- its ticket may already belong to another agent. Never a
	// silent no-op: the agent has been working on something it no longer
	// holds and needs to stop and check in.
	ErrLeaseLost = errors.New("ticket: lease no longer held")
	// ErrLockHeld matches a *LockHeldError.
	ErrLockHeld   = errors.New("ticket: lock is held by another agent")
	ErrNotHolder  = errors.New("ticket: agent does not hold this ticket")
	ErrNoSuchLock = errors.New("ticket: lock not held")
	// ErrNotQueued is a drop of an agent that is not waiting for the lock.
	ErrNotQueued = errors.New("ticket: agent is not waiting for the lock")
	// ErrUnknownLock is a lock name that is none of the tenant's locks.
	ErrUnknownLock = errors.New("ticket: no such integration lock")
)

// LockHeldError refuses a lock another agent holds, or has reserved for the
// head of its queue, and says where the caller now stands. Agents were
// inventing how long they had waited and how many were ahead of them; the
// server states the real figures so they have no reason to estimate any.
type LockHeldError struct {
	// Holder is nil when nobody holds the lock but it is reserved for the
	// first agent in the queue -- see LockQueueEntry.OfferedAt.
	Holder *LockHolder
	// Position is the caller's place in the queue, 1 being next.
	Position int
	// MovedUpBy is why the caller stands ahead of an agent that queued
	// before it, if it does -- see LockQueueEntry.MovedUpBy.
	MovedUpBy string
	// Ahead is the agents in front of the caller, nearest the head first.
	Ahead []LockQueueEntry
}

func (e *LockHeldError) Error() string {
	var b strings.Builder
	b.WriteString(ErrLockHeld.Error())
	if e.Holder != nil {
		fmt.Fprintf(&b, ": held by agent %q on ticket #%d (%q) since %s, held for %s so far.",
			e.Holder.AgentName, e.Holder.TicketID, e.Holder.TicketTitle,
			e.Holder.AcquiredAt.UTC().Format(time.RFC3339), e.Holder.Held.Round(time.Second))
	} else {
		b.WriteString(": it is free, but reserved for the agent at the head of the queue, who has not taken it yet.")
	}
	fmt.Fprintf(&b, " You are number %d in the queue", e.Position)
	switch e.MovedUpBy {
	case LockMovedByPriority:
		b.WriteString(" (moved up by your ticket's priority)")
	case LockMovedByAging:
		b.WriteString(" (moved up by how long you have waited)")
	}
	if len(e.Ahead) == 0 {
		b.WriteString(", next in line.")
	} else {
		b.WriteString(", behind:")
		for _, a := range e.Ahead {
			fmt.Fprintf(&b, " agent %q on ticket #%d (queued %s, waiting %s)",
				a.AgentName, a.TicketID, a.QueuedAt.UTC().Format(time.RFC3339), a.Waited.Round(time.Second))
			if a.OfferedAt != nil {
				fmt.Fprintf(&b, " [offered the lock at %s]", a.OfferedAt.UTC().Format(time.RFC3339))
			}
			b.WriteString(";")
		}
	}
	b.WriteString(" The lock goes to the head of the queue when it is released. " +
		"You are queued: end your turn rather than polling, and you are resumed when it is yours. " +
		"The holder is normally waiting on CI, not stuck -- judge how long it has been from these server timestamps.")
	return b.String()
}

func (e *LockHeldError) Is(target error) bool { return target == ErrLockHeld }

// LockHolder is whoever has the lock. Held is measured by the database
// clock, the one AcquiredAt came from.
type LockHolder struct {
	AgentID     uuid.UUID
	AgentName   string
	TicketID    int64
	TicketTitle string
	// PRURL is the holder's ticket's change, when it has one recorded.
	PRURL      string
	AcquiredAt time.Time
	Held       time.Duration
	// LastHeartbeatAt is when the holder's lease was last renewed, and
	// SinceHeartbeat how long ago that was by the database clock. Dead is
	// SinceHeartbeat reaching the tenant's dead-holder setting: measured from
	// the heartbeat, not AcquiredAt, so a long CI wait that keeps
	// heartbeating is never dead.
	LastHeartbeatAt time.Time
	SinceHeartbeat  time.Duration
	Dead            bool
}

const (
	LockMovedByPriority = "priority"
	LockMovedByAging    = "aging"
)

// LockQueueEntry is one agent waiting for the lock. Waited is measured by
// the database clock.
type LockQueueEntry struct {
	AgentID     uuid.UUID
	AgentName   string
	TicketID    int64
	TicketTitle string
	// Priority is the ticket's, read when the queue was listed.
	Priority model.Priority
	QueuedAt time.Time
	Waited   time.Duration
	// MovedUpBy says why the entry stands ahead of an agent that queued
	// before it: LockMovedByPriority, LockMovedByAging, or empty when it
	// stands where it queued.
	MovedUpBy string
	// OfferedAt is when the lock first stood free with this entry at the
	// head: the first right to take it, which lapses after LockOfferBound.
	OfferedAt *time.Time
}

// LockState is the lock and the queue behind it, for the board.
type LockState struct {
	Holder *LockHolder
	Queue  []LockQueueEntry
}

// ErrScopeConflict matches a *ScopeConflictError.
var ErrScopeConflict = errors.New("ticket: paths overlap a scope another ticket holds")

// ScopeConflictError refuses a person's edit that would leave a ticket
// holding its scope -- leased, or in review -- over another ticket's held
// scope: a paths edit on a holding ticket, or a move into review. Two
// tickets holding the same files is what scopes exist to rule out. Blocked
// names the scope that stood in the way.
type ScopeConflictError struct {
	Blocked Blocked
	// InBatch is a conflict with a ticket the same bulk move takes into
	// review, rather than one already holding its scope.
	InBatch bool
}

func (e *ScopeConflictError) Error() string {
	if e.InBatch {
		return fmt.Sprintf("%v: #%d's paths would overlap %q of #%d, which this same move takes into review -- "+
			"move them separately, or choose paths clear of each other",
			ErrScopeConflict, e.Blocked.TicketID, e.Blocked.ConflictingPath, e.Blocked.ConflictingTicketID)
	}
	return fmt.Sprintf("%v: #%d's paths would overlap %q, held by #%d -- "+
		"wait for that ticket to finish, take it back from its holder, or choose paths clear of it",
		ErrScopeConflict, e.Blocked.TicketID, e.Blocked.ConflictingPath, e.Blocked.ConflictingTicketID)
}

func (e *ScopeConflictError) Is(target error) bool { return target == ErrScopeConflict }

// ErrLeaseRevoked matches a *LeaseRevokedError.
var ErrLeaseRevoked = errors.New("ticket: lease revoked by a human")

// LeaseRevokedError tells an agent that a human took its ticket away by
// moving it to Status. It is delivered once, by the agent's next heartbeat
// -- which every tool call makes. Whether that call then fails with it or
// carries it alongside its result is the caller's to decide.
//
// It is deliberately not ErrNoLease. A lease that lapsed is routine and the
// tools shrug it off; this one is an instruction, and an agent that read it
// as "no lease" would carry on working, and try to finish, a ticket the
// human has withdrawn.
type LeaseRevokedError struct {
	TicketID int64
	// Attempt is the attempt the lease was for.
	Attempt int
	// Status is where the human moved the ticket, or revokedToClosed when
	// they closed this attempt and moved nothing.
	Status Status
}

func (e *LeaseRevokedError) Error() string {
	if e.Status == revokedToClosed {
		return fmt.Sprintf("attempt %d of ticket #%d was closed by a human -- stop working on it. "+
			"You no longer hold it: don't finish, commit or merge anything more for it. "+
			"Stop now and report this; the supervisor hands you no more work on it.",
			e.Attempt, e.TicketID)
	}
	moved := "moved to " + statusLabel(e.Status)
	if e.Status == StatusCancelled {
		moved = "cancelled"
	}
	return fmt.Sprintf("ticket #%d was %s by a human -- stop working on it. "+
		"You no longer hold it: don't finish, commit or merge anything more for it. "+
		"Stop now and report this; the supervisor hands you no more work on it.",
		e.TicketID, moved)
}

func (e *LeaseRevokedError) Is(target error) bool { return target == ErrLeaseRevoked }
