package mcpserver

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
)

func noop[In any](context.Context, uuid.UUID, In) (*mcp.CallToolResult, okOutput, error) {
	return text("ok"), okOutput{OK: true}, nil
}

// A tool registered without saying whether it needs the held ticket would
// silently get one behaviour or the other on a revocation. Refused at
// startup instead.
func TestAddTool_RefusesAToolWithNoLeaseNeed(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	h := NewHandler(Deps{}, slog.DiscardHandler)

	assert.PanicsWithValue(t, `mcpserver: tool "unclassified" has no lease need`, func() {
		addTool(server, h, &mcp.Tool{Name: "unclassified"}, leaseNeed(0), noop[emptyInput])
	})
	assert.PanicsWithValue(t, `mcpserver: tool "nameless" is leaseNamed but its input names no ticket`, func() {
		addTool(server, h, &mcp.Tool{Name: "nameless"}, leaseNamed, noop[emptyInput])
	})
	assert.NotPanics(t, func() {
		addTool(server, h, &mcp.Tool{Name: "named"}, leaseNamed, noop[ticketCommentInput])
	})
}
