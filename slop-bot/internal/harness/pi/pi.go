// Package pi is the pi/rpc harness: pi run as `pi --mode rpc`, one
// long-lived process per conversation, speaking JSONL commands and events.
//
// It runs on the user's own pi configuration (~/.pi/agent: login, settings,
// extensions, skills, trust decisions). The rpc protocol covers turns,
// steering, abort and state, and the agent reaches slop-bot through its
// CLI; the one extension of slop-bot's own is the merge guard's
// (merge_guard.ts), loaded with -e, which project trust doesn't gate.
package pi

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/agentproc"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/rpcdriver"
)

// Name and Driver, as the config and tickets spell them.
const (
	Name   = "pi"
	Driver = "rpc"
)

// DefaultModel is the model used when neither the workspace nor the Harness
// names one. --model is always passed because pi's own configured default
// can be one the user's login doesn't serve (the spike's gpt-5.5 was
// rejected on this ChatGPT plan), and an agent that fails its first request
// is worse than one on a known-good model.
const DefaultModel = "openai/gpt-6.2-sol"

// thinkingLevels are what pi's --thinking takes (pi --help, 1.4.0).
var thinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// Harness runs pi.
type Harness struct {
	// Binary is the pi executable; "" finds it on PATH.
	Binary string
	// DefaultModel overrides the package's DefaultModel for workspaces that
	// name none, e.g. from the user's config.
	DefaultModel string
	// AgentDir, when set, is pi's config directory (PI_CODING_AGENT_DIR)
	// instead of the user's ~/.pi/agent. It is the hook for tier 2: a temp
	// directory whose models.json defines a custom provider pointing at the
	// scripted model server, e.g.
	//
	//	{"providers":{"scripted":{"baseUrl":"http://127.0.0.1:<port>/v1",
	//	  "api":"openai-completions","apiKey":"not-a-real-key",
	//	  "models":[{"id":"scripted-model-1","reasoning":true,"input":["text"],
	//	    "contextWindow":128000,"maxTokens":8192,
	//	    "cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}}]}}}
	//
	// with the workspace's Model set to "scripted/scripted-model-1". Sessions
	// then live under that directory too. Production leaves it blank.
	AgentDir string
	Logger   *slog.Logger
	Clock    clock.Clock
	// Environ is the base environment; nil means os.Environ.
	Environ func() []string
	// PreToolHook, when set, is the command (an argv) pi runs before each
	// bash or MCP tool call, through slop-bot's extension: the merge guard,
	// `slop-bot hook pre-tool`. The extension is written beside the
	// briefing file, outside the worktree.
	PreToolHook []string
}

//go:embed merge_guard.ts
var mergeGuardExtension []byte

// hookEnv names the hook's argv, as JSON, for the extension.
const hookEnv = "SLOP_BOT_PRE_TOOL_HOOK"

// Name implements harness.AIHarness.
func (h *Harness) Name() string { return Name }

// Driver implements harness.AIHarness.
func (h *Harness) Driver() string { return Driver }

// Capabilities implements harness.AIHarness. A steer lands in the running
// run, after the current tool calls and before the next model call.
func (h *Harness) Capabilities() harness.Capabilities {
	return harness.Capabilities{MidTurnSteer: true, ChosenIDs: true}
}

// Start implements harness.AIHarness. pi's --session-id opens the session
// with that id in the cwd's project, creating it if it is missing, so a new
// conversation and a resumed one launch the same way.
func (h *Harness) Start(ctx context.Context, ws *harness.Workspace, resume *harness.ConversationRef) (harness.Conversation, error) {
	bin, err := h.binary()
	if err != nil {
		return nil, err
	}
	flags, err := h.common(ws)
	if err != nil {
		return nil, err
	}
	env := h.env(ws)
	id := uuid.NewString()
	if resume != nil {
		id = resume.ID
	}
	args := append([]string{"--mode", "rpc", "--session-id", id}, flags...)
	spec := &harness.LaunchSpec{Path: bin, Args: args, Env: env, Dir: ws.Dir}
	h.Logger.Info("pi: starting", "agent", ws.Agent, "session", id, "model", h.model(ws), "resume", resume != nil)
	return rpcdriver.Start(ctx, &rpcdriver.Options{
		Harness: Name, Driver: Driver,
		Codec: newCodec(id, resume != nil, h.Logger),
		Spec:  spec, Logger: h.Logger, Clock: h.Clock,
		Attach: func(id string) *harness.LaunchSpec {
			return &harness.LaunchSpec{Path: bin, Args: append([]string{"--session", id}, flags...), Env: env, Dir: ws.Dir}
		},
	})
}

// model is the workspace's model, or the configured default.
func (h *Harness) model(ws *harness.Workspace) string {
	switch {
	case ws.Model != "":
		return ws.Model
	case h.DefaultModel != "":
		return h.DefaultModel
	default:
		return DefaultModel
	}
}

// common are the flags the rpc session and its TUI hand-over share, so an
// attached person runs the same agent: same model, thinking and briefing.
// pi has no permission prompts and no directory allowlist, so Permissive and
// ExtraDirs need no flag.
func (h *Harness) common(ws *harness.Workspace) ([]string, error) {
	args := []string{"--model", h.model(ws)}
	if ws.Effort != "" {
		if !slices.Contains(thinkingLevels, ws.Effort) {
			return nil, fmt.Errorf("pi: effort %q is not a thinking level (%v)", ws.Effort, thinkingLevels)
		}
		args = append(args, "--thinking", ws.Effort)
	}
	if b := briefingArg(ws); b != "" {
		args = append(args, "--append-system-prompt", b)
	}
	if len(h.PreToolHook) > 0 {
		ext, err := writeGuardExtension(ws)
		if err != nil {
			return nil, err
		}
		args = append(args, "--extension", ext)
	}
	return args, nil
}

// writeGuardExtension writes the merge guard's extension beside the
// briefing file, the agent's own directory, and returns its path.
func writeGuardExtension(ws *harness.Workspace) (string, error) {
	if ws.BriefingFile == "" {
		return "", errors.New("pi: the merge guard's extension goes beside the briefing file, and the workspace has none")
	}
	path := filepath.Join(filepath.Dir(ws.BriefingFile), "merge-guard.ts")
	if err := os.WriteFile(path, mergeGuardExtension, 0o600); err != nil {
		return "", fmt.Errorf("pi: writing the merge guard's extension: %w", err)
	}
	return path, nil
}

// briefingArg prefers the briefing file: pi reads an argument that names an
// existing file as that file's contents, which keeps a long briefing clear
// of the kernel's 128KiB limit on one argument. The file is outside the
// worktree, so nothing is written there.
func briefingArg(ws *harness.Workspace) string {
	if ws.BriefingFile != "" {
		if _, err := os.Stat(ws.BriefingFile); err == nil {
			return ws.BriefingFile
		}
	}
	return ws.Briefing
}

func (h *Harness) env(ws *harness.Workspace) []string {
	set := map[string]string{}
	if h.AgentDir != "" {
		set["PI_CODING_AGENT_DIR"] = h.AgentDir
	}
	if len(h.PreToolHook) > 0 {
		b, _ := json.Marshal(h.PreToolHook) // a []string always encodes
		set[hookEnv] = string(b)
	}
	return agentproc.WorkspaceEnv(h.Environ, ws, set)
}

func (h *Harness) binary() (string, error) {
	if h.Binary != "" {
		return h.Binary, nil
	}
	return exec.LookPath("pi")
}
