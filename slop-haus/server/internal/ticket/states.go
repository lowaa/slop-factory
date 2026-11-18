package ticket

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// The ticket and attempt state machines, written down once. The tables are
// the specification; SQL stays the atomic enforcer (the claim lock, the
// WHERE clauses, the status column's CHECK). Tests hold the code that makes
// each transition to these tables (states_test.go), so a transition added in
// one place and not here fails the build.
//
// Staying put is always allowed and is not listed.

// ticketTransitions is every move of a ticket's status that any route makes:
// a person's, the claim lifecycle's, an agent leaving its lease, and the
// queue resolving open against blocked.
var ticketTransitions = map[Status][]Status{
	StatusPreparing:    {StatusOpen, StatusBlocked, StatusParked, StatusDone, StatusCancelled},
	StatusParked:       {StatusPreparing, StatusDone, StatusCancelled},
	StatusOpen:         {StatusBlocked, StatusInProgress, StatusInMergeQueue, StatusParked, StatusPreparing, StatusDiscussion, StatusReview, StatusDone, StatusCancelled},
	StatusInProgress:   {StatusOpen, StatusBlocked, StatusInMergeQueue, StatusPreparing, StatusDiscussion, StatusReview, StatusDone, StatusCancelled},
	StatusInMergeQueue: {StatusOpen, StatusBlocked, StatusPreparing, StatusDiscussion, StatusReview, StatusDone, StatusCancelled},
	StatusBlocked:      {StatusOpen, StatusInProgress, StatusPreparing, StatusDiscussion, StatusReview, StatusDone, StatusCancelled},
	StatusDiscussion:   {StatusOpen, StatusBlocked, StatusInProgress, StatusPreparing, StatusReview, StatusDone, StatusCancelled},
	StatusReview:       {StatusOpen, StatusBlocked, StatusPreparing, StatusDiscussion, StatusDone, StatusCancelled},
	StatusDone:         {StatusOpen, StatusBlocked, StatusInProgress, StatusPreparing, StatusDiscussion, StatusReview, StatusCancelled},
	StatusCancelled:    {StatusOpen, StatusBlocked, StatusInProgress, StatusPreparing, StatusDiscussion, StatusReview, StatusDone},
}

// humanMoves is the part of ticketTransitions the board's free-form status
// edit may make; Status.humanMove is judged by it. The rest have a route of
// their own, which decides what a free-form edit would skip:
//   - out of preparing into the queue only through Ready;
//   - out of review into the queue only through Approve or Request changes;
//   - the merge queue is entered only by finishing a ticket (Land), and left
//     by a person only to a status outside the landing: the move also takes
//     the ticket's landing row away;
//   - parked is entered only from preparing or open, and left only to
//     preparing (or finished).
var humanMoves = map[Status][]Status{
	StatusPreparing:    {StatusParked, StatusDone, StatusCancelled},
	StatusParked:       {StatusPreparing, StatusDone, StatusCancelled},
	StatusOpen:         {StatusBlocked, StatusInProgress, StatusParked, StatusPreparing, StatusDiscussion, StatusReview, StatusDone, StatusCancelled},
	StatusInProgress:   {StatusOpen, StatusBlocked, StatusPreparing, StatusDiscussion, StatusReview, StatusDone, StatusCancelled},
	StatusInMergeQueue: {StatusOpen, StatusBlocked, StatusPreparing, StatusDone, StatusCancelled},
	StatusBlocked:      {StatusOpen, StatusInProgress, StatusPreparing, StatusDiscussion, StatusReview, StatusDone, StatusCancelled},
	StatusDiscussion:   {StatusOpen, StatusBlocked, StatusInProgress, StatusPreparing, StatusReview, StatusDone, StatusCancelled},
	StatusReview:       {StatusPreparing, StatusDiscussion, StatusDone, StatusCancelled},
	StatusDone:         {StatusOpen, StatusBlocked, StatusInProgress, StatusPreparing, StatusDiscussion, StatusReview, StatusCancelled},
	StatusCancelled:    {StatusOpen, StatusBlocked, StatusInProgress, StatusPreparing, StatusDiscussion, StatusReview, StatusDone},
}

// AttemptStatus is the status of one attempt: one way of doing a ticket. The
// ticket's own status is worked out from its attempts (derivedStatus).
type AttemptStatus string

const (
	// AttemptStatusQueued is waiting to be claimed. The ticket's preparing, parked,
	// open and blocked all read as this on the attempt.
	AttemptStatusQueued     AttemptStatus = "queued"
	AttemptStatusInProgress AttemptStatus = "in_progress"
	AttemptStatusDiscussion AttemptStatus = "discussion"
	AttemptStatusReview     AttemptStatus = "review"
	// AttemptStatusInMergeQueue is finished and handed to the server's landing
	// queue. It stays so while the server holds it to merge it: the holder is
	// the server agent, not a worker.
	AttemptStatusInMergeQueue AttemptStatus = "in_merge_queue"
	// AttemptStatusDone is merged.
	AttemptStatusDone AttemptStatus = "done"
	// AttemptStatusClosed is over without merging: cancelled with its ticket, or
	// closed by a person.
	AttemptStatusClosed AttemptStatus = "closed"
)

var attemptStatuses = []AttemptStatus{
	AttemptStatusQueued, AttemptStatusInProgress, AttemptStatusInMergeQueue, AttemptStatusDiscussion, AttemptStatusReview, AttemptStatusDone, AttemptStatusClosed,
}

// Valid reports whether s is a status the attempts table accepts.
func (s AttemptStatus) Valid() bool { return slices.Contains(attemptStatuses, s) }

// Live reports whether the attempt can still be worked on, answered or
// decided: everything but done and closed.
func (s AttemptStatus) Live() bool { return s != AttemptStatusDone && s != AttemptStatusClosed }

// AttemptStatusFor is the status an attempt takes when its ticket is set to
// status: the Go side of the attempt_status_for_ticket SQL function.
func AttemptStatusFor(status Status) AttemptStatus {
	switch status {
	case StatusInProgress:
		return AttemptStatusInProgress
	case StatusDiscussion:
		return AttemptStatusDiscussion
	case StatusReview:
		return AttemptStatusReview
	case StatusInMergeQueue:
		return AttemptStatusInMergeQueue
	case StatusDone:
		return AttemptStatusDone
	case StatusCancelled:
		return AttemptStatusClosed
	}
	return AttemptStatusQueued
}

// attemptTransitions is every move of an attempt's status: the ticket's moves
// as the attempt sees them, a lease ending (in progress back to queued), the
// landing queue taking a finished attempt and handing it back, and a person
// closing it.
var attemptTransitions = map[AttemptStatus][]AttemptStatus{
	AttemptStatusQueued:       {AttemptStatusInProgress, AttemptStatusInMergeQueue, AttemptStatusDiscussion, AttemptStatusReview, AttemptStatusDone, AttemptStatusClosed},
	AttemptStatusInProgress:   {AttemptStatusQueued, AttemptStatusInMergeQueue, AttemptStatusDiscussion, AttemptStatusReview, AttemptStatusDone, AttemptStatusClosed},
	AttemptStatusInMergeQueue: {AttemptStatusQueued, AttemptStatusDiscussion, AttemptStatusReview, AttemptStatusDone, AttemptStatusClosed},
	AttemptStatusDiscussion:   {AttemptStatusQueued, AttemptStatusInProgress, AttemptStatusReview, AttemptStatusDone, AttemptStatusClosed},
	AttemptStatusReview:       {AttemptStatusQueued, AttemptStatusDiscussion, AttemptStatusDone, AttemptStatusClosed},
	AttemptStatusDone:         {AttemptStatusQueued, AttemptStatusInProgress, AttemptStatusDiscussion, AttemptStatusReview, AttemptStatusClosed},
	AttemptStatusClosed:       {AttemptStatusQueued, AttemptStatusInProgress, AttemptStatusDiscussion, AttemptStatusReview, AttemptStatusDone},
}

// CanTransition reports whether any route moves an attempt from one status to
// another.
func (s AttemptStatus) CanTransition(to AttemptStatus) bool {
	return s == to || slices.Contains(attemptTransitions[s], to)
}

// statusesWhere is the statuses, in board order, that keep holds.
func statusesWhere(keep func(Status) bool) []string {
	var out []string
	for _, s := range model.Statuses() {
		if keep(s) {
			out = append(out, string(s))
		}
	}
	return out
}

// attemptStatusesWhere is the attempt statuses that keep holds.
func attemptStatusesWhere(keep func(AttemptStatus) bool) []string {
	var out []string
	for _, s := range attemptStatuses {
		if keep(s) {
			out = append(out, string(s))
		}
	}
	return out
}

// StateDiagram renders a transition table as a mermaid state diagram, states
// and targets in order, for the docs that show the machine (a test holds the
// docs to it).
func StateDiagram[S ~string](order []S, table map[S][]S) string {
	var b strings.Builder
	b.WriteString("```mermaid\nstateDiagram-v2\n")
	for _, from := range order {
		for _, to := range order {
			if slices.Contains(table[from], to) {
				fmt.Fprintf(&b, "    %s --> %s\n", from, to)
			}
		}
	}
	b.WriteString("```\n")
	return b.String()
}

// TicketStateDiagram is the ticket lifecycle as a mermaid diagram.
func TicketStateDiagram() string { return StateDiagram(model.Statuses(), ticketTransitions) }

// AttemptStateDiagram is the attempt lifecycle as a mermaid diagram.
func AttemptStateDiagram() string { return StateDiagram(attemptStatuses, attemptTransitions) }

// WithDiagram returns doc with the block between the markers
// `<!-- state-diagram:name -->` and `<!-- /state-diagram:name -->` replaced
// by diagram, and whether doc has the markers.
func WithDiagram(doc, name, diagram string) (string, bool) {
	open, closing := "<!-- state-diagram:"+name+" -->\n", "<!-- /state-diagram:"+name+" -->"
	start := strings.Index(doc, open)
	if start < 0 {
		return doc, false
	}
	start += len(open)
	end := strings.Index(doc[start:], closing)
	if end < 0 {
		return doc, false
	}
	return doc[:start] + diagram + doc[start+end:], true
}
