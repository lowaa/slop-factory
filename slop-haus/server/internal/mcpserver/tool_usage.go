package mcpserver

import (
	"context"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/usagemetrics"
)

// ToolUsage counts tool calls, in aggregate (*usagemetrics.Recorder): it
// is given a tool's name, the kind of agent and how the call ended, and
// nothing that identifies either.
type ToolUsage interface {
	// Known is told each registered tool once, so only those are counted
	// under their own name.
	Known(tool string)
	ToolCall(tool, agentKind string, outcome usagemetrics.Outcome, took time.Duration)
}

// kinded is implemented by the agent behind a call (agent.Agent).
type kinded interface{ AgentKind() string }

// callOutcome is how addTool tells toolUsageGuard what its tool did: the
// guard sees every call, but only addTool knows a refusal from a failure.
type callOutcome struct{ outcome usagemetrics.Outcome }

type callOutcomeKey struct{}

func setOutcome(ctx context.Context, outcome usagemetrics.Outcome) {
	if c, ok := ctx.Value(callOutcomeKey{}).(*callOutcome); ok {
		c.outcome = outcome
	}
}

// toolUsageGuard counts every tool call once. It must be the first receiving
// middleware, so a call another guard refuses is counted too. A call that
// reached no tool (addTool set no outcome) is a refusal by a guard, or its
// arguments failed the tool's schema, which the SDK reports before the tool
// runs.
func (h *Handler) toolUsageGuard(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		call, ok := req.GetParams().(*mcp.CallToolParamsRaw)
		if h.toolUsage == nil || method != "tools/call" || !ok {
			return next(ctx, method, req)
		}
		slot := &callOutcome{}
		start := time.Now()
		res, err := next(context.WithValue(ctx, callOutcomeKey{}, slot), method, req)

		outcome := slot.outcome
		if outcome == "" {
			var rpc *jsonrpc.Error
			switch result, _ := res.(*mcp.CallToolResult); {
			case errors.As(err, &rpc) && rpc.Code == jsonrpc.CodeInvalidParams:
				outcome = usagemetrics.OutcomeInvalid
			case err != nil:
				outcome = usagemetrics.OutcomeRefused
			case result != nil && result.IsError:
				outcome = usagemetrics.OutcomeInvalid
			default:
				outcome = usagemetrics.OutcomeOK
			}
		}
		kind := ""
		if found, err := authenticatedAgent(ctx); err == nil {
			if k, ok := found.(kinded); ok {
				kind = k.AgentKind()
			}
		}
		h.toolUsage.ToolCall(call.Name, kind, outcome, time.Since(start))
		return res, err
	}
}
