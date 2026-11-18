package mcpserver

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/assist"
)

// Drafter stores the steward's draft of a decision (assist.Service).
type Drafter interface {
	Draft(ctx context.Context, agentID uuid.UUID, req *assist.Request) (*assist.Draft, error)
}

type stewardDraftInput struct {
	ProposalID    string `json:"proposal_id" jsonschema:"the id a propose_* tool gave you for the proposal"`
	Category      string `json:"category" jsonschema:"the steward category you believe the proposal falls in"`
	Reason        string `json:"reason" jsonschema:"one line: why it falls in that category"`
	HeadSHA       string `json:"head_sha,omitempty" jsonschema:"the head commit of the ticket's change you read, for categories that concern a change"`
	Body          string `json:"body,omitempty" jsonschema:"the review notes or a summary of the edit for the owner to read; for an answer the server shows the proposal's own text instead"`
	WouldSendBack bool   `json:"would_send_back,omitempty" jsonschema:"a review only: you would request changes. The draft is then notes for the owner and is never applied"`
}

type stewardDraftOutput struct {
	toolNotes
	Category      string             `json:"category"`
	WouldSendBack bool               `json:"would_send_back"`
	Revision      int                `json:"revision"`
	HeadSHA       string             `json:"head_sha,omitempty"`
	Evidence      []steward.Evidence `json:"evidence"`
}

func registerStewardDraftTool(server *mcp.Server, h *Handler) {
	addTool(server, h, &mcp.Tool{
		Name: "steward_draft",
		Description: "Steward only, and only while the owner has the steward at the assist level. Pre-draft the decision on one of your pending " +
			"proposals so the owner can apply it with one click. The server runs the category's checks itself and keeps every result, pass " +
			"or fail, as the evidence the owner sees; the ticket revision and head it records are the ones it reads, not yours. Nothing is " +
			"applied by this call: the owner applies the draft, and the server checks it again when they do. A review you would send back " +
			"is notes for the owner only. One draft per proposal.",
	}, leaseFree, func(ctx context.Context, agentID uuid.UUID, in stewardDraftInput) (*mcp.CallToolResult, stewardDraftOutput, error) {
		var zero stewardDraftOutput
		isSteward, err := h.stewards.IsSteward(ctx, agentID)
		if err != nil {
			return nil, zero, err
		}
		if !isSteward {
			h.events.Record(ctx, audit.ConsoleToolRefused, "agent_id", agentID, "tool", "steward_draft")
			return nil, zero, errNotSteward
		}
		id, err := uuid.Parse(in.ProposalID)
		if err != nil {
			return nil, zero, fmt.Errorf("proposal_id must be a proposal id: %w", err)
		}
		d, err := h.drafter.Draft(ctx, agentID, &assist.Request{ProposalID: id, Category: in.Category, Reason: in.Reason,
			HeadSHA: in.HeadSHA, Body: in.Body, WouldSendBack: in.WouldSendBack})
		if err != nil {
			return nil, zero, err
		}
		out := stewardDraftOutput{Category: d.Category, WouldSendBack: d.WouldSendBack, Revision: d.TicketRevision, HeadSHA: d.HeadSHA, Evidence: d.Evidence}
		note := "The owner applies it from the approvals page; nothing is applied until they do."
		if d.WouldSendBack {
			note = "It is notes for the owner and is never applied."
		}
		return text("Drafted %s (%s). %s", id, d.Category, note), out, nil
	})
}
