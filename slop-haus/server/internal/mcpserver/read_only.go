package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
)

// readOnlyTools are the tools a read-only credential may call (see
// oauth.ScopeRead): ones that change nothing, not even the caller's
// last-seen time (caller skips that for it). An allowlist, so a new tool
// is closed to it until someone decides it belongs here.
var readOnlyTools = map[string]bool{
	"ticket_get":      true,
	"landing_batches": true,
}

// readOnlyMethods are the MCP methods a read-only credential may use
// besides calling those tools: the handshake, and listing what exists.
var readOnlyMethods = map[string]bool{
	"initialize":                true,
	"server/discover":           true,
	"notifications/initialized": true,
	"notifications/cancelled":   true,
	"ping":                      true,
	"tools/list":                true,
}

var errReadOnly = errors.New("this credential is read-only: it may not do this")

// readOnlyGuard refuses a read-only caller anything outside the
// allowlists. A receiving middleware rather than a check in addTool: it
// sees every method, not just the tools registered through addTool, and
// runs before the SDK validates a tool's arguments -- so the refusal is the
// same whatever the caller sent.
func (h *Handler) readOnlyGuard(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		found, err := authenticatedAgent(ctx)
		if err != nil {
			return nil, err
		}
		if !found.ReadOnly() || readOnlyMethods[method] {
			return next(ctx, method, req)
		}
		tool := ""
		if call, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok && method == "tools/call" {
			if readOnlyTools[call.Name] {
				return next(ctx, method, req)
			}
			tool = call.Name
		}
		h.events.Record(ctx, audit.ReadOnlyRefused,
			"agent_id", found.AgentID(), "tenant", found.TenantID(), "mcp_method", method, "tool", tool)
		return nil, errReadOnly
	}
}
