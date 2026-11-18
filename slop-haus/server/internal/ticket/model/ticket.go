package model

import (
	"time"

	"github.com/google/uuid"
)

type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityNormal Priority = "normal"
	PriorityHigh   Priority = "high"
	PriorityUrgent Priority = "urgent"
)

func (p Priority) Valid() bool {
	switch p {
	case PriorityLow, PriorityNormal, PriorityHigh, PriorityUrgent:
		return true
	}
	return false
}

// PathSource distinguishes the scope the human filed from one an agent
// discovered it needed mid-ticket.
type PathSource string

const (
	PathSourceFiled    PathSource = "filed"
	PathSourceExtended PathSource = "extended"
)

func (p PathSource) Valid() bool {
	return p == PathSourceFiled || p == PathSourceExtended
}

// TicketRef names another ticket.
type TicketRef struct {
	ID     int64
	Title  string
	Status Status
}

type Ticket struct {
	ID        int64
	Title     string
	Body      string
	Priority  Priority
	Status    Status
	Paths     []Path
	DependsOn []int64
	// Blockers is DependsOn with each blocker's title, for a reader who
	// needs to know which ticket a number is without loading it. The two
	// are filled from the same rows, so they always name the same tickets
	// in the same order.
	Blockers []TicketRef
	// CreatedByAgentID and CreatedByUserID are each nil when the other filed
	// the ticket, and both nil for seeded data with no recorded creator. At
	// most one is ever set -- see the schema's num_nonnulls check.
	CreatedByAgentID *uuid.UUID
	CreatedByUserID  *uuid.UUID
	// CreatedByUserDeleted says CreatedByUserID is nil because the person's
	// account was deleted, not because nobody filed it. The same goes for
	// the other <Field>Deleted flags below.
	CreatedByUserDeleted bool
	// NeedsDiscussion is the human saying the body is a direction, not a
	// spec: whoever claims it plans and asks rather than builds.
	NeedsDiscussion bool
	// ResumeAgentID is the agent that last handed the ticket to a human --
	// into discussion or review -- and AffinityUntil how long it keeps first
	// claim once the human has answered. See Service.Discuss and
	// Service.Review.
	ResumeAgentID *uuid.UUID
	AffinityUntil *time.Time
	// RequiresApproval is the human saying the ticket's PR merges only once
	// they approve it. See Service.Review.
	RequiresApproval bool
	// ApprovedAt and ApprovedByUserID record the latest review's approval;
	// ChangesRequestedAt its request for changes. At most one outcome is
	// set, and going back into review clears both.
	ApprovedAt         *time.Time
	ApprovedByUserID   *uuid.UUID
	ApprovedByDeleted  bool
	ChangesRequestedAt *time.Time
	// ReviewStartedAt is when the latest review cycle began; a review
	// submitted elsewhere before it is of an earlier version of the change.
	ReviewStartedAt *time.Time
	// PostReviewedAt and PostReviewedByUserID record a person looking over
	// the ticket's change after it merged; see Service.MarkPostReviewed.
	// Both or neither are set.
	PostReviewedAt        *time.Time
	PostReviewedByUserID  *uuid.UUID
	PostReviewedByDeleted bool
	// Revision counts the edits to Title and Body; see Revision.
	Revision int
	// PRURL is the reviewable change the ticket was merged through, as the
	// agent that finished it reported it; nil if it reported none.
	PRURL *string
	// BaseCommit is the main commit the ticket's work started from, recorded at
	// its first change; nil when none was (older tickets, or the host could not
	// be read).
	BaseCommit *string
	// BaseRef is the commit hash or tag the ticket was filed to start from,
	// as its filer wrote it; BaseCommit is what it resolved to. Nil when the
	// base is the default branch's head.
	BaseRef *string
	// Warnings are things the human should know about a ticket that was
	// created, re-based or opened anyway. Not stored: only the response to
	// that call has them.
	Warnings []string
	// RequiredRoles are the roles the claiming agent needs, any or all of
	// them as RoleMatch says; none means any agent may claim it.
	RequiredRoles []RoleRef
	RoleMatch     RoleMatch
	// NoEligibleAgent is set when it requires roles no enabled agent meets.
	NoEligibleAgent bool
	// BlockedUntil, CooldownReleases and CooldownSince are the release
	// cooldown (see CooldownPolicy): when the queue next re-checks the ticket,
	// how many back-to-back cooldown releases it has had, and when that streak
	// began. BlockedUntil stays set after it passes, until the next status write.
	BlockedUntil     *time.Time
	CooldownReleases int
	CooldownSince    *time.Time
	// DoneThroughOther is set on a ticket as one of its attempts sees it
	// (loadTicketOnAttempt) when the ticket is done through another attempt
	// and this one, a live shadow, runs on. Status then shows the attempt's
	// own, so this is the only place the ticket's done is said.
	DoneThroughOther bool
	// Replay is set alongside DoneThroughOther when the attempt is a replay,
	// which is told ReplayNote in place of DoneThroughOtherNote.
	Replay bool
	// Card is set on a row of an attempt-card list: the attempt this row is the
	// card of, whose status, pr_url and approval the row's fields carry in
	// place of the promoted attempt's. Nil on a ticket card.
	Card *AttemptCard
	// Intake is set for a ticket an external system filed through the
	// intake webhook; what it sent is in ListExternal, never in Title or Body.
	Intake    *IntakeRef
	CreatedAt time.Time
	UpdatedAt time.Time
	ClosedAt  *time.Time
	// Unblocks is how many unfinished tickets wait on this one, directly or
	// further downstream. It is read only by the list sorted by it (see
	// blockingSort) and is zero on every other read.
	Unblocks int
	// MatchedAttempt is the attempt whose PR a webhook matched, set only on
	// the ticket ApprovePR, RequestChangesPR and CloseMergedPR return. Their
	// callers record it: reading the promoted attempt afterwards would name
	// another if promotion moved meanwhile.
	MatchedAttempt int
}

type Path struct {
	Path   string
	Source PathSource
}

// RoleMatch says how many of a ticket's required roles the claiming agent
// needs.
type RoleMatch string

const (
	RoleMatchAny RoleMatch = "any"
	RoleMatchAll RoleMatch = "all"
)

func (m RoleMatch) Valid() bool {
	return m == RoleMatchAny || m == RoleMatchAll
}

// RoleRef names one of a ticket's required roles.
type RoleRef struct {
	ID   uuid.UUID
	Name string
}

// IntakeRef says which source and sender key a ticket came from.
type IntakeRef struct {
	SourceID uuid.UUID
	Key      string
	// Title is what the sender called the issue. It is for a person's list
	// view alone: nothing an agent reads may carry it outside external
	// content, which is why it is not the ticket's Title.
	Title string
}

// AttemptCard is what an attempt's card adds to its ticket's.
type AttemptCard struct {
	Number int
	// Total is how many attempts the ticket has, closed ones too.
	Total  int
	Status Status
	// AwaitingChoice is a finished attempt nobody has promoted, waiting in
	// review for a person to choose it or a sibling.
	AwaitingChoice bool
	Variant        *AttemptVariant
}

// Multiple is whether the card is one of several for its ticket's attempts.
// A ticket with one attempt is shown as the ticket it is, exactly as before
// attempts, so its card carries nothing of this.
func (c *AttemptCard) Multiple() bool { return c != nil && c.Total > 1 }

// AttemptVariant names the variant version an attempt is routed to.
type AttemptVariant struct {
	VersionID uuid.UUID
	Name      string
	Version   int
	Archived  bool
}

type Lease struct {
	ID            int64
	TicketID      int64
	AgentID       uuid.UUID
	AcquiredAt    time.Time
	ExpiresAt     time.Time
	ReleasedAt    *time.Time
	ReleaseReason string
}

// ReviewOutcome is where the latest review of a ticket left it, as the agent
// that claims it next must be told.
type ReviewOutcome string

const (
	// ReviewNone is a ticket with no review outcome: never reviewed, or in
	// review now.
	ReviewNone             ReviewOutcome = ""
	ReviewApproved         ReviewOutcome = "approved"
	ReviewChangesRequested ReviewOutcome = "changes_requested"
)

// ReviewOutcome reports the latest review's outcome.
func (t *Ticket) ReviewOutcome() ReviewOutcome {
	switch {
	case t.ApprovedAt != nil:
		return ReviewApproved
	case t.ChangesRequestedAt != nil:
		return ReviewChangesRequested
	}
	return ReviewNone
}

// ReviewStage is which part of the PR review flow a ticket is in, as the
// agent that holds or reads it must be told. It combines the ticket's
// status, its latest review outcome and whether it requires approval, so
// every door onto a ticket says the same thing about what to do next.
type ReviewStage string

const (
	// ReviewStageNone is a ticket outside the review flow: ungated and
	// never reviewed, or finished.
	ReviewStageNone ReviewStage = ""
	// ReviewStageInReview is a PR waiting on a human's approval.
	ReviewStageInReview ReviewStage = "in_review"
	// ReviewStageApproved is a PR a human approved, for the agent to merge.
	ReviewStageApproved ReviewStage = "approved"
	// ReviewStageChangesRequested is a PR a human sent back for more work.
	ReviewStageChangesRequested ReviewStage = "changes_requested"
	// ReviewStageRequiresApproval is a gated ticket with no review yet: its
	// PR goes to a human, with ticket_finish, rather than being merged.
	ReviewStageRequiresApproval ReviewStage = "requires_approval"
)

// ReviewStage reports the ticket's stage in the review flow. The order of
// the checks is the rule: a finished ticket has nothing left to merge
// whatever its review says; a ticket in review is waiting whatever an
// earlier review said; and an outcome outranks the gate it came from.
func (t *Ticket) ReviewStage() ReviewStage {
	switch {
	case t.Status.IsTerminal():
		return ReviewStageNone
	case t.Status == StatusReview:
		return ReviewStageInReview
	case t.ReviewOutcome() == ReviewApproved:
		return ReviewStageApproved
	case t.ReviewOutcome() == ReviewChangesRequested:
		return ReviewStageChangesRequested
	case t.RequiresApproval:
		return ReviewStageRequiresApproval
	}
	return ReviewStageNone
}
