package mcpserver

import (
	"context"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (h *Handler) integrationLock(ctx context.Context, agentID uuid.UUID, _ emptyInput) (*mcp.CallToolResult, okOutput, error) {
	if err := h.tickets.AcquireIntegrationLock(ctx, agentID); err != nil {
		return nil, okOutput{}, h.leaseLost(ctx, agentID, err)
	}
	h.progress(ctx, agentID)
	return okSays(true, "Integration lock held. Your lease will not expire while you hold it.")
}

func (h *Handler) integrationUnlock(ctx context.Context, agentID uuid.UUID, _ emptyInput) (*mcp.CallToolResult, okOutput, error) {
	if err := h.tickets.ReleaseIntegrationLock(ctx, agentID); err != nil {
		return nil, okOutput{}, err
	}
	h.progress(ctx, agentID)
	return okSays(true, "Integration lock released.")
}
