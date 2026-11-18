package model

import "slices"

type Status string

const (
	// StatusPreparing is a ticket a human is still shaping. Nothing claims
	// it and nothing moves it out automatically: only a human's Ready puts
	// it in the queue. Not terminal, so a ticket depending on it stays
	// blocked.
	StatusPreparing Status = "preparing"
	// StatusParked is a ticket a human is keeping but not planning to do.
	// Like Preparing, nothing claims it and only a human moves it in or out
	// -- and out only back to Preparing, so it is looked at again before
	// anything can claim it. Not terminal, unlike Cancelled: a ticket
	// depending on it stays blocked, because parking is "not now", not
	// "won't do".
	StatusParked     Status = "parked"
	StatusOpen       Status = "open"
	StatusInProgress Status = "in_progress"
	StatusBlocked    Status = "blocked"
	// StatusDiscussion is a ticket waiting on a human's answer in its
	// comments. Not claimable, and not terminal: a ticket blocked on it
	// stays blocked until the discussion ends and the work is done.
	StatusDiscussion Status = "discussion"
	// StatusReview is a ticket whose PR waits on a human's approval. Not
	// claimable, and not terminal: its change is not merged yet. Its scopes
	// stay held, so nothing else is built on paths an unmerged PR touches.
	StatusReview Status = "review"
	// StatusInMergeQueue is a finished ticket whose change waits for, or is
	// being merged by, the server's landing queue. The queue owns it: only
	// Land writes it, in the same transaction as the landing row, and no
	// person moves a ticket into it (merge-queue mode only). Not claimable,
	// not terminal, and its scopes are not held while it waits.
	StatusInMergeQueue Status = "in_merge_queue"
	StatusDone         Status = "done"
	StatusCancelled    Status = "cancelled"
)

// statuses is every status, in the order the board lists them.
var statuses = []Status{
	StatusPreparing, StatusParked, StatusOpen, StatusInProgress, StatusInMergeQueue, StatusBlocked, StatusDiscussion,
	StatusReview, StatusDone, StatusCancelled,
}

// Statuses is every status, in the order the board lists them.
func Statuses() []Status {
	return slices.Clone(statuses)
}

// IsTerminal reports whether a dependency in this status counts as
// satisfied.
//
// Cancelled counts, and that is the whole reason this is a function rather
// than an `== StatusDone` comparison at each call site. The claim query and
// the blocked predicate must agree on what "satisfied" means; if one of
// them accepts only StatusDone, cancelling a ticket silently strands
// everything that depended on it, blocked forever on work nobody will ever
// do.
func (s Status) IsTerminal() bool {
	return s == StatusDone || s == StatusCancelled
}

// Valid reports whether s is a status the schema will accept. Validating
// here rather than letting the database's CHECK constraint catch it is what
// makes a bad value a 422 instead of a 500: a constraint violation arrives
// as an opaque driver error that mapError cannot tell apart from a genuine
// internal fault.
func (s Status) Valid() bool {
	return slices.Contains(statuses, s)
}

// IsWork is whether the board lists attempts, not tickets, in this status.
func (s Status) IsWork() bool { return slices.Contains(workStatuses, s) }

var workStatuses = []Status{StatusInProgress, StatusDiscussion, StatusReview}

// WorkStatuses is the statuses the board lists attempts in, not tickets.
func WorkStatuses() []Status { return slices.Clone(workStatuses) }
