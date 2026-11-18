// Package ticket is slop-haus's work queue: the tickets agents claim, the
// path scopes that keep two agents out of the same files, and the comments
// that keep decisions visible to everyone.
//
// Service holds every rule. The REST handlers here, and the MCP tools and
// hook endpoints that land in later stages, are all thin callers of it --
// a rule reimplemented slightly differently in one transport is how the
// "two agents can't hold overlapping paths" guarantee stops being a
// property of the system and becomes a property of whichever door the
// agent came through.
package ticket

import (
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Status, Ticket and Lease are used throughout the server, so they stay
// reachable here; everything else of the model is imported from it directly.
type (
	Status = model.Status
	Ticket = model.Ticket
	Lease  = model.Lease
)

const (
	StatusPreparing    = model.StatusPreparing
	StatusParked       = model.StatusParked
	StatusOpen         = model.StatusOpen
	StatusInProgress   = model.StatusInProgress
	StatusBlocked      = model.StatusBlocked
	StatusDiscussion   = model.StatusDiscussion
	StatusReview       = model.StatusReview
	StatusInMergeQueue = model.StatusInMergeQueue
	StatusDone         = model.StatusDone
	StatusCancelled    = model.StatusCancelled
)

// humanMove checks that the board's free-form status edit may take a
// ticket from s to next, returning why not if it may not. The rule is the
// humanMoves table; the sentinel names the part of it that refused.
//
// Two statuses have their own route back into the queue, which decides
// between open and blocked by the ticket's dependencies -- and, for review,
// records the outcome the agent is told when it claims the ticket again:
//   - a Preparing ticket enters the queue only through Ready;
//   - a ticket in Review returns to it only through Approve or Request
//     changes.
//
// A free-form edit to open would skip that, and could leave a ticket with
// unfinished dependencies showing as Open while nothing can claim it.
// Leaving either for a status outside the queue is allowed: discarding a
// draft, finding it already done (a PR the human merged themselves), or
// sending a reviewed ticket back to be reshaped doesn't put anything in the
// queue.
//
// Parked has rules both ways:
//   - only a ticket nobody holds or is waiting on -- preparing or open --
//     is parked. A held one (in progress, review) is released or sent back
//     to preparing first, so parking never takes work from under an agent;
//     a blocked or discussion one is waiting on something that parking
//     would leave dangling;
//   - unparking goes to preparing, never straight into the queue, so a
//     human looks at the ticket again before anyone can claim it. Discarding
//     it (cancelled) or finding it done is allowed, as from preparing.
func humanMove(s, next Status) error {
	if next == s || slices.Contains(humanMoves[s], next) {
		return nil
	}
	switch {
	case next == StatusInMergeQueue:
		return ErrEnterMergeQueue
	case s == StatusInMergeQueue:
		return ErrLeaveMergeQueue
	case next == StatusParked:
		return ErrNotParkable
	case s == StatusPreparing:
		return ErrLeavePreparing
	case s == StatusParked:
		return ErrLeaveParked
	}
	return ErrLeaveReview
}

// CheckHumanMove reports why a person may not move a ticket from one status
// to another, as the board's status edit would refuse it. Exported so that
// something proposing such a move on a person's behalf can refuse an
// impossible one up front, by the rule that will apply when they approve.
func CheckHumanMove(from, to Status) error {
	return humanMove(from, to)
}

// settledDirectly reports whether a person's move to s is the ticket's own
// status, set as asked: preparing, parked and cancelled are outside the queue
// lifecycle, whose status is worked out from the ticket's attempts (see
// derivedStatus).
func settledDirectly(s Status) bool {
	return s == StatusPreparing || s == StatusParked || s == StatusCancelled
}

// revokesLease reports whether a human moving a held ticket to s takes it
// away from the agent working it: anything but in_progress does. A lease
// left live under any other status would let the agent carry on, and
// finish, work the human has just taken back -- and under open, let a
// second agent claim the ticket while the first still holds it.
func revokesLease(s Status) bool {
	return s != StatusInProgress
}

// statusLabel is the status as the board names it, so an agent is told about a
// move in the words the human who made it saw.
func statusLabel(s Status) string {
	switch s {
	case StatusPreparing:
		return "Preparing"
	case StatusParked:
		return "Parked"
	case StatusOpen:
		return "Open"
	case StatusInProgress:
		return "In progress"
	case StatusInMergeQueue:
		return "In merge queue"
	case StatusBlocked:
		return "Blocked"
	case StatusDiscussion:
		return "Discussion"
	case StatusReview:
		return "Review"
	case StatusDone:
		return "Done"
	case StatusCancelled:
		return "Cancelled"
	}
	return string(s)
}

// validAtCreation reports whether a ticket may be filed in s. Anything past
// open is a state the claim lifecycle reaches, not one a ticket starts in.
func validAtCreation(s Status) bool {
	return s == StatusPreparing || s == StatusOpen
}

// DoneThroughOtherNote is what an agent holding a live shadow of a done ticket
// is told wherever it reads the ticket, so it does not take "done" for its own
// attempt and stop: its attempt, and the PR and CI that go with it, run to
// their own end.
const DoneThroughOtherNote = "THIS TICKET IS DONE THROUGH ANOTHER ATTEMPT, and yours is a live shadow of it. " +
	"That is expected: finish your attempt as normal -- open your PR from your own branch (change_open targets the ticket's base " +
	"branch for you; it cannot merge to the base branch), then ticket_finish as for any attempt. " +
	"Don't stop or release because the ticket says done."

// ReplayNote is said, in DoneThroughOtherNote's place, to an agent holding a
// replay: a re-run of a finished ticket, whose real work already landed. What
// it does must stay inside its own branch.
const ReplayNote = "THIS IS A REPLAY of a ticket that is already done or parked: you are re-running it from its original base commit, " +
	"on the ticket as it was first given, as an experiment. Any real change it had is already merged and the ticket's status does not change. " +
	"Do not deploy, push to main or touch any live system. Where the ticket needs one of those, describe what you would do and stop there. " +
	"Otherwise finish as normal -- open your PR from your own branch (change_open targets the ticket's base branch for you; " +
	"it cannot merge), then ticket_finish. Don't stop because the ticket says done."

// Revision is one edit to a ticket's title or description: the full old and
// new text of each field it changed. A field it left alone is nil on both
// sides. Full text rather than a diff, so every revision is readable on its
// own; diffs are computed when one is displayed.
type Revision struct {
	ID       int64
	TicketID int64
	// Number is the ticket's Revision after this edit: 1 for the first.
	Number int
	Editor Author
	// EditorDeleted says Editor.UserID is nil because the editor's account
	// was deleted.
	EditorDeleted bool
	OldTitle      *string
	NewTitle      *string
	OldBody       *string
	NewBody       *string
	CreatedAt     time.Time
}

// AgentContext is a handoff: what an agent leaves on a ticket when it stops
// for a discussion, written so that any agent -- itself after a /clear, or
// another one -- can carry on from it.
type AgentContext struct {
	ID        int64
	TicketID  int64
	AgentID   uuid.UUID
	Body      string
	CreatedAt time.Time
	// AttemptNumber is the attempt whose agent wrote the handoff.
	AttemptNumber int64
}

// DefaultResumeAffinity is how long the agent that handed a ticket to a human --
// with a question, or a PR to review -- keeps first claim on it once the
// human has answered. Long enough for an agent that is
// finishing something else to come back for it; short enough that an agent
// that has gone quiet does not hold the answer hostage -- after this, any
// agent can take the ticket, working from the handoff. Only a worker is
// given the window: a console or conductor does not poll for work.
// Service.ResumeAffinity is the window in force, which WithResumeAffinity
// sets.
const DefaultResumeAffinity = 30 * time.Minute

type Comment struct {
	ID int64
	// Exactly one of AuthorAgentID and AuthorUserID is set -- see the
	// schema's ticket_comments num_nonnulls check. There is no third,
	// authorless case for a comment: every comment is either an agent's or the signed-in user's.
	AuthorAgentID *uuid.UUID
	AuthorUserID  *uuid.UUID
	// AuthorUserDeleted replaces AuthorUserID once the person's account is
	// deleted: the comment still has a person author, just not a named one.
	AuthorUserDeleted bool
	// OnBehalfOfUserID is the person an owner-bound agent (a conductor, or
	// a console with a live full-access grant) commented for; only with
	// AuthorAgentID.
	OnBehalfOfUserID *uuid.UUID
	Body             string
	CreatedAt        time.Time
	// AttemptNumber is the attempt the comment is addressed to; nil is the
	// ticket's own, for every attempt.
	AttemptNumber *int64
}

// Author identifies who is adding a comment: exactly one of AgentID and
// UserID must be set. It exists so authorship is decided by the caller
// passing a value it can only construct one honest way -- AgentAuthor or
// UserAuthor -- rather than by a lone *uuid.UUID whose meaning ("this is
// the agent" vs. "this is nobody, i.e. the human") depended on which of two
// separate parameters used to carry it.
type Author struct {
	AgentID *uuid.UUID
	UserID  *uuid.UUID
	// onBehalf is set only by Service.AgentAddDependency/AgentRemoveDependency
	// after the agent resolved to an owner-bound session, and is unexported
	// so no other package can claim the direct blocker powers by building one.
	onBehalf *OwnerBound
	// speaksFor is set only by Service.AgentComment, for a comment of an
	// owner-bound session: the person the comment is recorded as made for,
	// and so delivered to the ticket's holder as that person's. Kept apart
	// from onBehalf so a blocker edit's bookkeeping comment is never
	// delivered as the person's words.
	speaksFor *OwnerBound
}

// AgentAuthor is a comment or edit authored by the agent id.
func AgentAuthor(id uuid.UUID) Author { return Author{AgentID: &id} }

// UserAuthor is a comment or edit authored by the signed-in user id.
func UserAuthor(id uuid.UUID) Author { return Author{UserID: &id} }

// valid reports whether exactly one of AgentID and UserID is set, matching
// the schema's ticket_comments CHECK.
func (a Author) valid() bool {
	return (a.AgentID != nil) != (a.UserID != nil)
}

var (
	ErrNotFound = errors.New("ticket: not found")
	// ErrDependencyCycle is returned when adding a dependency would make a
	// ticket unreachable from every claimable state -- it would depend,
	// transitively, on itself.
	ErrDependencyCycle = errors.New("ticket: dependency would create a cycle")
	ErrEmptyTitle      = errors.New("ticket: title must not be empty")
	// ErrInvalidEnum covers an unrecognised status, priority or path
	// source. Client input, so a 422 rather than a 500.
	ErrInvalidEnum  = errors.New("ticket: invalid value")
	ErrEmptyComment = errors.New("ticket: comment body must not be empty")
	// ErrInvalidSearch is a text filter over MaxSearchTextLen or
	// MaxSearchTerms, or one that is not text Postgres can compare (a NUL,
	// invalid UTF-8) and would otherwise fail the query as a 500.
	ErrInvalidSearch = errors.New("ticket: invalid search text")
	// ErrInvalidPage is a page size or cursor the list cannot honour.
	ErrInvalidPage = errors.New("ticket: invalid page")
	// ErrInvalidAuthor is returned when a comment's Author does not set
	// exactly one of AgentID/UserID -- never trusted from a request body,
	// so this is a caller (wiring) bug, but still worth a named error rather
	// than an assertion.
	ErrInvalidAuthor = errors.New("ticket: a comment must have exactly one author")
	// ErrMultipleCreators is returned when a ticket is filed with both an
	// agent and a user creator; the schema allows at most one.
	ErrMultipleCreators = errors.New("ticket: a ticket can only be created by one of an agent or a user")
	// ErrEmptyQuestion and ErrEmptyHandoff guard a discussion: without a
	// question the human has nothing to answer, and without a handoff
	// whoever picks the ticket up next starts from nothing.
	ErrEmptyQuestion = errors.New("ticket: a discussion needs a question for the human")
	ErrEmptyHandoff  = errors.New("ticket: a discussion needs a handoff for whoever picks the ticket up next")
	// ErrNotPreparing is returned by Ready for a ticket that is not in
	// Preparing -- a stale board, or a second click.
	ErrNotPreparing = errors.New("ticket: ticket is not preparing")
	// ErrLeavePreparing refuses a free-form status edit that would take a
	// Preparing ticket into the queue. See Status.humanMove.
	ErrLeavePreparing = errors.New("ticket: a preparing ticket enters the queue only by being opened")
	// ErrNotParkable refuses parking a ticket that is not preparing or
	// open. See Status.humanMove.
	ErrNotParkable = errors.New("ticket: only a preparing or open ticket can be parked -- " +
		"release a held ticket, or send it back to preparing, first")
	// ErrLeaveParked refuses a free-form status edit that would take a
	// parked ticket anywhere but back to preparing (or to done/cancelled).
	// See Status.humanMove.
	ErrLeaveParked = errors.New("ticket: a parked ticket is unparked back to preparing, not into the queue")
	// ErrLeaveReview refuses a free-form status edit that would take a
	// ticket in Review back into the queue. See Status.humanMove.
	ErrLeaveReview = errors.New("ticket: a ticket in review returns to the queue only by being approved or having changes requested")
	// ErrEnterMergeQueue refuses a free-form status edit into the merge queue:
	// only finishing a ticket enqueues it. See Status.humanMove.
	ErrEnterMergeQueue = errors.New("ticket: a ticket enters the merge queue only by its agent finishing it")
	// ErrLeaveMergeQueue refuses a free-form status edit that would take a
	// ticket in the merge queue anywhere a landing cannot be handed back to.
	ErrLeaveMergeQueue = errors.New("ticket: a ticket in the merge queue can only be moved to preparing, open, blocked, done or cancelled")
	// ErrNotInReview is returned by Approve and RequestChanges for a ticket
	// that is not in Review -- a stale board, or a second click.
	ErrNotInReview = errors.New("ticket: ticket is not in review")
	// ErrNotDone is returned by MarkPostReviewed for a ticket that is not
	// done: there is no merged change to have looked at.
	ErrNotDone = errors.New("ticket: only a done ticket can be marked reviewed")
	// ErrAlreadyPostReviewed is returned by MarkPostReviewed for a ticket
	// someone has already marked -- a stale board, or a second click. The
	// first mark stands, so the record says who actually looked first.
	ErrAlreadyPostReviewed = errors.New("ticket: ticket is already marked reviewed")
	// ErrNotApproved refuses ticket_finish on a ticket that requires a
	// human's approval of its PR and has none. The server is the only
	// enforcement there is -- the repo host has no branch protection -- so
	// its text is the instruction an agent that skipped review needs.
	ErrNotApproved = errors.New("ticket: this ticket requires a human to approve its PR before it is merged and closed. " +
		"Do not merge it: open the PR if you have not, and call ticket_finish with its URL and a handoff")
	// ErrNeedsDiscussion refuses ticket_finish on a held
	// ticket whose needs_discussion flag is still set: the human asked for a
	// plan, not a merged change, and an agent that built anyway must be
	// stopped before its work is closed or sent to review as if it were
	// asked for. ticket_discuss clears the flag, so there is always a way
	// out -- and the text names it, since the agent acts on it.
	ErrNeedsDiscussion = errors.New("ticket: this ticket NEEDS DISCUSSION, so it cannot be closed or put into review yet. " +
		"Do not merge anything for it: post your plan and questions with ticket_discuss first")
	// ErrPRURLRequired guards Review: a review with no PR has nothing for
	// the human to look at.
	ErrPRURLRequired = errors.New("ticket: a review needs the URL of the PR to review")
	// ErrEmptyReason guards Prepare: an agent pulling its ticket out of the
	// queue must say why, or the human finds it back in Preparing with no
	// idea what to shape.
	ErrEmptyReason = errors.New("ticket: moving a ticket to preparing needs a reason for the human")
	// ErrProcessingDisabled is returned by Claim when the human has
	// switched ticket processing off. Distinct from "nothing claimable":
	// an agent told the queue is empty concludes there is no work and
	// stops, whereas this one is something it should report rather than
	// quietly absorb.
	ErrProcessingDisabled = errors.New("ticket: ticket processing is disabled")
	// ErrEmptyEdit is an edit that names neither a title nor a body.
	ErrEmptyEdit = errors.New("ticket: an edit needs a new title or body")
	// ErrStaleRevision refuses an edit made against an older revision than
	// the ticket's: someone else edited it after the editor was opened, and
	// saving would silently overwrite their change.
	ErrStaleRevision = errors.New("ticket: the ticket was edited since you opened it")
	// ErrEmptyBatch is a bulk action naming no tickets.
	ErrEmptyBatch = errors.New("ticket: a bulk action needs at least one ticket")
	// ErrBatchTooLarge is a bulk action naming more tickets than the cap;
	// see WithBulkMax.
	ErrBatchTooLarge = errors.New("ticket: too many tickets for one bulk action")
	// ErrInvalidPRURL refuses a pr_url that is not a plain https link. It
	// ends up as an href on the board, so anything else is refused rather
	// than cleaned up.
	ErrInvalidPRURL = errors.New("ticket: pr_url must be an https URL")
)
