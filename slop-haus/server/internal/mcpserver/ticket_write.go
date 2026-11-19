package mcpserver

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

type ticketCommentInput struct {
	TicketID int64  `json:"ticket_id"`
	Attempt  *int   `json:"attempt,omitempty" jsonschema:"only a console or conductor: the attempt the comment is addressed to, which then only that attempt's agent is told; left out on a ticket with several live attempts, every one of them is told; a worker never passes it"`
	Body     string `json:"body"`
}

func (in ticketCommentInput) namedTicket() int64 { return in.TicketID }

type ticketCreateInput struct {
	Title     string   `json:"title"`
	Body      string   `json:"body,omitempty"`
	Priority  string   `json:"priority,omitempty" jsonschema:"low, normal, high or urgent"`
	Paths     []string `json:"paths,omitempty" jsonschema:"the narrowest repo-relative paths you expect the work to write, end a directory with /. Prefer leaf directories or files over shared roots: where the tenant locks path scopes, a broad scope stops every overlapping ticket from being claimed while this one is held. Whoever takes the ticket extends it with ticket_scope_extend as it finds it needs more"`
	Status    string   `json:"status,omitempty" jsonschema:"preparing (the default) or open. Only open if you were specifically asked to file it as open"`
	BlockedOn []int64  `json:"blocked_on,omitempty" jsonschema:"ticket numbers the new ticket waits on"`
	Roles     []string `json:"roles,omitempty" jsonschema:"role names, exactly as spelled; only an agent with them may claim it"`
	RoleMatch string   `json:"role_match,omitempty" jsonschema:"any (the default): one of the roles is enough; all: every one"`
	BaseRef   string   `json:"base_ref,omitempty" jsonschema:"a commit hash or a tag the work starts from, not a branch. Leave it out to start from the base branch's tip when the ticket is claimed"`
}

func (h *Handler) ticketComment(ctx context.Context, agentID uuid.UUID, in ticketCommentInput) (*mcp.CallToolResult, okOutput, error) {
	if _, err := h.tickets.AgentCommentOn(ctx, agentID, in.TicketID, in.Attempt, in.Body); err != nil {
		return nil, okOutput{}, err
	}
	h.progress(ctx, agentID)
	return okSays(true, "Commented on #%d.", in.TicketID)
}

func (h *Handler) ticketCreate(ctx context.Context, agentID uuid.UUID, in ticketCreateInput) (*mcp.CallToolResult, ticketCreated, error) {
	paths := make([]model.Path, 0, len(in.Paths))
	for _, p := range in.Paths {
		paths = append(paths, model.Path{Path: p})
	}
	created, err := h.tickets.Create(ctx, &ticket.CreateParams{
		Title:            in.Title,
		Body:             in.Body,
		Priority:         model.Priority(in.Priority),
		Paths:            paths,
		CreatedByAgentID: &agentID,
		Status:           ticket.Status(in.Status),
		BlockedOn:        in.BlockedOn,
		BaseRef:          in.BaseRef,
		// By name, and only when filing: adding a requirement never widens
		// what an agent can claim. Nothing an agent calls removes one.
		Roles: ticket.RoleRequirement{Names: in.Roles, Match: model.RoleMatch(in.RoleMatch)},
	})
	if err != nil {
		return nil, ticketCreated{}, err
	}
	h.progress(ctx, agentID)
	ref := h.toRef(ctx, created)
	filed := fmt.Sprintf("Filed #%d (%s)", ref.ID, ref.Status)
	if len(ref.BlockedBy) > 0 {
		filed += ", blocked by: " + blockedByLine(ref.BlockedBy)
	}
	if len(ref.Roles) > 0 {
		filed += ", claimable only by an agent with " + ticket.DescribeRoles(ref.Roles, model.RoleMatch(ref.RoleMatch))
	}
	out := ticketCreated{ticketRef: ref}
	message := filed + "."
	// Feedback only: the ticket is filed whatever this finds, and a failure
	// to compute it is not a failure to file.
	if overlaps, err := h.tickets.ScopeOverlaps(ctx, created.ID); err == nil && len(overlaps) > 0 {
		out.ScopeOverlaps = toOverlapRefs(overlaps)
		hint := ticket.SharedOverlapsHint
		if slices.ContainsFunc(overlaps, func(o ticket.ScopeOverlap) bool { return o.Resolve == "" }) {
			hint = ticket.ScopeOverlapsHint
			if slices.ContainsFunc(overlaps, func(o ticket.ScopeOverlap) bool { return o.LockingOff }) {
				hint = ticket.LockingOffOverlapsHint
			}
		}
		message += "\n\nIts scopes overlap live tickets:\n" + overlapLines(overlaps) + "\n" + hint +
			" A human sees this list when opening the ticket and can narrow its paths on the board."
	}
	out.Guidance = message
	return text("%s", message), out, nil
}

// ticketCreated is ticket_create's result: the ticket, and the live tickets
// whose scopes it overlaps.
type ticketCreated struct {
	ticketRef
	toolNotes
	// Guidance is the text's answer: what was filed, and what its scope
	// overlaps with and how to deal with that.
	Guidance      string       `json:"guidance"`
	ScopeOverlaps []overlapRef `json:"scope_overlaps,omitempty"`
}

type overlapRef struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	// Path is the new ticket's scope, Other the live ticket's.
	Path  string `json:"path"`
	Other string `json:"other"`
	// Resolve is set for an overlap inside a shared path, which blocks nothing.
	Resolve string `json:"resolve,omitempty"`
	// LockingOff is set when the tenant's scope locking is off: it blocks nothing.
	LockingOff bool `json:"locking_off,omitempty"`
}

func toOverlapRefs(overlaps []ticket.ScopeOverlap) []overlapRef {
	out := make([]overlapRef, len(overlaps))
	for i, o := range overlaps {
		out[i] = overlapRef{ID: o.TicketID, Title: o.Title, Status: string(o.Status), Path: o.Path, Other: o.Other, Resolve: o.Resolve, LockingOff: o.LockingOff}
	}
	return out
}

func overlapLines(overlaps []ticket.ScopeOverlap) string {
	lines := make([]string, len(overlaps))
	for i, o := range overlaps {
		lines[i] = fmt.Sprintf("- #%d %q (%s): %s overlaps %s", o.TicketID, o.Title, o.Status, o.Path, o.Other)
		if o.Resolve != "" {
			lines[i] += fmt.Sprintf(" [shared, %s: blocks nothing]", o.Resolve)
		}
		if o.LockingOff {
			lines[i] += " [scope locking is off: blocks nothing]"
		}
	}
	return strings.Join(lines, "\n")
}
