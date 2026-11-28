// Package claudecode is the claude-code/stream-json harness: Claude Code run
// as `claude -p` with stream-json input and output, one long-lived process
// per conversation.
package claudecode

import (
	"context"
	"log/slog"
	"os"
	"os/exec"

	"github.com/google/uuid"

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
	args = append(args, common(ws)...)
	env := h.env(ws)
	spec := &harness.LaunchSpec{Path: bin, Args: args, Env: env, Dir: ws.Dir}
	return rpcdriver.Start(ctx, &rpcdriver.Options{
		Harness: Name, Driver: Driver, Codec: &codec{id: id}, Spec: spec,
		Logger: h.Logger, Clock: h.Clock,
		Attach: func(id string) *harness.LaunchSpec {
			return &harness.LaunchSpec{Path: bin, Args: append([]string{"--resume", id}, common(ws)...), Env: env, Dir: ws.Dir}
		},
	})
}

// common are the flags the programmatic session and its TUI hand-over share,
// so an attached person runs the same agent: same model, permissions,
// briefing and directories.
func common(ws *harness.Workspace) []string {
	var args []string
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
	return args
}

func (h *Harness) env(ws *harness.Workspace) []string {
	base := os.Environ
	if h.Environ != nil {
		base = h.Environ
	}
	set := map[string]string{
		// Without it a long command becomes a background task mid-turn, and
		// turn boundaries stop meaning "the agent is done for now".
		"CLAUDE_CODE_DISABLE_BACKGROUND_TASKS": "1",
	}
	for k, v := range ws.Env {
		set[k] = v
	}
	return agentproc.AgentEnv(&agentproc.EnvOptions{Base: base(), APIKeyOptIn: ws.APIKeyOptIn, Set: set})
}

func (h *Harness) binary() (string, error) {
	if h.Binary != "" {
		return h.Binary, nil
	}
	return exec.LookPath("claude")
}
