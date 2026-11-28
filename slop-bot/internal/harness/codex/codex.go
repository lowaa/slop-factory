// Package codex is the codex/app-server harness: OpenAI's Codex CLI run as
// `codex app-server`, JSON-RPC over stdio, one long-lived process per
// conversation. The protocol's types are generated (./protocol).
//
// It runs on the user's own Codex home ($CODEX_HOME, else ~/.codex): their
// login, config, and session history, with no setup. trust.go says why it
// isn't a per-agent home.
package codex

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/agentproc"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/codex/protocol"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/rpcdriver"
	"github.com/lowaa/slop-factory/slop-bot/internal/version"
)

// Name and Driver, as the config and tickets spell them.
const (
	Name   = "codex"
	Driver = "app-server"
)

// Harness runs Codex.
type Harness struct {
	// Binary is the codex executable; "" finds it on PATH.
	Binary string
	Logger *slog.Logger
	Clock  clock.Clock
	// Environ is the base environment; nil means os.Environ.
	Environ func() []string
	// Version is the client version reported to Codex; "" is slop-bot's.
	Version string
	// ModelProvider points Codex at another Responses API endpoint instead
	// of the user's own provider: tier 2's scripted model. Nil leaves the
	// user's configuration alone.
	ModelProvider *ModelProvider
	// Config are further `key=value` overrides of the user's config.toml,
	// for this session only (Codex's `-c`).
	Config []string
}

// ModelProvider is a model provider defined for one session, through
// Codex's `-c model_providers.<ID>...` overrides; nothing is written to the
// user's config. It speaks the Responses API: Codex POSTs
// <BaseURL>/responses (and GETs <BaseURL>/models). Codex sends no
// credentials to it unless EnvKey names a variable holding a key.
type ModelProvider struct {
	// ID is a TOML bare key: letters, digits, - and _.
	ID      string
	BaseURL string
	// EnvKey optionally names the environment variable with its API key.
	EnvKey string
}

var bareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func (p *ModelProvider) flags() ([]string, error) {
	if !bareKey.MatchString(p.ID) {
		return nil, fmt.Errorf("codex: model provider id %q is not a TOML bare key", p.ID)
	}
	pre := "model_providers." + p.ID + "."
	out := []string{
		"-c", "model_provider=" + tomlString(p.ID),
		"-c", pre + "name=" + tomlString(p.ID),
		"-c", pre + "base_url=" + tomlString(p.BaseURL),
		"-c", pre + "wire_api=" + tomlString("responses"),
	}
	if p.EnvKey != "" {
		out = append(out, "-c", pre+"env_key="+tomlString(p.EnvKey))
	}
	return out, nil
}

// Name implements harness.AIHarness.
func (h *Harness) Name() string { return Name }

// Driver implements harness.AIHarness.
func (h *Harness) Driver() string { return Driver }

// Capabilities implements harness.AIHarness: turn/steer reaches the running
// turn, and Codex, not slop-bot, chooses the thread id.
func (h *Harness) Capabilities() harness.Capabilities {
	return harness.Capabilities{MidTurnSteer: true, ChosenIDs: false}
}

// Start implements harness.AIHarness. A resume of a thread Codex has no
// rollout for fails with ErrNoRollout.
func (h *Harness) Start(ctx context.Context, ws *harness.Workspace, resume *harness.ConversationRef) (harness.Conversation, error) {
	bin, err := h.binary()
	if err != nil {
		return nil, err
	}
	common, err := h.common(ws)
	if err != nil {
		return nil, err
	}
	env := h.env(ws)
	opts := &codecOptions{logger: h.Logger, version: h.version(), effort: ws.Effort}
	opts.start, opts.resumeParams = threadParams(ws)
	if resume != nil {
		opts.resume = resume.ID
		opts.resumeParams.ThreadID = resume.ID
	}
	spec := &harness.LaunchSpec{Path: bin, Args: append([]string{"app-server"}, common...), Env: env, Dir: ws.Dir}
	return rpcdriver.Start(ctx, &rpcdriver.Options{
		Harness: Name, Driver: Driver, Codec: newCodec(opts), Spec: spec,
		Logger: h.Logger, Clock: h.Clock,
		Attach: func(id string) *harness.LaunchSpec {
			return &harness.LaunchSpec{Path: bin, Args: attachArgs(id, ws, common), Env: env, Dir: ws.Dir}
		},
	})
}

// threadParams are thread/start's and thread/resume's: the same settings,
// so a resumed thread runs as it started.
func threadParams(ws *harness.Workspace) (*protocol.ThreadStartParams, *protocol.ThreadResumeParams) {
	// Nobody is at a terminal to answer an approval, so Codex never asks,
	// permissive or not; Permissive also lifts the sandbox. Without it the
	// sandbox is whatever the user's config says.
	start := &protocol.ThreadStartParams{
		Cwd: ws.Dir, ApprovalPolicy: protocol.AskForApprovalNever, Model: ws.Model,
		// The briefing goes in as developer instructions: nothing is
		// written to the worktree, and the thread keeps it across a resume
		// and an attach (it is in the rollout once, protocol spike).
		DeveloperInstructions: ws.Briefing,
	}
	resume := &protocol.ThreadResumeParams{
		Cwd: ws.Dir, ApprovalPolicy: protocol.AskForApprovalNever, Model: ws.Model,
		DeveloperInstructions: ws.Briefing,
	}
	if ws.Permissive {
		start.Sandbox = protocol.SandboxModeDangerFullAccess
		resume.Sandbox = protocol.SandboxModeDangerFullAccess
	}
	return start, resume
}

// common are the flags the app-server and its TUI hand-over share, so an
// attached person runs the same agent: same provider, model, effort and
// directories.
func (h *Harness) common(ws *harness.Workspace) ([]string, error) {
	var args []string
	if h.ModelProvider != nil {
		f, err := h.ModelProvider.flags()
		if err != nil {
			return nil, err
		}
		args = append(args, f...)
	}
	for _, kv := range h.Config {
		args = append(args, "-c", kv)
	}
	if ws.Model != "" {
		args = append(args, "-c", "model="+tomlString(ws.Model))
	}
	if ws.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+tomlString(ws.Effort))
	}
	if len(ws.ExtraDirs) > 0 {
		roots := make([]string, len(ws.ExtraDirs))
		for i, d := range ws.ExtraDirs {
			roots[i] = tomlString(d)
		}
		args = append(args, "-c", "sandbox_workspace_write.writable_roots=["+strings.Join(roots, ",")+"]")
	}
	return args, nil
}

// attachArgs resume the thread in Codex's TUI. --no-daemon keeps the
// session off the user's own shared Codex server, so an agent neither
// shares its state nor depends on it.
func attachArgs(id string, ws *harness.Workspace, common []string) []string {
	args := []string{"resume", id, "--no-daemon"}
	if ws.Permissive {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	} else {
		args = append(args, "--ask-for-approval", "never")
	}
	return append(args, common...)
}

func (h *Harness) env(ws *harness.Workspace) []string {
	base := os.Environ
	if h.Environ != nil {
		base = h.Environ
	}
	return agentproc.AgentEnv(&agentproc.EnvOptions{Base: base(), APIKeyOptIn: ws.APIKeyOptIn, Set: ws.Env})
}

func (h *Harness) binary() (string, error) {
	if h.Binary != "" {
		return h.Binary, nil
	}
	return exec.LookPath("codex")
}

func (h *Harness) version() string {
	if h.Version != "" {
		return h.Version
	}
	return version.String()
}

// tomlString is s as a TOML basic string, for `-c` values: Codex parses
// each value as TOML. strconv.Quote is close but writes \x escapes, which
// TOML lacks.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			b.WriteString(`\u` + leftPad(strconv.FormatInt(int64(r), 16), 4))
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func leftPad(s string, n int) string {
	return strings.Repeat("0", n-len(s)) + s
}
