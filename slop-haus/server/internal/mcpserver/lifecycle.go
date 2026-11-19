package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

type checkinInput struct{}

type checkinOutput struct {
	toolNotes
	Agent      string   `json:"agent"`
	Roles      []string `json:"roles,omitempty"`
	HeldTicket *int64   `json:"held_ticket,omitempty"`
	// Guidance is what to do next, and for an agent holding nothing why it
	// holds nothing if it was taken away.
	Guidance string `json:"guidance,omitempty"`
}

func (h *Handler) checkin(ctx context.Context, agentID uuid.UUID, _ checkinInput) (*mcp.CallToolResult, checkinOutput, error) {
	name, err := h.agents.Name(ctx, agentID)
	if err != nil {
		return nil, checkinOutput{}, err
	}
	roles, err := h.roles.RoleNames(ctx, agentID)
	if err != nil {
		return nil, checkinOutput{}, err
	}
	out := checkinOutput{Agent: name, Roles: roles}
	who := name
	if len(roles) > 0 {
		who = fmt.Sprintf("%s (roles: %s)", name, strings.Join(roles, ", "))
	}

	if held, err := h.tickets.LeaseFor(ctx, agentID); err == nil {
		out.HeldTicket = &held.TicketID
		out.Guidance = fmt.Sprintf("You are %s, holding ticket #%d.", who, held.TicketID)
		return text("%s", out.Guidance), out, nil
	} else if !errors.Is(err, ticket.ErrNoLease) {
		return nil, checkinOutput{}, err
	}
	nothing := fmt.Sprintf("You are %s, holding nothing.", who)
	if line := h.revocationContext(ctx, agentID); line != "" {
		nothing += " " + line
	}
	if caller, err := authn.FromContext(ctx); err == nil && isConsoleIdentity(caller) {
		out.Guidance = nothing + " You claim only a ticket you are told to take: ticket_next with its id."
		return text("%s", out.Guidance), out, nil
	}
	out.Guidance = nothing + " Call ticket_next to take work."
	return text("%s", out.Guidance), out, nil
}

type ticketNextInput struct {
	TicketID *int64 `json:"ticket_id,omitempty" jsonschema:"claim this specific ticket instead of the next one"`
}

type ticketNextOutput struct {
	toolNotes
	// heldView is the granted attempt's branch and the guidance on the ticket.
	heldView
	Ticket *ticketRef `json:"ticket,omitempty"`
	// Blocked explains a deadlocked queue. An agent that gets a bare
	// "nothing for you" when every remaining ticket conflicts will stop
	// silently; this tells it to wait for the scope to free instead.
	Blocked string `json:"blocked,omitempty"`
	// Disabled is set when the human has switched ticket processing off.
	// Kept separate from Blocked and from an empty queue because it is the
	// one "no work" answer that is not about the queue at all -- an agent
	// that conflated them would report a deadlock or an empty board when
	// neither is true.
	Disabled string `json:"disabled,omitempty"`
	// Message is the answer when there is no ticket to grant: a requested one
	// that was not granted, or an empty queue.
	Message string `json:"message,omitempty"`
	// Refused is set when the agent is in a variant but its session runs
	// another harness, or is a console asking for work it was not named, so
	// it is given none: an answer about the agent, like Disabled, so not a
	// failure to retry.
	Refused string `json:"refused,omitempty"`
	// WorkspaceMismatch is set when the agent's workspace is wrong in a
	// repo the granted ticket needs, and the agent is to give the ticket
	// back. The ticket is still granted: it is the agent's to release,
	// with what it found.
	WorkspaceMismatch string `json:"workspace_mismatch,omitempty"`
	// SharedOverlaps are held scopes the granted ticket overlaps only inside
	// shared paths: nothing blocked, but a clash there is resolved as each says.
	SharedOverlaps []sharedOverlapRef `json:"shared_overlaps,omitempty"`
	// LockingOffOverlaps are held scopes the granted ticket overlaps that
	// would have blocked it, with the tenant's scope locking off.
	LockingOffOverlaps []lockingOffOverlapRef `json:"locking_off_overlaps,omitempty"`
}

type lockingOffOverlapRef struct {
	TicketID int64  `json:"ticket_id"`
	Path     string `json:"path"`
}

func toLockingOffOverlapRefs(overlaps []ticket.LockingOffOverlap) []lockingOffOverlapRef {
	out := make([]lockingOffOverlapRef, len(overlaps))
	for i, o := range overlaps {
		out[i] = lockingOffOverlapRef{TicketID: o.TicketID, Path: o.Path}
	}
	return out
}

type sharedOverlapRef struct {
	TicketID int64  `json:"ticket_id"`
	Path     string `json:"path"`
	Resolve  string `json:"resolve" jsonschema:"migration or regenerate: how a clash there is settled when the base branch is merged"`
}

func toSharedOverlapRefs(overlaps []ticket.SharedOverlap) []sharedOverlapRef {
	out := make([]sharedOverlapRef, len(overlaps))
	for i, o := range overlaps {
		out[i] = sharedOverlapRef{TicketID: o.TicketID, Path: o.Path, Resolve: o.Resolve}
	}
	return out
}

func (h *Handler) ticketNext(ctx context.Context, agentID uuid.UUID, in ticketNextInput) (*mcp.CallToolResult, ticketNextOutput, error) {
	result, err := h.tickets.Claim(ctx, agentID, in.TicketID)
	var missing *ticket.MissingRoleError
	if errors.As(err, &missing) {
		// An answer, like the disabled switch below: retrying can't help,
		// only a person giving the agent the role can.
		return text("%s", missing.Error()), ticketNextOutput{Blocked: missing.Error()}, nil
	}
	var inVariant *ticket.VariantClaimError
	if errors.As(err, &inVariant) {
		return text("%s", inVariant.Error()), ticketNextOutput{Refused: inVariant.Error()}, nil
	}
	if errors.Is(err, ticket.ErrConsoleUnnamedClaim) {
		// An answer, not a failure: retrying the same call is refused again.
		return text("%s", err.Error()), ticketNextOutput{Refused: err.Error()}, nil
	}
	if errors.Is(err, ticket.ErrSetupIncomplete) {
		// An answer, like the disabled switch below: only a person setting
		// the tenant up changes it, and the ticket was never taken.
		return text("%s", err.Error()), ticketNextOutput{Refused: err.Error()}, nil
	}
	if errors.Is(err, ticket.ErrProcessingDisabled) {
		// A refusal, not a failure. Returned as a normal result so the
		// agent reads it as an answer and stops: surfaced as a tool error
		// it looks like something to retry, which is the one response that
		// makes a disabled queue expensive.
		msg := "Ticket processing is switched off, so no ticket can be claimed. " +
			"This is deliberate -- do not retry. Stop."
		return text("%s", msg), ticketNextOutput{Disabled: msg}, nil
	}
	if err != nil {
		return nil, ticketNextOutput{}, err
	}
	switch {
	case result.Ticket != nil:
		h.progress(ctx, agentID)
		ref := h.toRef(ctx, result.Ticket)
		granted := fmt.Sprintf("Granted ticket #%d: %s\nScopes: %s", ref.ID, ref.Title, joinOrNone(ref.Paths))
		branch := ticket.ClaimBranch(result)
		guidance := ticket.GuidanceFor(result.Ticket, branch)
		view := heldView{Guidance: guidance.OrNil()}
		attempt := 1
		if result.Attempt != nil {
			attempt = result.Attempt.Number
		}
		view.Attempt = ticket.NewAttemptView(result.Ticket, attempt, branch)
		// Needs-discussion first, so it outranks the review guidance below.
		if guidance.NeedsDiscussion != "" {
			granted += "\n" + guidance.NeedsDiscussion
		}
		granted += "\n" + view.Attempt.Guidance
		if guidance.External != "" {
			granted += "\n" + guidance.External
		}
		if ref.URL != "" {
			granted += fmt.Sprintf("\nLink: %s -- its PR's description links back here, with \"slop-haus #%d\".", ref.URL, ref.ID)
		}
		for _, line := range []string{guidance.DoneThroughOther, guidance.Review} {
			if line != "" {
				granted += "\n" + line
			}
		}
		out := ticketNextOutput{Ticket: &ref, heldView: view}
		if notice := ticket.SharedOverlapNotice(result.SharedOverlaps); notice != "" {
			granted += "\n" + notice
			out.SharedOverlaps = toSharedOverlapRefs(result.SharedOverlaps)
		}
		if notice := ticket.LockingOffNotice(result.LockingOffOverlaps); notice != "" {
			granted += "\n" + notice
			out.LockingOffOverlaps = toLockingOffOverlapRefs(result.LockingOffOverlaps)
		}
		if h.workspace != nil {
			if notice := h.workspace.ClaimNotice(ctx, agentID, ref.Paths); notice != "" {
				// Last, and in capitals: it outranks everything above.
				granted += "\n" + notice
				out.WorkspaceMismatch = notice
			}
		}
		return text("%s", granted), out, nil
	case result.Blocked != nil:
		msg := fmt.Sprintf(
			"No ticket available. #%d is next but its scope %q is held by ticket #%d. Nothing can be taken until it frees: stop here.",
			result.Blocked.TicketID, result.Blocked.ConflictingPath, result.Blocked.ConflictingTicketID)
		return text("%s", msg), ticketNextOutput{Blocked: msg}, nil
	case in.TicketID != nil:
		msg := h.notGranted(ctx, agentID, *in.TicketID)
		return text("%s", msg), ticketNextOutput{Message: msg}, nil
	default:
		msg := "The queue is empty."
		return text("%s", msg), ticketNextOutput{Message: msg}, nil
	}
}

// notGranted says why a directed claim of #id came back empty. A ticket of
// another tenant reads exactly as one that does not exist.
func (h *Handler) notGranted(ctx context.Context, agentID uuid.UUID, id int64) string {
	detail, err := h.tickets.DetailFor(ctx, id, agentID)
	if err != nil {
		if !errors.Is(err, ticket.ErrNotFound) {
			h.logger.Warn("explain directed claim", "ticket_id", id, "err", err)
		}
		return fmt.Sprintf("Ticket #%d was not granted: no such ticket.", id)
	}
	var why string
	switch detail.Ticket.Status {
	case ticket.StatusInProgress, ticket.StatusDiscussion, ticket.StatusReview:
		why = "it is held by another agent or waiting on a human (" + statusWord(detail.Ticket.Status) + ")"
	case ticket.StatusPreparing:
		why = "it is still Preparing: a human has to open it first"
	case ticket.StatusInMergeQueue:
		why = "it is held by the merge queue, which lands it"
	case ticket.StatusParked:
		why = "it is parked"
	case ticket.StatusBlocked:
		why = "it is blocked on tickets that are not done yet"
	case ticket.StatusDone, ticket.StatusCancelled:
		why = "it is already " + statusWord(detail.Ticket.Status)
	default:
		why = "it is " + statusWord(detail.Ticket.Status) + " but not claimable by you right now, e.g. reserved for the agent that is to resume it"
	}
	return fmt.Sprintf("Ticket #%d was not granted: %s.", id, why)
}

func isConsoleIdentity(caller authn.Identity) bool {
	c, ok := caller.(interface{ IsConsole() bool })
	return ok && c.IsConsole()
}

type scopeExtendInput struct {
	Paths []string `json:"paths" jsonschema:"repo-relative paths; end a directory with /"`
}

type scopeExtendOutput struct {
	toolNotes
	Granted bool   `json:"granted"`
	Denied  string `json:"denied,omitempty"`
	// SharedOverlaps, LockingOffOverlaps: see ticketNextOutput.
	SharedOverlaps     []sharedOverlapRef     `json:"shared_overlaps,omitempty"`
	LockingOffOverlaps []lockingOffOverlapRef `json:"locking_off_overlaps,omitempty"`
}

func (h *Handler) scopeExtend(ctx context.Context, agentID uuid.UUID, in scopeExtendInput) (*mcp.CallToolResult, scopeExtendOutput, error) {
	paths := make([]model.Path, 0, len(in.Paths))
	for _, p := range in.Paths {
		paths = append(paths, model.Path{Path: p})
	}
	result, err := h.tickets.ExtendScope(ctx, agentID, paths)
	if err != nil {
		return nil, scopeExtendOutput{}, h.leaseLost(ctx, agentID, err)
	}
	if !result.Granted {
		// A denied extension is not progress: it mutated nothing, and
		// counting it would let an agent stuck in an extend-deny cycle
		// evade the stop-hook loop breaker forever.
		msg := fmt.Sprintf("Denied: ticket #%d already holds %q. Narrow your approach, or release with a comment.",
			result.Denied.ConflictingTicketID, result.Denied.ConflictingPath)
		return text("%s", msg), scopeExtendOutput{Denied: msg}, nil
	}
	h.progress(ctx, agentID)
	msg := "Scope extended."
	out := scopeExtendOutput{Granted: true}
	if notice := ticket.SharedOverlapNotice(result.SharedOverlaps); notice != "" {
		msg += "\n" + notice
		out.SharedOverlaps = toSharedOverlapRefs(result.SharedOverlaps)
	}
	if notice := ticket.LockingOffNotice(result.LockingOffOverlaps); notice != "" {
		msg += "\n" + notice
		out.LockingOffOverlaps = toLockingOffOverlapRefs(result.LockingOffOverlaps)
	}
	return text("%s", msg), out, nil
}

type scopeNarrowInput struct {
	Remove []string `json:"remove" jsonschema:"paths the ticket holds now, to give up"`
	Keep   []string `json:"keep,omitempty" jsonschema:"narrower paths inside the removed ones to keep"`
}

type scopeNarrowOutput struct {
	toolNotes
	Removed   []string `json:"removed"`
	Kept      []string `json:"kept"`
	Remaining []string `json:"remaining"`
}

func (h *Handler) scopeNarrow(ctx context.Context, agentID uuid.UUID, in scopeNarrowInput) (*mcp.CallToolResult, scopeNarrowOutput, error) {
	result, err := h.tickets.NarrowScope(ctx, agentID, in.Remove, in.Keep)
	if err != nil {
		return nil, scopeNarrowOutput{}, h.leaseLost(ctx, agentID, err)
	}
	h.progress(ctx, agentID)
	out := scopeNarrowOutput{Removed: result.Removed, Kept: result.Kept, Remaining: result.Remaining}
	if out.Kept == nil {
		out.Kept = []string{}
	}
	return text("Scope narrowed. The ticket now holds: %s.", strings.Join(result.Remaining, ", ")), out, nil
}

type releaseInput struct {
	Reason string `json:"reason" jsonschema:"why you are handing this back; posted as your comment unless blank"`
	// BlockedOn moves the ticket to blocked rather than open.
	BlockedOn *int64 `json:"blocked_on,omitempty" jsonschema:"ticket number this is waiting on"`
	// RetryAfter is a Go duration ("30m", "2h"); see ticket.ReleaseParams.
	RetryAfter string `json:"retry_after,omitempty" jsonschema:"how long to keep the ticket out of the queue before anyone may claim it again, e.g. 30m or 2h; the server clamps it (default 5m to 24h). Omitted, you get the minimum"`
	Handoff    string `json:"handoff,omitempty" jsonschema:"where the work stands, for whoever claims it next: branch, pushed commit, what is done and half done. Omit it to leave the previous handoff as the latest"`
}

func (h *Handler) release(ctx context.Context, agentID uuid.UUID, in releaseInput) (*mcp.CallToolResult, okOutput, error) {
	var retryAfter time.Duration
	if in.RetryAfter != "" {
		d, err := time.ParseDuration(in.RetryAfter)
		if err != nil {
			return nil, okOutput{}, fmt.Errorf("retry_after %q is not a duration like 30m or 2h: %w", in.RetryAfter, err)
		}
		retryAfter = d
	}
	h.narrowToChange(ctx, agentID)
	out, err := h.tickets.ReleaseWith(ctx, agentID, ticket.ReleaseParams{
		Reason: in.Reason, Handoff: in.Handoff, BlockedOn: in.BlockedOn, RetryAfter: retryAfter,
	})
	if err != nil {
		return nil, okOutput{}, h.leaseLost(ctx, agentID, err)
	}
	h.progress(ctx, agentID)
	switch {
	case out.Escalated:
		return okSays(true, "#%d is in discussion: %d releases in a row were waiting on a condition that had not cleared, "+
			"so a person needs to look. You no longer hold it; take other work, or stop if there is none.", out.Ticket.ID, out.Checks)
	case out.RetryAt != nil:
		return okSays(true, "Released #%d (now %s). It cannot be claimed again until %s UTC.",
			out.Ticket.ID, out.Ticket.Status, out.RetryAt.UTC().Format("15:04 on 2 Jan"))
	}
	return okSays(true, "Released #%d (now %s).", out.Ticket.ID, out.Ticket.Status)
}

type discussInput struct {
	Question string `json:"question" jsonschema:"what you need the human to answer; posted as your comment"`
	Handoff  string `json:"handoff" jsonschema:"where the work stands, what was decided, what you would do next -- for whoever picks the ticket up"`
}

func (h *Handler) discuss(ctx context.Context, agentID uuid.UUID, in discussInput) (*mcp.CallToolResult, okOutput, error) {
	h.narrowToChange(ctx, agentID)
	discussed, err := h.tickets.Discuss(ctx, agentID, in.Question, in.Handoff)
	if err != nil {
		return nil, okOutput{}, h.leaseLost(ctx, agentID, err)
	}
	h.progress(ctx, agentID)
	return okSays(true, "#%d is in discussion. You no longer hold it; take other work, or stop if there is none. "+
		"When the human answers it comes back to the queue with you first in line.", discussed.ID)
}

type prepareInput struct {
	Reason string `json:"reason" jsonschema:"why the ticket needs a human to reshape it before anyone works it; posted as your comment"`
}

func (h *Handler) prepare(ctx context.Context, agentID uuid.UUID, in prepareInput) (*mcp.CallToolResult, okOutput, error) {
	prepared, err := h.tickets.Prepare(ctx, agentID, in.Reason)
	if err != nil {
		return nil, okOutput{}, h.leaseLost(ctx, agentID, err)
	}
	h.progress(ctx, agentID)
	return okSays(true, "#%d is back in preparing. You no longer hold it; take other work, or stop if there is none. "+
		"It stays out of the queue until a human opens it.", prepared.ID)
}

// handOver is the review hand-over ticket_finish makes. Where change tools are on, the change flow does
// it, so a change slop-haus opened has the commit under review recorded with
// the hand-over; the tenant is the authenticated agent's.
func (h *Handler) handOver(ctx context.Context, agentID uuid.UUID, params *ticket.ReviewParams) (*ticket.Ticket, error) {
	if h.changes == nil {
		return h.tickets.Review(ctx, agentID, params)
	}
	found, err := authenticatedAgent(ctx)
	if err != nil {
		return nil, err
	}
	return h.changes.Review(ctx, found.TenantID(), agentID, params)
}

// undiscussedRefusal turns the needs-discussion gate and the voluntary-review
// reason on ticket_finish into an answer, as ticket_next does a missing role: as a
// tool error it reads as something to retry, and retrying can't help --
// only ticket_discuss can. It is not progress: nothing was written.
func undiscussedRefusal(err error) (*mcp.CallToolResult, bool) {
	if !errors.Is(err, ticket.ErrNeedsDiscussion) && !errors.Is(err, ticket.ErrReviewNeedsReason) && !errors.Is(err, landing.ErrRedRequeue) {
		return nil, false
	}
	return text("%s", err.Error()), true
}

func statusWord(s ticket.Status) string {
	return strings.ReplaceAll(string(s), "_", " ")
}
