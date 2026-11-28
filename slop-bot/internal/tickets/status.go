package tickets

import "slices"

// Status is where a ticket is in its life. Being blocked is not a status:
// it is a ticket with open blockers (Ticket.BlockedBy), whatever its
// status, because blockers gate the work and not the refinement.
type Status string

const (
	// StatusPreparing is being refined: an agent reads the code, clarifies
	// the scope and chooses an approach. Every ticket starts here unless the
	// user files it straight into open.
	StatusPreparing Status = "preparing"
	// StatusDiscussion is waiting on a person to resolve a risky decision,
	// raised during refinement or mid-work.
	StatusDiscussion Status = "discussion"
	// StatusOpen is refined and ready to be worked once nothing blocks it.
	StatusOpen Status = "open"
	// StatusInProgress is being worked.
	StatusInProgress Status = "in_progress"
	// StatusMisconfigured has an agent spec that failed validation, so it
	// isn't scheduled until the spec is fixed. It is a ticket's status, not
	// an agent's, because usually no agent exists yet.
	StatusMisconfigured Status = "misconfigured"
	// StatusDone is finished: the agent, or the user, judged its goal met.
	StatusDone Status = "done"
	// StatusCancelled is dropped. A ticket blocked on it stays blocked.
	StatusCancelled Status = "cancelled"
)

// transitions is every move a ticket's status may make. Moving to the
// status it already has is not a transition and is always allowed.
//
//   - done is reached only from in_progress: only work finishes a ticket. A
//     person closing one by hand cancels it.
//   - into in_progress: from open, the work starting, and from discussion,
//     the work resuming once a mid-work decision is resolved. Either way
//     not while the ticket has open blockers (see CheckChange).
//   - back to preparing: when the work needs refining again (from open or
//     discussion), or on reopening a finished ticket.
//   - out of done and cancelled is a reopen, which only the user may do
//     (see CheckChange).
var transitions = map[Status][]Status{
	StatusPreparing:     {StatusDiscussion, StatusOpen, StatusMisconfigured, StatusCancelled},
	StatusDiscussion:    {StatusPreparing, StatusOpen, StatusInProgress, StatusCancelled},
	StatusOpen:          {StatusPreparing, StatusDiscussion, StatusInProgress, StatusMisconfigured, StatusCancelled},
	StatusInProgress:    {StatusDiscussion, StatusOpen, StatusDone, StatusCancelled},
	StatusMisconfigured: {StatusPreparing, StatusOpen, StatusCancelled},
	StatusDone:          {StatusPreparing, StatusOpen},
	StatusCancelled:     {StatusPreparing, StatusOpen},
}

// Statuses is every status, in lifecycle order.
func Statuses() []Status {
	return []Status{
		StatusPreparing, StatusDiscussion, StatusOpen, StatusInProgress,
		StatusMisconfigured, StatusDone, StatusCancelled,
	}
}

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	_, ok := transitions[s]
	return ok
}

// Terminal reports whether s ends a ticket: done or cancelled.
func (s Status) Terminal() bool { return s == StatusDone || s == StatusCancelled }

// CanTransition reports whether a ticket may move from one status to the
// other, whoever asks.
func CanTransition(from, to Status) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	return from == to || slices.Contains(transitions[from], to)
}
