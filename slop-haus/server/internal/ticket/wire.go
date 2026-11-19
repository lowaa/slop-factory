package ticket

import (
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// -- Shared read/write body types --

// PathBody is a ticket's scope entry on the wire.
type PathBody struct {
	Path   string           `json:"path"`
	Source model.PathSource `json:"source"`
}

// ScopeOverlapBody is a live ticket whose scopes conflict with the ticket
// it is reported on. Path is that ticket's own scope, Other the live
// ticket's.
type ScopeOverlapBody struct {
	TicketID   int64  `json:"ticket_id"`
	Title      string `json:"title"`
	Status     Status `json:"status"`
	Path       string `json:"path"`
	Other      string `json:"other"`
	LockingOff bool   `json:"locking_off,omitempty" doc:"set when the tenant has scope locking off: this overlap would have blocked a claim, and now blocks nothing"`
	Resolve    string `json:"resolve,omitempty" enum:"migration,regenerate,merge" doc:"set when the overlap lies inside a shared path of the workspace: it blocks nothing, and this says how a clash there is settled when the base branch is merged"`
}

func overlapsToBody(overlaps []ScopeOverlap) []ScopeOverlapBody {
	out := make([]ScopeOverlapBody, len(overlaps))
	for i, o := range overlaps {
		out[i] = ScopeOverlapBody{TicketID: o.TicketID, Title: o.Title, Status: o.Status, Path: o.Path, Other: o.Other, Resolve: o.Resolve, LockingOff: o.LockingOff}
	}
	return out
}

func pathToBody(p model.Path) PathBody {
	return PathBody{Path: p.Path, Source: p.Source}
}

// PathInput is a scope on the way in. It carries no source, deliberately:
// which kind a scope is, is the server's to decide -- one supplied when
// filing or editing a ticket is "filed", and one granted by a scope
// extension (a later stage) is "extended". Letting a client state it would
// let it claim a brand-new ticket's scope was already extended, which means
// nothing.
type PathInput struct {
	Path string `json:"path"`
}

func pathFromInput(b PathInput) model.Path {
	return model.Path{Path: b.Path, Source: model.PathSourceFiled}
}

// TicketRefBody is the wire representation of a TicketRef. Status is
// there so a ticket stuck on a blocker nobody is working -- a parked one --
// says so, rather than leaving the reader to open each blocker to find out.
type TicketRefBody struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Status Status `json:"status"`
}

func ticketRefsToBody(refs []model.TicketRef) []TicketRefBody {
	body := make([]TicketRefBody, 0, len(refs))
	for _, ref := range refs {
		body = append(body, TicketRefBody{ID: ref.ID, Title: ref.Title, Status: ref.Status})
	}
	return body
}

// RoleRefBody is one of a ticket's required roles on the wire.
type RoleRefBody struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// TicketBody is the wire representation of a Ticket.
type TicketBody struct {
	ID       int64          `json:"id"`
	Title    string         `json:"title"`
	Body     string         `json:"body"`
	Priority model.Priority `json:"priority"`
	Status   Status         `json:"status"`
	Paths    []PathBody     `json:"paths"`
	// DependsOn is a plain array of ticket IDs -- the dependency graph
	// itself (adding/removing edges) isn't part of this handler's surface
	// yet, only its current shape.
	DependsOn []int64 `json:"depends_on"`
	// Blockers is DependsOn with titles, so a page listing them can say
	// which ticket each is without a request per blocker.
	Blockers []TicketRefBody `json:"blockers"`
	// CreatedByAgentID and CreatedByUserID are each nil unless that kind of
	// caller filed the ticket.
	CreatedByAgentID *string `json:"created_by_agent_id"`
	CreatedByUserID  *string `json:"created_by_user_id"`
	// The <field>_deleted flags below say the id is null because the
	// person's account was deleted, which is not the same as nobody.
	CreatedByUserDeleted bool `json:"created_by_user_id_deleted,omitempty"`
	NeedsDiscussion      bool `json:"needs_discussion"`
	// ResumeAgentID is the agent owed the ticket back while it is in
	// discussion or review, and the one with first claim until
	// AffinityUntil once a human has answered.
	ResumeAgentID *string    `json:"resume_agent_id"`
	AffinityUntil *time.Time `json:"affinity_until"`
	// RequiresApproval holds the ticket's PR for a human's approval.
	RequiresApproval bool `json:"requires_approval"`
	// ApprovedAt/ApprovedByUserID and ChangesRequestedAt are the latest
	// review's outcome; at most one is set.
	ApprovedAt         *time.Time `json:"approved_at"`
	ApprovedByUserID   *string    `json:"approved_by_user_id"`
	ApprovedByDeleted  bool       `json:"approved_by_user_id_deleted,omitempty"`
	ChangesRequestedAt *time.Time `json:"changes_requested_at"`
	// PostReviewedAt/PostReviewedByUserID record a person looking over the
	// change after it merged; both or neither are set.
	PostReviewedAt        *time.Time `json:"post_reviewed_at"`
	PostReviewedByUserID  *string    `json:"post_reviewed_by_user_id"`
	PostReviewedByDeleted bool       `json:"post_reviewed_by_user_id_deleted,omitempty"`
	// Revision counts edits to the title and body; an editor sends back
	// the one it started from as base_revision.
	Revision int `json:"revision"`
	// PRURL is the reviewable change the ticket was merged through, as
	// the agent that finished it reported it.
	PRURL *string `json:"pr_url"`
	// BaseCommit is the main commit the ticket's work started from, when
	// known: what a replay of the ticket starts from.
	BaseCommit *string `json:"base_commit"`
	// BaseRef is the commit hash or tag the ticket was filed to start from;
	// null means the default branch's head at the first claim.
	BaseRef *string `json:"base_ref"`
	// Warnings are set only on the response of the call that created the
	// ticket, edited its base, or took it out of preparing: something the
	// human should know.
	Warnings []string `json:"warnings,omitempty"`
	// RequiredRoles are the roles an agent needs to claim it: any one of
	// them, or all, as RoleMatch says. Empty means any agent may.
	RequiredRoles []RoleRefBody `json:"required_roles"`
	RoleMatch     string        `json:"role_match" enum:"any,all"`
	// Unblocks is how many unfinished tickets wait on this one, directly or
	// further downstream. Only the blocked list sets it; elsewhere it is
	// left out rather than sent as a zero that reads as "holds nothing up".
	Unblocks int `json:"unblocks,omitempty"`
	// External is set on a ticket an intake source filed. Title is what the
	// sender called it: text a stranger may have written, to be shown to a
	// person as such and never to an agent outside external content.
	External *ExternalRefBody `json:"external"`
	// NoEligibleAgent is set when it requires roles that no enabled agent
	// meets, so nothing will claim it until a person changes something.
	NoEligibleAgent bool `json:"no_eligible_agent"`
	// BlockedUntil is when a release cooldown ends and the queue re-checks
	// the ticket; null for a ticket that never had one. It can be in the
	// past (the ticket has been re-checked), so a reader compares it to now.
	// CooldownReleases counts the back-to-back cooldown releases so far and
	// CooldownSince is when that streak began; both are left out with no streak.
	BlockedUntil     *time.Time `json:"blocked_until"`
	CooldownReleases int        `json:"cooldown_releases,omitempty"`
	CooldownSince    *time.Time `json:"cooldown_since,omitempty"`
	// Attempt is set on a card of one of a ticket's several attempts, in the
	// In progress, Discussion and Review columns: the card is then the
	// attempt's, and Status, PRURL and the approval fields are its own.
	// Absent on every other card, including a ticket's one attempt's.
	Attempt *AttemptCardBody `json:"attempt,omitempty"`
	// Attempts is set on the detail read only, ordered by number; board and
	// list rows leave it out to stay lean.
	Attempts  []AttemptBody `json:"attempts,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	ClosedAt  *time.Time    `json:"closed_at"`
	// AllowedMoves and CanReady are what a human may do to the status (empty
	// on an attempt card, whose status is one attempt's): the
	// targets of a free-form status edit (the current status among them),
	// and whether Ready applies. The UI follows them rather than keeping
	// its own copy of the rules; the server still enforces them.
	AllowedMoves []Status `json:"allowed_moves"`
	CanReady     bool     `json:"can_ready"`
	// ScopeOverlaps lists the live tickets whose scopes conflict with this
	// one's, so a human can see what it would hold up before opening it.
	// Only a ticket's own detail and edit carry it, and only while it is
	// preparing or open; elsewhere it is absent.
	ScopeOverlaps []ScopeOverlapBody `json:"scope_overlaps,omitempty"`
}

func ticketToBody(t Ticket) TicketBody {
	body := TicketBody{
		ID:        t.ID,
		Title:     t.Title,
		Body:      t.Body,
		Priority:  t.Priority,
		Status:    t.Status,
		Paths:     []PathBody{},
		DependsOn: []int64{},
		CreatedAt: t.CreatedAt,
		UpdatedAt: t.UpdatedAt,
		ClosedAt:  t.ClosedAt,

		NeedsDiscussion:    t.NeedsDiscussion,
		AffinityUntil:      t.AffinityUntil,
		Revision:           t.Revision,
		PRURL:              t.PRURL,
		BaseCommit:         t.BaseCommit,
		BaseRef:            t.BaseRef,
		Warnings:           t.Warnings,
		RequiresApproval:   t.RequiresApproval,
		ApprovedAt:         t.ApprovedAt,
		ChangesRequestedAt: t.ChangesRequestedAt,
		PostReviewedAt:     t.PostReviewedAt,
		RequiredRoles:      rolesToBody(t.RequiredRoles),
		RoleMatch:          string(t.RoleMatch),
		NoEligibleAgent:    t.NoEligibleAgent,
		BlockedUntil:       t.BlockedUntil,
		CooldownReleases:   t.CooldownReleases,
		CooldownSince:      t.CooldownSince,
		Unblocks:           t.Unblocks,
		AllowedMoves:       AllowedMoves(t.Status),
		CanReady:           CanReady(t.Status),
	}
	if t.Card.Multiple() {
		body.Attempt = attemptCardToBody(&t)
		body.Status = t.Card.Status
		// A status move is the ticket's, and the card's status is one
		// attempt's: a card offers none, so no control is drawn for a move
		// the card's status does not describe.
		body.AllowedMoves, body.CanReady = []Status{}, false
	}
	body.CreatedByUserDeleted = t.CreatedByUserDeleted
	body.ApprovedByDeleted = t.ApprovedByDeleted
	body.PostReviewedByDeleted = t.PostReviewedByDeleted
	if t.PostReviewedByUserID != nil {
		id := t.PostReviewedByUserID.String()
		body.PostReviewedByUserID = &id
	}
	if t.ApprovedByUserID != nil {
		id := t.ApprovedByUserID.String()
		body.ApprovedByUserID = &id
	}
	if t.ResumeAgentID != nil {
		id := t.ResumeAgentID.String()
		body.ResumeAgentID = &id
	}
	if t.Intake != nil {
		body.External = &ExternalRefBody{SourceID: t.Intake.SourceID.String(), Key: t.Intake.Key, Title: t.Intake.Title}
	}
	for _, p := range t.Paths {
		body.Paths = append(body.Paths, pathToBody(p))
	}
	body.DependsOn = append(body.DependsOn, t.DependsOn...)
	body.Blockers = ticketRefsToBody(t.Blockers)
	if t.CreatedByAgentID != nil {
		id := t.CreatedByAgentID.String()
		body.CreatedByAgentID = &id
	}
	if t.CreatedByUserID != nil {
		id := t.CreatedByUserID.String()
		body.CreatedByUserID = &id
	}
	return body
}

// rolesToBody never returns nil, so a ticket requiring no roles is `[]` on
// the wire rather than `null`.
func rolesToBody(roles []model.RoleRef) []RoleRefBody {
	out := make([]RoleRefBody, len(roles))
	for i, r := range roles {
		out[i] = RoleRefBody{ID: r.ID.String(), Name: r.Name}
	}
	return out
}

// CommentBody is the wire representation of a Comment.
type CommentBody struct {
	ID int64 `json:"id"`
	// Exactly one of AuthorAgentID and AuthorUserID is set.
	AuthorAgentID *string `json:"author_agent_id"`
	AuthorUserID  *string `json:"author_user_id"`
	// AuthorUserDeleted is true when the author is a person whose account
	// was deleted: author_user_id is then null, and so is the agent's.
	AuthorUserDeleted bool      `json:"author_user_id_deleted,omitempty"`
	Body              string    `json:"body"`
	CreatedAt         time.Time `json:"created_at"`
	// AttemptNumber is null for a ticket-level comment, the one addressed to
	// no attempt.
	AttemptNumber *int64 `json:"attempt_number"`
}

func commentToBody(c Comment) CommentBody {
	body := CommentBody{ID: c.ID, Body: c.Body, CreatedAt: c.CreatedAt, AttemptNumber: c.AttemptNumber, AuthorUserDeleted: c.AuthorUserDeleted}
	if c.AuthorAgentID != nil {
		id := c.AuthorAgentID.String()
		body.AuthorAgentID = &id
	}
	if c.AuthorUserID != nil {
		id := c.AuthorUserID.String()
		body.AuthorUserID = &id
	}
	return body
}

func ticketBodies(tickets []Ticket) []TicketBody {
	body := make([]TicketBody, len(tickets))
	for i, t := range tickets {
		body[i] = ticketToBody(t)
	}
	return body
}
