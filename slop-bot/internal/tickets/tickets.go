// Package tickets is the seam between slop-bot and whatever tracks its
// tickets: the TicketingSystem interface, the types it speaks in, and the
// rules every implementation enforces (rules.go). Local mode (package local)
// is the first implementation; slop-tickets will be the next, and both run
// the conformance suite (package conformance), so neither can drift from
// the rules.
package tickets

import (
	"context"
	"time"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
)

// TicketingSystem is what slop-bot needs from a ticketing system.
//
// Every mutating call takes the acting principal: the user, or an agent
// calling with its token. The rules in rules.go key on it. Reads take none:
// everyone may read every ticket.
//
// # The event feed
//
// Events() delivers every change, in order, each event carrying a Cursor.
// The consumer (the bot) stores the cursor of the last event it has
// handled, and passes it to Start after a restart to resume just after it.
//
// Delivery is at least once. An event can come again: after a crash
// between handling it and storing its cursor, or when Start is given an
// older cursor than the last one handled. Consumers must be idempotent.
// That is cheap here because events are thin: they name the ticket and what
// kind of change it was, not the change itself, so a consumer re-reads the
// ticket with Get and acts on its current state. Handling an event twice is
// then just reading the same ticket twice.
//
// Nothing is ever dropped. Backpressure is the consumer's pace: an
// implementation keeps its events in a durable, ordered log, and its feed
// blocks on the send until the consumer takes the event. Writers are never
// held up by a slow consumer, since they only append to the log, and the
// feed holds at most one batch in memory however far behind it is. A
// consumer that stops reading for an hour costs nothing but latency, and
// one that restarts resumes from its cursor. Buffering in memory instead
// would either grow without bound or have to drop, and dropping an
// "unblocked" event would leave a ticket silently unscheduled.
type TicketingSystem interface {
	// Start begins delivering events after the cursor (the zero Cursor
	// means from the first event). ctx bounds the setup only; the feed runs
	// until Stop. A system starts at most once.
	Start(ctx context.Context, after Cursor) error
	// Stop ends the feed and releases everything Start acquired. Events()
	// is closed once Stop returns nil. Stop is safe to call more than once,
	// and without Start.
	Stop(ctx context.Context) error
	// Events is the feed. It is the same channel before and after Start.
	Events() <-chan TicketEvent

	// Get returns the ticket with its comments and open blockers, or
	// ErrNotFound.
	Get(ctx context.Context, id ident.TicketID) (*Ticket, error)
	// List returns the tickets matching q (all of them when q is nil), in
	// creation order. Their Comments are left nil: Get a ticket to read
	// them.
	List(ctx context.Context, q *Query) ([]*Ticket, error)
	// CountOutstandingAgentCreated counts the tickets agents created that
	// are not yet done or cancelled: the autonomy budget's measure.
	CountOutstandingAgentCreated(ctx context.Context) (int, error)
	// Blockers returns ticket's blocker edges both ways, done or not.
	Blockers(ctx context.Context, ticket ident.TicketID) (*BlockerSet, error)

	// Create files a ticket. See NewTicket for what an agent may set.
	Create(ctx context.Context, by ident.Principal, nt *NewTicket) (*Ticket, error)
	// Update applies the non-nil fields of change, atomically.
	Update(ctx context.Context, by ident.Principal, id ident.TicketID, change *TicketChange) (*Ticket, error)
	// Assign sets the ticket's assignee; a blank one unassigns it.
	Assign(ctx context.Context, by ident.Principal, id ident.TicketID, assignee UserID) (*Ticket, error)
	// Comment adds a comment to the ticket.
	Comment(ctx context.Context, by ident.Principal, id ident.TicketID, body string) (*Comment, error)

	// AddBlocker records that ticket waits on blocker, refusing an edge
	// that would make the graph cyclic (ErrCycle). The check commits with
	// the insert: two opposite edges added at once can't both land.
	AddBlocker(ctx context.Context, by ident.Principal, ticket, blocker ident.TicketID) (*BlockerChange, error)
	// RemoveBlocker lifts the edge. Removing one that isn't there changes
	// nothing and is not an error.
	RemoveBlocker(ctx context.Context, by ident.Principal, ticket, blocker ident.TicketID) (*BlockerChange, error)
}

// Config is the policy an implementation enforces that the user configures.
type Config struct {
	// AgentsMaySetUrgent is the config's agents_may_set_urgent. When false,
	// only the user may change a ticket's Urgent flag.
	AgentsMaySetUrgent bool
}

// UserID names a person, as the ticketing system knows them. Eligibility
// needs a ticket to be assigned to the user slop-bot runs for.
type UserID string

// Ticket is what slop-bot needs of every ticket, whatever the system.
type Ticket struct {
	ID    ident.TicketID
	Title string
	Body  string
	// Status: see Status.
	Status Status
	// AssigneeID is blank when unassigned.
	AssigneeID UserID
	// BlockedBy lists the open blockers: those not done. A cancelled
	// blocker is open: the ticket stays blocked until a person decides.
	// Empty means nothing blocks it.
	BlockedBy []TicketRef
	// Agent is which harness, model and effort runs the ticket. Locking it
	// once an agent exists is slop-bot's state.db's job, not this one's:
	// another system may let it be edited upstream regardless.
	Agent AgentSpec
	// AutoMerge says whether the agent may merge without a person. Only the
	// user may set it.
	AutoMerge bool
	// Urgent puts the ticket in the urgent pool. UrgentReason is why, and
	// is always given when an agent set it.
	Urgent       bool
	UrgentReason string
	// CreatedBy is who filed it. For an agent, CreatedBy.Ticket is the
	// ticket that agent was working, which is also Parent.
	CreatedBy ident.Principal
	// Parent is the ticket whose agent filed this one; blank for the user's.
	Parent ident.TicketID
	// Repos are the repositories the work touches, the primary one first.
	Repos []string
	// Comments, oldest first. Only Get fills them.
	Comments  []Comment
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AgentCreated reports whether an agent filed the ticket.
func (t *Ticket) AgentCreated() bool { return t.CreatedBy.Kind == ident.Agent }

// AgentSpec is which harness runs a ticket, on which model, at which
// effort. A blank field means a default: the config's default_harness; the
// config's harnesses.<name>.model, else the harness's own default model; the
// harness's own default effort.
type AgentSpec struct {
	Harness string
	Model   string
	Effort  string
}

// TicketRef is the other end of a blocker edge.
type TicketRef struct {
	ID     ident.TicketID
	Title  string
	Status Status
}

// Comment is one comment on a ticket.
type Comment struct {
	// ID is opaque, unique within the system.
	ID        string
	Ticket    ident.TicketID
	Author    ident.Principal
	Body      string
	CreatedAt time.Time
}

// NewTicket is a ticket to file.
type NewTicket struct {
	// Title is required.
	Title string
	Body  string
	// Status is the starting status: preparing (the default when blank) or
	// open. An agent's tickets always start preparing, so another agent
	// refines them first.
	Status Status
	// Assignee is the caller's call. For an agent's ticket the bot decides:
	// the bot's user when autonomy is on, blank (into the review stream)
	// when it is off.
	Assignee UserID
	Agent    AgentSpec
	// AutoMerge may be set only by the user. An agent's ticket inherits its
	// parent's AutoMerge, so it never has more merge rights than its
	// creator; an agent asking for it is refused.
	AutoMerge bool
	// Urgent and UrgentReason: an agent may set Urgent only when
	// Config.AgentsMaySetUrgent, and must then give a reason. The reason is
	// also posted as a comment.
	Urgent       bool
	UrgentReason string
	Repos        []string
	// BlockedOn are blockers added with the ticket, owned by its creator as
	// if added with AddBlocker. A missing one files nothing.
	BlockedOn []ident.TicketID
}

// TicketChange is an edit; nil fields are left as they are.
type TicketChange struct {
	Title  *string
	Body   *string
	Status *Status
	Agent  *AgentSpec
	// AutoMerge is user-only: an agent passing it at all is refused, even on
	// its own ticket and even to the value it has.
	AutoMerge *bool
	Urgent    *UrgentChange
	Repos     *[]string
}

// UrgentChange sets or clears the Urgent flag. Setting it records Reason;
// clearing it clears the stored reason.
type UrgentChange struct {
	Urgent bool
	Reason string
}

// Query narrows List. Zero fields match everything.
type Query struct {
	// Statuses matches any of them.
	Statuses []Status
	// Assignee matches that assignee; a pointer to "" matches unassigned.
	Assignee *UserID
	// Urgent matches the flag.
	Urgent *bool
	// AgentCreated matches who filed it: true for agents, false for the user.
	AgentCreated *bool
}

// BlockerSet is a ticket's blocker edges both ways.
type BlockerSet struct {
	// BlockedBy are the tickets this one waits on, done or not.
	BlockedBy []BlockerEdge
	// Blocks are the tickets waiting on this one.
	Blocks []BlockerEdge
}

// BlockerEdge is one edge, seen from the ticket it was asked about.
type BlockerEdge struct {
	// Ticket is the other end.
	Ticket TicketRef
	// AddedBy is who added the edge; an agent may remove only its own.
	AddedBy ident.Principal
}

// BlockerChange is a ticket after AddBlocker or RemoveBlocker, and whether
// anything changed: re-adding an edge that is there, or removing one that
// isn't, records nothing.
type BlockerChange struct {
	Ticket  *Ticket
	Changed bool
}
