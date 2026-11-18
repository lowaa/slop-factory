package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
)

var errNotSteward = errors.New("only the steward may use this tool")

// Stewards says whether an agent is an enabled steward, read at call time.
type Stewards interface {
	IsSteward(ctx context.Context, agentID uuid.UUID) (bool, error)
}

// Assessor records the server's verdict on a steward's proposal
// (steward.Service).
type Assessor interface {
	Assess(ctx context.Context, agentID uuid.UUID, req *steward.Request) (*steward.Record, error)
}

type stewardAssessInput struct {
	ProposalID string `json:"proposal_id" jsonschema:"the id a propose_* tool gave you for the proposal"`
	Category   string `json:"category" jsonschema:"the steward category you believe the proposal falls in"`
	Reason     string `json:"reason" jsonschema:"one line: why it falls in that category; for needs_human, the decision the owner has to make"`
	HeadSHA    string `json:"head_sha,omitempty" jsonschema:"the head commit of the ticket's change you read, for categories that concern a change"`
}

type stewardAssessOutput struct {
	toolNotes
	Category string             `json:"category"`
	Tier     string             `json:"tier"`
	Verdict  string             `json:"verdict"`
	Mode     string             `json:"mode"`
	Evidence []steward.Evidence `json:"evidence"`
}

func registerStewardTools(server *mcp.Server, h *Handler) {
	addTool(server, h, &mcp.Tool{
		Name: "steward_assess",
		Description: "Steward only. Record which category your proposal falls in and why. The server runs that category's checks itself " +
			"and returns its verdict (would_auto, would_veto or needs_human) with the evidence; the verdict does not depend on what you " +
			"claim, and a failed check or an unknown category is needs_human. A discussion answer (merge_continue, infra_wait, gate_confirmed) " +
			"is posted as the owner, so its body must equal the category's server-rendered template; any other text is needs_human, and the " +
			"evidence quotes the exact text to propose. Call it once per proposal, straight after proposing. " +
			"A proposal you do not assess is shown to the owner as an ordinary proposal. Nothing is applied in shadow mode; in live mode " +
			"the server applies an Auto-tier decision at once and a Veto-tier one when the owner's window ends, after checking it again.",
	}, leaseFree, func(ctx context.Context, agentID uuid.UUID, in stewardAssessInput) (*mcp.CallToolResult, stewardAssessOutput, error) {
		var zero stewardAssessOutput
		isSteward, err := h.stewards.IsSteward(ctx, agentID)
		if err != nil {
			return nil, zero, err
		}
		if !isSteward {
			h.events.Record(ctx, audit.ConsoleToolRefused, "agent_id", agentID, "tool", "steward_assess")
			return nil, zero, errNotSteward
		}
		id, err := uuid.Parse(in.ProposalID)
		if err != nil {
			return nil, zero, fmt.Errorf("proposal_id must be a proposal id: %w", err)
		}
		rec, err := h.assessor.Assess(ctx, agentID, &steward.Request{ProposalID: id, Category: in.Category, Reason: in.Reason, HeadSHA: in.HeadSHA})
		if err != nil {
			return nil, zero, err
		}
		out := stewardAssessOutput{Category: rec.Category, Tier: string(rec.Tier), Verdict: string(rec.Verdict), Mode: string(rec.Mode), Evidence: rec.Evidence}
		return text("Assessed %s: %s (tier %s, %s mode). %s", id, out.Verdict, out.Tier, out.Mode, appliedNote(rec)), out, nil
	})
}

// appliedNote says what happens to the proposal next, which depends on the
// mode and the verdict as the server concluded them.
func appliedNote(rec *steward.Record) string {
	switch {
	case rec.Mode != steward.ModeLive || rec.Verdict == steward.VerdictNeedsHuman:
		return "Nothing is applied until the owner approves it."
	case rec.Tier == steward.TierAuto:
		return "The server applies it at once, after checking it again; if a check fails it is left to the owner."
	}
	return "The server applies it when the owner's veto window ends, after checking it again, unless the owner vetoes it."
}
