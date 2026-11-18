package ticket

import (
	"strings"
)

// Status sets as SQL, for packages that join tickets or attempts. Beside
// ActiveLeaseClause, they are the one definition of each set, so a status
// added here is not missed by a store that spelled the literals itself.

// sqlList renders values as a parenthesised SQL list, e.g. ('done', 'cancelled').
// The values are package constants, never input.
func sqlList(values ...string) string {
	return "('" + strings.Join(values, "', '") + "')"
}

const (
	// SQLStatusOpen, SQLStatusInProgress and SQLStatusDone are the ticket
	// statuses as SQL literals.
	SQLStatusOpen       = "'" + string(StatusOpen) + "'"
	SQLStatusInProgress = "'" + string(StatusInProgress) + "'"
	// SQLStatusInMergeQueue is the ticket status, and the attempt status of
	// the same name, as a SQL literal.
	SQLStatusInMergeQueue = "'" + string(StatusInMergeQueue) + "'"
	SQLStatusDone         = "'" + string(StatusDone) + "'"
)

// The sets below are generated from the status types' predicates and the
// state tables, so a status added there joins the sets it belongs to.
var (
	// TerminalStatusesSQL are the ticket statuses nothing leaves: a ticket
	// depending on one of them is unblocked, and it keeps its paths.
	TerminalStatusesSQL = sqlList(statusesWhere(Status.IsTerminal)...)

	// WaitingOnPersonStatusesSQL are the ticket (or attempt) statuses that
	// wait on a human: a discussion answer or a review.
	WaitingOnPersonStatusesSQL = sqlList(statusesWhere(func(s Status) bool {
		return s == StatusDiscussion || s == StatusReview
	})...)

	// AttemptEndedStatusesSQL are the attempt statuses of an attempt that is
	// over: done, or closed without merging.
	AttemptEndedStatusesSQL = sqlList(attemptStatusesWhere(func(s AttemptStatus) bool { return !s.Live() })...)

	// AttemptFinishedStatusesSQL are the attempt statuses of an attempt that
	// has finished its own work: waiting on review, or over.
	AttemptFinishedStatusesSQL = sqlList(attemptStatusesWhere(func(s AttemptStatus) bool {
		return s == AttemptStatusReview || !s.Live()
	})...)

	// AttemptRunningStatusesSQL are the attempt statuses of an attempt
	// still doing its work: queued, running, or waiting on a discussion.
	AttemptRunningStatusesSQL = sqlList(attemptStatusesWhere(func(s AttemptStatus) bool {
		return s.Live() && s != AttemptStatusReview
	})...)

	// AttemptLiveStatusesSQL are the attempt statuses of an attempt that can
	// still be worked on, answered or decided.
	AttemptLiveStatusesSQL = sqlList(attemptStatusesWhere(AttemptStatus.Live)...)

	// queueLifecycleStatusesSQL are the ticket statuses worked out from its
	// attempts, rather than set directly by a person (see settledDirectly).
	queueLifecycleStatusesSQL = sqlList(statusesWhere(func(s Status) bool { return !settledDirectly(s) })...)

	// holdingStatusesSQL are the ticket statuses a ticket can hold its scope
	// in, or be offered for a claim in: the queue lifecycle but for blocked.
	holdingStatusesSQL = sqlList(statusesWhere(func(s Status) bool {
		return !settledDirectly(s) && s != StatusBlocked
	})...)
)
