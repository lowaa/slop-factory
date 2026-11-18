package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionlabel"
)

// DiscussionLabeller completes a discussion round's pending label with the
// steward's claim (stewardwire.DiscussionSubmitter).
type DiscussionLabeller interface {
	Submit(ctx context.Context, ticketID int64, attempt, round int, c *discussionlabel.Classification) (*discussionlabel.Record, error)
}

type stewardLabelInput struct {
	TicketID int64  `json:"ticket_id" jsonschema:"the discussion ticket"`
	Attempt  int    `json:"attempt" jsonschema:"the attempt number of the discussion, as ticket_get shows it"`
	Round    int    `json:"round" jsonschema:"the discussion round, as ticket_get shows it"`
	Label    string `json:"label" jsonschema:"routine or yours. When unsure, yours: never guess routine"`
	Category string `json:"category,omitempty" jsonschema:"for routine: the answer category you verified (merge_continue, infra_wait or gate_confirmed)"`
	Reason   string `json:"reason" jsonschema:"one line: why"`
}

type stewardLabelOutput struct {
	toolNotes
	Label    string             `json:"label"`
	Category string             `json:"category,omitempty"`
	Reason   string             `json:"reason"`
	Evidence []steward.Evidence `json:"evidence"`
}

func registerStewardLabelTool(server *mcp.Server, h *Handler) {
	addTool(server, h, &mcp.Tool{
		Name: "steward_label_discussion",
		Description: "Steward only. Label one pending discussion round: routine when one of the answer categories covers it, otherwise yours. " +
			"The server renders the category's template and runs its checks itself: a routine label whose checks fail becomes yours, " +
			"whatever you claim. When unsure say yours; never guess routine. Nothing is posted by this call. One label per round: a repeat " +
			"returns the existing one, and a round whose ticket or change moved since it was queued is refused as stale.",
	}, leaseFree, func(ctx context.Context, agentID uuid.UUID, in stewardLabelInput) (*mcp.CallToolResult, stewardLabelOutput, error) {
		var zero stewardLabelOutput
		isSteward, err := h.stewards.IsSteward(ctx, agentID)
		if err != nil {
			return nil, zero, err
		}
		if !isSteward {
			h.events.Record(ctx, audit.ConsoleToolRefused, "agent_id", agentID, "tool", "steward_label_discussion")
			return nil, zero, errNotSteward
		}
		label := discussionlabel.Label(in.Label)
		if label != discussionlabel.LabelRoutine && label != discussionlabel.LabelYours {
			return nil, zero, fmt.Errorf("label must be routine or yours, not %q", in.Label)
		}
		rec, err := h.labeller.Submit(ctx, in.TicketID, in.Attempt, in.Round, &discussionlabel.Classification{
			Label: label, Category: in.Category, Reason: in.Reason,
		})
		switch {
		case errors.Is(err, discussionlabel.ErrStale):
			return nil, zero, errors.New("that round moved since it was queued (the ticket's revision or the change's head changed): read the ticket again; " +
				"the owner can queue it again with \"Assess now\"")
		case errors.Is(err, discussionlabel.ErrNotFound):
			return nil, zero, fmt.Errorf("round %d of attempt %d on ticket %d has no label queued", in.Round, in.Attempt, in.TicketID)
		case errors.Is(err, discussionlabel.ErrOff):
			return nil, zero, errors.New("the steward is off")
		case err != nil:
			return nil, zero, err
		}
		out := stewardLabelOutput{Label: string(rec.Label), Category: rec.Category, Reason: rec.Reason, Evidence: rec.Evidence}
		if rec.Label == discussionlabel.LabelNotAssessed {
			return text("Round %d of attempt %d was not labelled in time (%s); it is waiting for the owner to assess it again.", in.Round, in.Attempt, rec.Reason), out, nil
		}
		return text("Labelled round %d of attempt %d: %s (%s).", in.Round, in.Attempt, rec.Label, rec.Reason), out, nil
	})
}
