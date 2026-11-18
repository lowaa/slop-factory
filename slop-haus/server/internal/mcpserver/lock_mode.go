package mcpserver

import (
	"context"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/usagemetrics"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// QueueMode says whether the caller's tenant lands by the server's merge
// queue; landing.Mode is one. It is read on every request, so a switch back
// to the worker flow restores the lock tools at the agent's next session
// without a release.
type QueueMode interface {
	Enabled(ctx context.Context) (bool, error)
}

// lockTools are offered only to a tenant whose workers merge for themselves.
var lockTools = []string{"integration_lock", "integration_unlock"}

const queueLockRefusal = "this tenant lands by the merge queue: call ticket_finish"

// lockToolGuard hides the lock tools from, and refuses them to, a tenant that
// lands by the merge queue. A receiving middleware, like readOnlyGuard, so the
// tools stay registered for the tenants that need them. When the mode can't be
// read the tools are left alone: the lock itself still guards the merge.
func (h *Handler) lockToolGuard(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if h.queueMode == nil || (method != "tools/list" && method != "tools/call") {
			return next(ctx, method, req)
		}
		if call, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok && !slices.Contains(lockTools, call.Name) {
			return next(ctx, method, req)
		}
		queued, err := h.queueMode.Enabled(ctx)
		if err != nil {
			h.logger.Warn("could not read the tenant's merge method; leaving the lock tools alone", "err", err)
			return next(ctx, method, req)
		}
		if !queued {
			return next(ctx, method, req)
		}
		if method == "tools/call" {
			setOutcome(ctx, usagemetrics.OutcomeRefused)
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: queueLockRefusal}},
			}, nil
		}
		res, err := next(ctx, method, req)
		if list, ok := res.(*mcp.ListToolsResult); ok && err == nil {
			list.Tools = slices.DeleteFunc(list.Tools, func(t *mcp.Tool) bool { return slices.Contains(lockTools, t.Name) })
		}
		return res, err
	}
}
