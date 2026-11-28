// Package claudecode is the claude-code/stream-json harness: Claude Code run
// as `claude -p` with stream-json input and output, one long-lived process
// per conversation.
package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/google/uuid"
	"mvdan.cc/sh/v3/syntax"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/agentproc"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/rpcdriver"
)

// Name and Driver, as the config and tickets spell them.
const (
	Name   = "claude-code"
	Driver = "stream-json"
)

// Harness runs Claude Code.
type Harness struct {
	// Binary is the claude executable; "" finds it on PATH.
	Binary string
	Logger *slog.Logger
	Clock  clock.Clock
	// Environ is the base environment; nil means os.Environ.
	Environ func() []string
	// PreToolHook, when set, is the command (an argv) Claude Code runs
	// before each Bash or MCP tool call: slop-bot's merge guard, `slop-bot
	// hook pre-tool`. It goes in through --settings, which adds to the
	// user's own settings for this session only: nothing is written to
	// their files or the worktree.
	PreToolHook []string
}

// Name implements harness.AIHarness.
func (h *Harness) Name() string { return Name }

// Driver implements harness.AIHarness.
func (h *Harness) Driver() string { return Driver }

// Capabilities implements harness.AIHarness.
func (h *Harness) Capabilities() harness.Capabilities {
	return harness.Capabilities{MidTurnSteer: true, ChosenIDs: true}
}

// Start implements harness.AIHarness.
func (h *Harness) Start(ctx context.Context, ws *harness.Workspace, resume *harness.ConversationRef) (harness.Conversation, error) {
	bin, err := h.binary()
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	session := []string{"--session-id", id}
	if resume != nil {
		id = resume.ID
		session = []string{"--resume", id}
	}
	args := append([]string{
		"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
	}, session...)
	common, err := h.common(ws)
	if err != nil {
		return nil, err
	}
	args = append(args, common...)
	env := h.env(ws)
	spec := &harness.LaunchSpec{Path: bin, Args: args, Env: env, Dir: ws.Dir}
	return rpcdriver.Start(ctx, &rpcdriver.Options{
		Harness: Name, Driver: Driver, Codec: &codec{id: id}, Spec: spec,
		Logger: h.Logger, Clock: h.Clock,
		Attach: func(id string) *harness.LaunchSpec {
			return &harness.LaunchSpec{Path: bin, Args: append([]string{"--resume", id}, common...), Env: env, Dir: ws.Dir}
		},
	})
}

// common are the flags the programmatic session and its TUI hand-over share,
// so an attached person runs the same agent: same model, permissions,
// briefing, directories and merge guard.
func (h *Harness) common(ws *harness.Workspace) ([]string, error) {
	var args []string
	if len(h.PreToolHook) > 0 {
		settings, err := h.hookSettings()
		if err != nil {
			return nil, err
		}
		args = append(args, "--settings", settings)
	}
	if ws.Permissive {
		args = append(args, "--dangerously-skip-permissions")
	}
	if ws.Model != "" {
		args = append(args, "--model", ws.Model)
	}
	if ws.Effort != "" {
		args = append(args, "--effort", ws.Effort)
	}
	if ws.Briefing != "" {
		args = append(args, "--append-system-prompt", ws.Briefing)
	}
	for _, d := range ws.ExtraDirs {
		args = append(args, "--add-dir", d)
	}
	return args, nil
}

// hookTimeout is the hook's bound in seconds, in Claude Code's units. The
// hook answers in milliseconds; this only stops a wedged one holding the
// turn for Claude Code's default of a minute.
const hookTimeout = 30

// hookSettings is the --settings JSON that runs PreToolHook before Bash and
// MCP tools, where a merge can be: a shell command, or a forge's MCP tool.
func (h *Harness) hookSettings() (string, error) {
	quoted := make([]string, 0, len(h.PreToolHook))
	for _, a := range h.PreToolHook {
		q, err := syntax.Quote(a, syntax.LangBash)
		if err != nil {
			return "", fmt.Errorf("claudecode: quoting the pre-tool hook: %w", err)
		}
		quoted = append(quoted, q)
	}
	type command struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	type matcher struct {
		Matcher string    `json:"matcher"`
		Hooks   []command `json:"hooks"`
	}
	b, err := json.Marshal(map[string]any{"hooks": map[string]any{"PreToolUse": []matcher{{
		Matcher: "Bash|mcp__.*",
		Hooks:   []command{{Type: "command", Command: strings.Join(quoted, " "), Timeout: hookTimeout}},
	}}}})
	return string(b), err
}

func (h *Harness) env(ws *harness.Workspace) []string {
	return agentproc.WorkspaceEnv(h.Environ, ws, map[string]string{
		// Without it a long command becomes a background task mid-turn, and
		// turn boundaries stop meaning "the agent is done for now".
		"CLAUDE_CODE_DISABLE_BACKGROUND_TASKS": "1",
	})
}

func (h *Harness) binary() (string, error) {
	if h.Binary != "" {
		return h.Binary, nil
	}
	return exec.LookPath("claude")
}
