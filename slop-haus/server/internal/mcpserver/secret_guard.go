package mcpserver

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/secretscan"
)

// Refusals counts writes refused for holding a credential
// (metrics.Metrics.SecretWriteRefused).
type Refusals interface {
	SecretWriteRefused(kind string)
}

// refuseSecrets is the second line against an agent that read hostile text
// and was steered into copying a secret into something it writes. It runs in
// addTool, before the tool's own work, so every input of every tool is
// covered -- comments, handoffs, proposals, drafts, change descriptions --
// and a new tool cannot forget it. Text a person writes on the board does not
// come through here.
//
// The refusal, the audit record and the log carry the kind and never the
// text, so a refused credential reaches no further than the request.
func (h *Handler) refuseSecrets(ctx context.Context, agentID uuid.UUID, tool string, in any) error {
	found := secretscan.Check(in)
	if found == nil {
		return nil
	}
	attrs := []any{"agent_id", agentID, "tool", tool, "kinds", found.Kinds, "field", found.Field}
	// Best effort: most refused writes name a ticket, or the agent holds one.
	if named, ok := in.(namedTicketer); ok {
		attrs = append(attrs, "ticket_id", named.namedTicket())
	} else if lease, err := h.tickets.LeaseFor(ctx, agentID); err == nil && lease != nil {
		attrs = append(attrs, "ticket_id", lease.TicketID)
	}
	if identity, err := authenticatedAgent(ctx); err == nil {
		attrs = append(attrs, "tenant", identity.TenantID())
	}
	h.events.Record(ctx, audit.SecretWriteRefused, attrs...)
	if h.refusals != nil {
		for _, kind := range found.Kinds {
			h.refusals.SecretWriteRefused(string(kind))
		}
	}
	// A plain error: the SDK shows its text to the agent as the tool's failure.
	return errors.New(found.Error())
}
