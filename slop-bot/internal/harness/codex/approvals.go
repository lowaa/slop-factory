package codex

import (
	"encoding/json"
	"fmt"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness/codex/protocol"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/rpcdriver"
)

// methodNotFound is JSON-RPC's code for a method the client doesn't offer.
const methodNotFound = -32601

// answer replies to a server request. With approvalPolicy "never" Codex
// sends none (protocol spike), but nobody is at a terminal to answer one,
// and unanswered it would hold the turn for ever. So each is answered the
// way the agent's permissive mode means: approved. mu is held.
func (c *codec) answer(w *rpcdriver.Wire, m *message) error {
	result, err := approval(m)
	if err != nil {
		c.opts.logger.Warn("codex sent a request slop-bot can't answer; refused", "method", m.Method, "err", err)
		return write(w, &message{ID: m.ID, Error: &rpcError{Code: methodNotFound, Message: err.Error()}})
	}
	c.opts.logger.Warn("codex asked for approval despite approvalPolicy never; approved", "method", m.Method)
	b, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("codex: encoding the answer to %s: %w", m.Method, err)
	}
	return write(w, &message{ID: m.ID, Result: b})
}

// approval is the approving answer to one server request, or an error for
// a request that isn't an approval (a dynamic tool call, which slop-bot
// registers none of; a token refresh, which Codex does itself).
func approval(m *message) (any, error) {
	switch m.Method {
	case protocol.MethodItemCommandExecutionRequestApproval:
		return &protocol.CommandExecutionRequestApprovalResponse{Decision: protocol.CommandExecutionApprovalDecisionAccept}, nil
	case protocol.MethodItemFileChangeRequestApproval:
		return &protocol.FileChangeRequestApprovalResponse{Decision: protocol.FileChangeApprovalDecisionAccept}, nil
	case protocol.MethodApplyPatchApproval:
		return &protocol.ApplyPatchApprovalResponse{Decision: protocol.ReviewDecisionApproved}, nil
	case protocol.MethodExecCommandApproval:
		return &protocol.ExecCommandApprovalResponse{Decision: protocol.ReviewDecisionApproved}, nil
	case protocol.MethodItemPermissionsRequestApproval:
		// Grant what was asked, for this turn.
		var p protocol.PermissionsRequestApprovalParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return nil, fmt.Errorf("decoding %s: %w", m.Method, err)
		}
		return &protocol.PermissionsRequestApprovalResponse{
			Permissions: protocol.GrantedPermissionProfile(p.Permissions),
			Scope:       protocol.PermissionGrantScopeTurn,
		}, nil
	case protocol.MethodMcpServerElicitationRequest:
		// An MCP tool's confirmation. A form that needs fields fails
		// validation on an empty answer, which the tool reports to the
		// agent, rather than waiting.
		return &protocol.McpServerElicitationRequestResponse{
			Action: protocol.McpServerElicitationActionAccept, Content: json.RawMessage(`{}`),
		}, nil
	case protocol.MethodItemToolRequestUserInput:
		// No one to ask: no answers, and the agent carries on.
		return &protocol.ToolRequestUserInputResponse{Answers: map[string]protocol.ToolRequestUserInputAnswer{}}, nil
	}
	return nil, fmt.Errorf("%s is not supported by slop-bot", m.Method)
}
