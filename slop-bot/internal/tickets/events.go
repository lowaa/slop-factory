package tickets

import (
	"time"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
)

// Cursor marks a place in a TicketingSystem's feed. It is opaque, but
// ordered: within one system, a later event's cursor is greater by string
// comparison. The zero Cursor is before the first event.
type Cursor string

// EventKind says what changed. Events are thin (see TicketingSystem): the
// consumer reads the ticket for the rest.
type EventKind string

const (
	// EventCreated is a new ticket.
	EventCreated EventKind = "created"
	// EventUpdated is an Update that changed something, status included.
	EventUpdated EventKind = "updated"
	// EventAssigned is a change of assignee.
	EventAssigned EventKind = "assigned"
	// EventCommented is a Comment. Comments a system writes itself, recording
	// another change, come with that change's event instead.
	EventCommented EventKind = "commented"
	// EventBlockersChanged is a change to the ticket's open blockers: an
	// edge added or removed, or a blocker that finished or was reopened.
	EventBlockersChanged EventKind = "blockers_changed"
	// EventUnblocked follows EventBlockersChanged when the ticket's last open
	// blocker went, so the scheduler picks it up with no polling. It is not
	// sent for a ticket that is itself done or cancelled.
	EventUnblocked EventKind = "unblocked"
)

// TicketEvent is one entry in the feed.
type TicketEvent struct {
	Cursor Cursor
	Kind   EventKind
	Ticket ident.TicketID
	// By is who made the change. For EventBlockersChanged and
	// EventUnblocked caused by a blocker's status change, it is whoever
	// changed that blocker.
	By ident.Principal
	At time.Time
}
