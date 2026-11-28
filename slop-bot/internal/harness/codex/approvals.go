package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/codex/protocol"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/rpcdriver"
	"github.com/lowaa/slop-factory/slop-bot/internal/mergeguard"
)

// methodNotFound is JSON-RPC's code for a method the client doesn't offer.
const methodNotFound = -32601

// guardTimeout bounds one guard verdict. The guard answers in milliseconds;
// this only stops a wedged one (a hung git) holding the turn for ever.
const guardTimeout = 30 * time.Second

// screened is the guard's verdict on one command approval, and the turn
// that asked.
type screened struct {
	verdict        *mergeguard.Verdict
	thread, turnID string
}

// unreadable is the refusal of a command approval the guard can't read.
const unreadable = "slop-bot's merge guard couldn't read this command, so it was refused"

// screen is the merge guard's verdict on a command Codex asks to run, or
// nil when there is no guard or the request isn't a command's approval. It
// runs on rpcdriver's goroutine, which Codex's turn waits on anyway: every
// command is held until it is answered.
//
// Both of the protocol's command approvals are screened: turn/start's
// item/commandExecution/requestApproval, and the legacy turn APIs'
// execCommandApproval, which slop-bot's turns don't use but a permissive
// agent would otherwise approve unscreened. Patch approvals aren't: a patch
// only writes files, and running anything it writes takes a command, which
// is screened (docs/merge_guard.md).
func (c *codec) screen(m *message) *screened {
	if c.opts.guard == nil || m.ID == nil {
		return nil
	}
	call, err := commandOf(m)
	if err != nil {
		// Unlike the hook, which can only fail open on input it can't
		// read, this answer is what lets the command run: approving it
		// unclassified would pass a merge on any format change.
		c.opts.logger.Warn("codex: an unreadable command approval; refused", "method", m.Method, "err", err)
		return &screened{verdict: &mergeguard.Verdict{Reason: unreadable}}
	}
	if call == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), guardTimeout)
	defer cancel()
	v := c.opts.guard.Check(ctx, c.opts.by, call.call)
	return &screened{verdict: v, thread: call.thread, turnID: call.turnID}
}

// commandCall is the command one approval asks to run, and the turn that
// asks.
type commandCall struct {
	call           *mergeguard.Call
	thread, turnID string
}

// commandOf is the command a command approval asks to run, or nil for any
// other message.
func commandOf(m *message) (*commandCall, error) {
	switch m.Method {
	case protocol.MethodItemCommandExecutionRequestApproval:
		var p protocol.CommandExecutionRequestApprovalParams
		if err := decodeParams(m, &p); err != nil {
			return nil, err
		}
		// Codex sends the command as one line (`/bin/bash -lc '<cmd>'`),
		// which the classifier follows into. A writeStdin approval, should
		// Codex send one, is classified the same way; the spike saw it send
		// none for input typed into a running shell (docs/merge_guard.md).
		return &commandCall{
			call:   &mergeguard.Call{Tool: shellTool, Command: p.Command, Cwd: string(p.Cwd)},
			thread: p.ThreadID, turnID: p.TurnID,
		}, nil
	case protocol.MethodExecCommandApproval:
		var p protocol.ExecCommandApprovalParams
		if err := decodeParams(m, &p); err != nil {
			return nil, err
		}
		if len(p.Command) == 0 {
			return nil, fmt.Errorf("decoding %s: no command", m.Method)
		}
		// The legacy request names no turn, so a refusal is never steered;
		// its denial carries the reason instead (answer).
		return &commandCall{
			call:   &mergeguard.Call{Tool: shellTool, Command: mergeguard.ShellLine(p.Command), Cwd: p.Cwd},
			thread: string(p.ConversationID),
		}, nil
	}
	return nil, nil
}

// decodeParams decodes m's params into p; absent params are an error.
func decodeParams(m *message, p any) error {
	if len(m.Params) == 0 || string(m.Params) == "null" {
		return fmt.Errorf("decoding %s: no params", m.Method)
	}
	if err := json.Unmarshal(m.Params, p); err != nil {
		return fmt.Errorf("decoding %s: %w", m.Method, err)
	}
	return nil
}

// shellTool is Codex's shell tool, as the classifier knows it.
const shellTool = "exec_command"

// answer replies to a server request. A guarded agent's commands each come
// here, and s is the guard's verdict: a merge is declined, and the guard's
// reason steered into the turn, since Codex's decline carries no reason of
// its own (the agent reads only "rejected by user"). Everything else is
// answered the way the agent's mode means. An unguarded Codex runs with
// approvalPolicy "never" and sends none (protocol spike), but nobody is at
// a terminal to answer one, and unanswered it would hold the turn for ever:
// approved when permissive, declined otherwise, so a sandboxed agent is
// never escalated by a request that slipped through. mu is held.
func (c *codec) answer(w *rpcdriver.Wire, m *message, s *screened) error {
	if s != nil && !s.verdict.Allow {
		if m.Method == protocol.MethodExecCommandApproval {
			// The legacy denial carries its reason to the model, so no
			// steer is needed and nothing races.
			return c.reply(w, m, &protocol.ExecCommandApprovalResponse{Decision: deniedDecision(s.verdict.Reason)})
		}
		if !c.explainable(s) {
			return c.decline(w, m.ID)
		}
		// The reason goes first, and the decline only once Codex has taken
		// it (response's reqSteer): declined, Codex moves the turn on at
		// once, and a steer it takes during that next model call has it
		// reject the commands the call made, unasked ("rejected by user"),
		// ordinary ones too. Written back to back, the two still raced.
		_, err := c.request(w, &request{kind: reqSteer, msg: &harness.Message{Text: s.verdict.Reason}, turn: c.turnID, decline: m.ID},
			protocol.MethodTurnSteer, &protocol.TurnSteerParams{
				ThreadID: c.threadID, ExpectedTurnID: c.turnID, Input: textInput(s.verdict.Reason),
			})
		if err != nil {
			return errors.Join(err, c.decline(w, m.ID))
		}
		return nil
	}
	answer, outcome := decline, "declined"
	if c.opts.permissive {
		answer, outcome = approval, "approved"
	}
	result, err := answer(m)
	if err != nil {
		c.opts.logger.Warn("codex sent a request slop-bot can't answer; refused", "method", m.Method, "err", err)
		return write(w, &message{ID: m.ID, Error: &rpcError{Code: methodNotFound, Message: err.Error()}})
	}
	if c.opts.guard == nil {
		c.opts.logger.Warn("codex asked for approval despite approvalPolicy never; "+outcome, "method", m.Method)
	}
	return c.reply(w, m, result)
}

// explainable says a refusal's reason can be steered into the turn that
// asked: this conversation's running turn. Anywhere else the command is
// only declined: a sub-agent's thread isn't this one, so the reason would
// reach an agent that didn't run the command; and a turn being
// interrupted, or one not running, would have the reason start a turn of
// its own after the user stopped the agent (screen runs outside mu, so an
// interrupt can land during the guard's check). mu is held.
func (c *codec) explainable(s *screened) bool {
	if s.thread != c.threadID || s.turnID == "" || s.turnID != c.turnID || c.interrupted || c.starting {
		c.opts.logger.Info("codex: a merge refused outside the running turn; declined without the reason",
			"thread", s.thread, "turn", s.turnID)
		return false
	}
	return true
}

// decline answers a command's approval with a decline.
func (c *codec) decline(w *rpcdriver.Wire, id json.RawMessage) error {
	return c.reply(w, &message{ID: id}, &protocol.CommandExecutionRequestApprovalResponse{Decision: protocol.CommandExecutionApprovalDecisionDecline})
}

// reply answers a server request with result.
func (c *codec) reply(w *rpcdriver.Wire, m *message, result any) error {
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

// rejection is the reason a declined request gives the agent.
const rejection = "slop-bot runs this agent unattended and without permissive mode: nobody can approve it"

// decline is the refusing answer to one server request, for a
// non-permissive agent. It refuses without interrupting the turn, so the
// agent can try something else.
func decline(m *message) (any, error) {
	switch m.Method {
	case protocol.MethodItemCommandExecutionRequestApproval:
		return &protocol.CommandExecutionRequestApprovalResponse{Decision: protocol.CommandExecutionApprovalDecisionDecline}, nil
	case protocol.MethodItemFileChangeRequestApproval:
		return &protocol.FileChangeRequestApprovalResponse{Decision: protocol.FileChangeApprovalDecisionDecline}, nil
	case protocol.MethodApplyPatchApproval:
		return &protocol.ApplyPatchApprovalResponse{Decision: deniedDecision(rejection)}, nil
	case protocol.MethodExecCommandApproval:
		return &protocol.ExecCommandApprovalResponse{Decision: deniedDecision(rejection)}, nil
	case protocol.MethodItemPermissionsRequestApproval:
		// Nothing granted.
		return &protocol.PermissionsRequestApprovalResponse{Scope: protocol.PermissionGrantScopeTurn}, nil
	case protocol.MethodMcpServerElicitationRequest:
		return &protocol.McpServerElicitationRequestResponse{Action: protocol.McpServerElicitationActionDecline}, nil
	case protocol.MethodItemToolRequestUserInput:
		return &protocol.ToolRequestUserInputResponse{Answers: map[string]protocol.ToolRequestUserInputAnswer{}}, nil
	}
	return nil, fmt.Errorf("%s is not supported by slop-bot", m.Method)
}

// deniedDecision is ReviewDecision's denial, an object variant the
// generated `any` has no constant for.
func deniedDecision(reason string) any {
	return map[string]any{"denied": map[string]string{"rejection": reason}}
}
