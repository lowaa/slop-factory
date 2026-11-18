package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// blockerInput names both ends of the edge. Which tickets an agent may
// change is the ticket service's rule (see ticket.Service.AgentAddDependency), not the
// tool's: the tool is one door among several onto the same edges.
type blockerInput struct {
	TicketID  int64 `json:"ticket_id" jsonschema:"the ticket that waits"`
	BlockerID int64 `json:"blocker_id" jsonschema:"the ticket it waits on"`
}

func (in blockerInput) namedTicket() int64 { return in.TicketID }

// blockerRef is one ticket another waits on, with its status so an agent
// can tell whether it is still holding anything up.
type blockerRef struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type blockersOutput struct {
	toolNotes
	Changed   bool         `json:"changed"`
	Status    string       `json:"status"`
	BlockedBy []blockerRef `json:"blocked_by"`
}

func (h *Handler) blockerAdd(ctx context.Context, agentID uuid.UUID, in blockerInput) (*mcp.CallToolResult, blockersOutput, error) {
	change, err := h.tickets.AgentAddDependency(ctx, agentID, in.TicketID, in.BlockerID)
	if err != nil {
		return nil, blockersOutput{}, err
	}
	return h.blockerResult(ctx, agentID, change,
		fmt.Sprintf("#%d now waits on #%d.", in.TicketID, in.BlockerID),
		fmt.Sprintf("#%d already waits on #%d; nothing changed.", in.TicketID, in.BlockerID))
}

func (h *Handler) blockerRemove(ctx context.Context, agentID uuid.UUID, in blockerInput) (*mcp.CallToolResult, blockersOutput, error) {
	change, err := h.tickets.AgentRemoveDependency(ctx, agentID, in.TicketID, in.BlockerID)
	if err != nil {
		return nil, blockersOutput{}, err
	}
	return h.blockerResult(ctx, agentID, change,
		fmt.Sprintf("#%d no longer waits on #%d.", in.TicketID, in.BlockerID),
		fmt.Sprintf("#%d was not waiting on #%d; nothing changed.", in.TicketID, in.BlockerID))
}

// blockerResult answers with the ticket's blockers as they now stand, so
// the agent sees the whole of what it edited rather than one edge.
func (h *Handler) blockerResult(ctx context.Context, agentID uuid.UUID, change *ticket.DependencyChange, changed, unchanged string) (*mcp.CallToolResult, blockersOutput, error) {
	t := change.Ticket
	out := blockersOutput{Changed: change.Changed, Status: string(t.Status), BlockedBy: toBlockerRefs(t.Blockers)}
	lead := unchanged
	if change.Changed {
		// A no-op is not progress: counting it would let an agent looping on
		// the same edge evade the stop-hook loop breaker.
		h.progress(ctx, agentID)
		lead = changed
	}
	return text("%s #%d is %s. Blocked by: %s", lead, t.ID, t.Status, blockedByLine(out.BlockedBy)), out, nil
}

func toBlockerRefs(refs []model.TicketRef) []blockerRef {
	out := make([]blockerRef, 0, len(refs))
	for _, r := range refs {
		out = append(out, blockerRef{ID: r.ID, Title: r.Title, Status: string(r.Status)})
	}
	return out
}

func blockedByLine(refs []blockerRef) string {
	if len(refs) == 0 {
		return "nothing"
	}
	parts := make([]string, 0, len(refs))
	for _, r := range refs {
		parts = append(parts, fmt.Sprintf("#%d %s (%s)", r.ID, r.Title, r.Status))
	}
	return strings.Join(parts, ", ")
}
