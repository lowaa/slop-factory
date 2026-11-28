package mergeguard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/lowaa/slop-factory/slop-bot/internal/api"
)

// HookOptions configures RunHook.
type HookOptions struct {
	// Ask puts the call to the daemon, as the agent (its token).
	Ask func(ctx context.Context, call *api.PreTool) (*api.PreToolVerdict, error)
	// Classifier decides alone when the daemon can't be asked.
	Classifier *Classifier
	Stdin      io.Reader
	Stdout     io.Writer
	// Stderr gets the hook's own warnings; the harness shows or logs them.
	Stderr io.Writer
}

// payload is a PreToolUse hook's input, in the shape Claude Code sends it
// (Codex's hooks are compatible, and slop-bot's pi extension sends the
// same).
type payload struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	Cwd       string          `json:"cwd"`
}

// hookOutput is a PreToolUse hook's answer. Only a refusal is written: an
// allow says nothing, so the harness's own permission rules still apply
// rather than being waved through.
type hookOutput struct {
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

// RunHook is `slop-bot hook pre-tool`: it reads the tool call from stdin,
// asks the daemon, and writes a refusal to stdout when the call may not run.
// If the daemon can't be asked it decides alone, refusing only merges, so a
// stopped daemon never blocks an agent's ordinary work.
func RunHook(ctx context.Context, opts *HookOptions) error {
	var in payload
	if err := json.NewDecoder(opts.Stdin).Decode(&in); err != nil {
		// Nothing to classify; refusing every call would stop the agent
		// over a harness's format change.
		_, _ = fmt.Fprintf(opts.Stderr, "slop-bot hook pre-tool: unreadable input, allowing: %v\n", err)
		return nil
	}
	call := &api.PreTool{Tool: in.ToolName, Command: command(in.ToolInput), Cwd: in.Cwd}
	verdict, err := opts.Ask(ctx, call)
	if err != nil {
		v := Unreachable(ctx, opts.Classifier, &Call{Tool: call.Tool, Command: call.Command, Cwd: call.Cwd}, err)
		verdict = &api.PreToolVerdict{Allow: v.Allow, Reason: v.Reason}
		if v.Finding != nil {
			verdict.Merge = string(v.Finding.Kind)
		}
	}
	if verdict.Allow {
		return nil
	}
	var out hookOutput
	out.HookSpecificOutput.HookEventName = "PreToolUse"
	out.HookSpecificOutput.PermissionDecision = "deny"
	out.HookSpecificOutput.PermissionDecisionReason = verdict.Reason
	return json.NewEncoder(opts.Stdout).Encode(&out)
}

// command is a shell tool's command: a string (Claude Code's Bash, pi's
// bash), or an argv (Codex's shell), quoted back into one command line.
func command(input json.RawMessage) string {
	var in struct {
		Command json.RawMessage `json:"command"`
	}
	if json.Unmarshal(input, &in) != nil || len(in.Command) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(in.Command, &s) == nil {
		return s
	}
	var argv []string
	if json.Unmarshal(in.Command, &argv) != nil {
		return ""
	}
	quoted := make([]string, 0, len(argv))
	for _, a := range argv {
		q, err := syntax.Quote(a, syntax.LangBash)
		if err != nil {
			q = a
		}
		quoted = append(quoted, q)
	}
	return strings.Join(quoted, " ")
}
