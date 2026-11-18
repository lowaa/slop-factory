// Package pendingaction holds the human-only actions a console agent
// proposes for a signed-in person to approve.
//
// A console is the agent behind the person's own interactive session. Its
// credential is a file any process of the same OS user can read, so it must
// never be enough to take a human action: it can only leave a proposal here.
// Nothing on a ticket changes until a person approves, with their own
// session, which is a later layer than this package -- there is deliberately
// no approve here for an agent-reachable caller to find.
package pendingaction

import (
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Kind is what a proposal asks the approver to do.
type Kind string

// aboutAnAttempt is whether a proposal of this kind is about the ticket's
// work -- an answer to its discussion, a decision on its review -- rather
// than the ticket itself (its readiness, status or text).
func (k Kind) aboutAnAttempt() bool {
	return k == KindDiscussionAnswer || k == KindReviewDecision
}

const (
	// KindDiscussionAnswer posts the payload's body as the approver's
	// comment on a ticket in discussion, which re-queues it.
	KindDiscussionAnswer Kind = "discussion_answer"
	// KindReady moves a preparing ticket into the queue.
	KindReady Kind = "ready"
	// KindStatus parks or cancels a ticket, with the payload's body as the
	// reason.
	KindStatus Kind = "status"
	// KindReviewDecision approves a ticket in review, or sends it back with
	// the payload's body as what to address.
	KindReviewDecision Kind = "review_decision"
	// KindEdit changes a ticket's title, body, paths or priority, as the
	// person's own edit would.
	KindEdit Kind = "edit"
	// KindManifestEdit replaces the tenant's workspace manifest. It is about
	// the tenant, not a ticket, and only an owner decides it (see kindSpecs).
	KindManifestEdit Kind = "manifest_edit"
	// KindCloseMerged closes a ticket as done because its change was merged
	// outside slop-haus (ticket.Service.CloseAsMerged). Payload.Body is why.
	KindCloseMerged Kind = "close_merged"
)

// Kinds is every kind of proposal.
var Kinds = []Kind{KindDiscussionAnswer, KindReady, KindStatus, KindReviewDecision, KindEdit, KindManifestEdit, KindCloseMerged}

func (k Kind) valid() bool { return slices.Contains(Kinds, k) }

// State is where a proposal stands.
type State string

const (
	StatePending   State = "pending"
	StateApproved  State = "approved"
	StateRejected  State = "rejected"
	StateWithdrawn State = "withdrawn"
	StateExpired   State = "expired"
	// StateVoided is a proposal whose proposer stopped being an enabled
	// console before it was decided.
	StateVoided State = "voided"
	// StateStale is a proposal refused at approval because its ticket had
	// moved on since.
	StateStale State = "stale"
	// StateFailed is a proposal the ticket rules refused when applied.
	StateFailed State = "failed"
)

func (s State) valid() bool {
	switch s {
	case StatePending, StateApproved, StateRejected, StateWithdrawn, StateExpired, StateVoided, StateStale, StateFailed:
		return true
	}
	return false
}

// Decision is a review proposal's verdict.
type Decision string

const (
	DecisionApprove        Decision = "approve"
	DecisionRequestChanges Decision = "request_changes"
)

// Payload is the exact action to apply on approval, validated and
// normalised when proposed. One shape for every kind, so the stored JSON
// has one schema; each kind uses only its own fields and a proposal that
// sets another's is refused.
type Payload struct {
	// Body is the comment to post (a discussion answer), the reason (a
	// status move or a close as merged) or what to address (a review's requested changes).
	Body string `json:"body,omitempty"`
	// Status is where a status proposal moves the ticket: parked or cancelled.
	Status ticket.Status `json:"status,omitempty"`
	// Decision is a review proposal's verdict.
	Decision Decision `json:"decision,omitempty"`
	// Edit is an edit proposal's change.
	Edit *EditPayload `json:"edit,omitempty"`
	// ManifestEdit is a manifest_edit proposal's change.
	ManifestEdit *ManifestEditPayload `json:"manifest_edit,omitempty"`
}

// EditPayload is what an edit proposal changes. A nil field is left alone.
type EditPayload struct {
	// BaseRevision is the ticket revision the proposer read its title and
	// body at. It must be the ticket's revision when proposing; approving
	// refuses a ticket edited since.
	BaseRevision int     `json:"base_revision"`
	Title        *string `json:"title,omitempty"`
	Body         *string `json:"body,omitempty"`
	// Paths replace the ticket's filed paths, normalised when proposed. A
	// ticket is never left with none.
	Paths    []string        `json:"paths,omitempty"`
	Priority *model.Priority `json:"priority,omitempty"`
	// BasePriority and BasePaths are the ticket's, when proposed, for the
	// fields the edit replaces. Unlike the title and body they have no
	// revision, so approving compares them instead: a ticket whose paths or
	// priority were changed since is refused rather than overwritten.
	BasePriority *model.Priority `json:"base_priority,omitempty"`
	BasePaths    []string        `json:"base_paths,omitempty"`
	// Open also moves the ticket out of preparing when the edit is applied,
	// in the same transaction. It is one proposal, not an edit and a ready,
	// because the two would make each other stale: see ErrBlocked.
	Open bool `json:"open,omitempty"`
}

// Fields names the fields the edit changes, in the words of the board's
// edit: title, body, paths, priority and open.
func (e *EditPayload) Fields() []string {
	var out []string
	if e.Title != nil {
		out = append(out, "title")
	}
	if e.Body != nil {
		out = append(out, "body")
	}
	if e.Paths != nil {
		out = append(out, "paths")
	}
	if e.Priority != nil {
		out = append(out, "priority")
	}
	if e.Open {
		out = append(out, "open")
	}
	return out
}

// Basis is the ticket as it stood when a proposal was made: the fields that
// change what approving it would do. An approval compares the ticket now to
// this, and refuses when it differs.
type Basis struct {
	Status          ticket.Status
	Revision        int
	PRURL           *string
	ReviewStartedAt *time.Time
}

// Action is one proposal.
type Action struct {
	ID uuid.UUID
	// TicketID is the ticket the proposal is about; nil for a kind whose
	// subject is the tenant (see KindSpec).
	TicketID *int64
	// AgentID is the proposer.
	AgentID uuid.UUID
	Kind    Kind
	Payload Payload
	// Attempt is the attempt an answer or a decision is about; nil for the
	// kinds about the ticket itself.
	Attempt *int
	Basis   Basis
	// State is effective at the time it was read: a proposal past its
	// expiry reads as expired whether or not a sweep has recorded it yet.
	State     State
	Outcome   string
	CreatedAt time.Time
	ExpiresAt time.Time
	DecidedAt *time.Time
	// DecidedByUserID is the person who approved or rejected it.
	DecidedByUserID *uuid.UUID
	// DecidedByDeleted says DecidedByUserID is nil because that person's
	// account was deleted.
	DecidedByDeleted bool
}

// OutcomeAutoApproved is the outcome of a proposal applied at once by its
// owner's auto-approve setting, which a clicked approval leaves empty.
const OutcomeAutoApproved = "auto-approved"

// OutcomeStewardApproved is the outcome of a proposal the steward applied for
// its owner under the steward's live mode.
const OutcomeStewardApproved = "steward-approved"

// AutoApproved says the proposal was applied by a standing choice (its
// owner's setting, or the steward under the owner's settings) rather than a
// click.
func (a *Action) AutoApproved() bool {
	return a.State == StateApproved && (a.Outcome == OutcomeAutoApproved || a.Outcome == OutcomeStewardApproved)
}

// attemptRef is the attempt the proposal is about, as stored.
func (a *Action) attemptRef() *ticket.AttemptRef {
	if a.Attempt == nil || a.TicketID == nil {
		return nil
	}
	return &ticket.AttemptRef{TicketID: *a.TicketID, Number: *a.Attempt}
}

// ticketID is the ticket of a proposal known to be about one: the kinds'
// ticket-only code, which Spec keeps ticket-less proposals out of.
func (a *Action) ticketID() int64 {
	if a.TicketID == nil {
		return 0
	}
	return *a.TicketID
}

// Errors are the caller's to map to a response; each is a refusal that
// leaves nothing created or changed.
var (
	ErrNotFound = errors.New("pending action: not found")
	// ErrTicketNotFound is a proposal naming a ticket the caller's tenant
	// does not have.
	ErrTicketNotFound = errors.New("pending action: ticket not found")
	// ErrNotApprover is a decision by a person whose role the kind does not
	// allow to decide it.
	ErrNotApprover = errors.New("pending action: your role cannot decide this kind of proposal")
	// ErrNotConsole is a proposer that is not an enabled console agent.
	ErrNotConsole = errors.New("pending action: agent is not an enabled console")
	// ErrTicketHeld is a proposal on a ticket an agent holds a lease on:
	// acting on it would take the work away from a running agent.
	ErrTicketHeld = errors.New("pending action: ticket is held by an agent")
	// ErrInvalid wraps every proposal the rules refuse, with the reason.
	ErrInvalid = errors.New("pending action: invalid proposal")
	// ErrTooManyPending and ErrRateLimited are the flood limits.
	ErrTooManyPending = errors.New("pending action: too many pending proposals")
	ErrRateLimited    = errors.New("pending action: proposing too fast")
	// ErrNotPending is an attempt to change a proposal already decided.
	ErrNotPending = errors.New("pending action: no longer pending")
	// ErrStale, ErrVoided and ErrRefused are approvals that were refused
	// and recorded: the ticket moved on since, the proposer stopped being a
	// console, or the ticket's own rules said no. The proposal is settled
	// in that state and nothing on the ticket changed.
	ErrStale   = errors.New("pending action: the ticket has changed since it was proposed")
	ErrVoided  = errors.New("pending action: the proposer is no longer an enabled console")
	ErrRefused = errors.New("pending action: the ticket's rules refused it")
	// ErrBlocked is an approval that must wait for another decision. Unlike
	// the three above it settles nothing: the proposal stays pending, and can
	// be approved once what it waits for is decided.
	ErrBlocked = errors.New("pending action: decide the pending edit for this ticket first")
	// ErrNotConfigured is a Service built without what deciding needs.
	ErrNotConfigured = errors.New("pending action: approving is not configured")
)

const (
	// DefaultTTL is how long a proposal waits for a person.
	DefaultTTL = 7 * 24 * time.Hour
	// DefaultMaxPendingPerAgent bounds how much one agent can put in the
	// person's queue at once.
	DefaultMaxPendingPerAgent = 50
	// DefaultRatePerMinute bounds how fast one agent can propose.
	DefaultRatePerMinute = 30
	// MaxTextBytes bounds a proposal's body: the person reads all of it.
	MaxTextBytes = 20_000

	rateWindow = time.Minute
)

// Config bounds proposals. A zero field takes its default.
type Config struct {
	TTL                time.Duration
	MaxPendingPerAgent int
	RatePerMinute      int
}

func (c Config) withDefaults() Config {
	if c.TTL <= 0 {
		c.TTL = DefaultTTL
	}
	if c.MaxPendingPerAgent <= 0 {
		c.MaxPendingPerAgent = DefaultMaxPendingPerAgent
	}
	if c.RatePerMinute <= 0 {
		c.RatePerMinute = DefaultRatePerMinute
	}
	return c
}

// ProposeParams is a console's request.
type ProposeParams struct {
	AgentID uuid.UUID
	// TicketID is the ticket for a ticket kind and must be zero for the rest.
	TicketID int64
	Kind     Kind
	Payload  Payload
	// Attempt is the attempt the console means, for an answer or a decision.
	// Without it the ticket's one live attempt, or its promoted one, stands;
	// with several live and none named the proposal is refused with the list.
	Attempt *int
}

// ListFilter narrows List. Zero fields match everything.
type ListFilter struct {
	AgentID  *uuid.UUID
	TicketID *int64
	Kind     *Kind
	// States are matched against the effective state.
	States []State
	// IDs limits the result to these proposals; nil means every proposal and
	// an empty, non-nil slice none.
	IDs []uuid.UUID
	// Ticket limits the result to proposals about tickets it matches.
	Ticket *ticket.ListFilter
	// Limit caps the rows, newest first; zero takes DefaultListLimit.
	Limit int
	// Oldest lists oldest first, the order to work through a queue in.
	Oldest bool
	// After resumes a walk of the list after the proposal the cursor names.
	After *PageCursor
}

const (
	DefaultListLimit = 100
	// DefaultPageSize and MaxPageSize are ViewsPage's, which the Approvals
	// list walks a page at a time.
	DefaultPageSize = 50
	MaxPageSize     = 100
)

func blank(s string) bool { return strings.TrimSpace(s) == "" }
