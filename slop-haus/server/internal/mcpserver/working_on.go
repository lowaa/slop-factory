package mcpserver

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/llmusage"
)

func registerWorkingOn(server *mcp.Server, h *Handler) {
	addConsoleTool(server, h, &mcp.Tool{
		Name: "working_on",
		Description: "Declare the ticket your own work is about from now on, so the usage of your model calls is counted as " +
			"guidance for it, or null for none (back to overhead). It does not claim or change the ticket, and the ticket must " +
			"exist. A conductor's declaration follows its one live conversation; a console's covers all of the agent's sessions, " +
			"because this request carries no session id: two console sessions on one agent share the latest declaration. " +
			"Pass attempt (with ticket_id) to name which of the ticket's attempts you are guiding: it is checked and recorded, and the usage you report from then on is charged to it. " +
			"With one live attempt it can be left out; with several and none named the call is refused with the list. " +
			"Usage is split as finely as reports arrive: a report that arrives after you declare but covers turns from before it " +
			"is charged whole to the declaration current when it is stored. Usage from before your first declaration stays overhead.",
	}, h.workingOn)
}

type workingOnInput struct {
	TicketID *int64 `json:"ticket_id" jsonschema:"the ticket to count your usage towards, or null for none"`
	Attempt  *int   `json:"attempt,omitempty" jsonschema:"the attempt of that ticket you are guiding; needs ticket_id"`
}

type workingOnOutput struct {
	toolNotes
	TicketID *int64 `json:"ticket_id,omitempty" jsonschema:"the ticket your usage now goes to; absent for overhead"`
}

func (h *Handler) workingOn(ctx context.Context, agentID uuid.UUID, in workingOnInput) (*mcp.CallToolResult, workingOnOutput, error) {
	if in.TicketID == nil && in.Attempt != nil {
		return nil, workingOnOutput{}, errors.New("attempt needs a ticket_id")
	}
	if in.TicketID != nil {
		// Resolved here at the edge, so what is declared is a real, live
		// attempt of a real ticket.
		if _, err := h.tickets.ResolveAttempt(ctx, *in.TicketID, in.Attempt); err != nil {
			return nil, workingOnOutput{}, err
		}
	}
	declared, err := h.usage.Declare(ctx, in.TicketID, in.Attempt)
	switch {
	case errors.Is(err, llmusage.ErrTicketNotFound):
		return nil, workingOnOutput{}, errors.New("no such ticket")
	case err != nil:
		return nil, workingOnOutput{}, err
	}
	h.progress(ctx, agentID)
	if declared.TicketID == nil {
		return text("Your usage goes to overhead from now on."), workingOnOutput{}, nil
	}
	return text("Your usage goes to #%d (%s) from now on.", *declared.TicketID, declared.TicketTitle),
		workingOnOutput{TicketID: declared.TicketID}, nil
}
