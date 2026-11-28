package scripted

import (
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/claudecode"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/codex"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/pi"
)

// Tier 2's switches: the suites that run real harness CLIs are opt-in.
const (
	// Tier2Env opts in (tier2.sh sets it).
	Tier2Env = "SLOP_BOT_TIER2"
	// ContainerEnv is set by tier2.sh's Docker mode, where every harness
	// must be installed at its pinned version and the network is cut.
	ContainerEnv = "SLOP_BOT_TIER2_CONTAINER"
)

// OptIn skips t unless tier 2 was asked for.
func OptIn(t *testing.T) {
	t.Helper()
	if os.Getenv(Tier2Env) != "1" {
		t.Skipf("set %s=1 to run the real harnesses against the scripted model (tier2.sh)", Tier2Env)
	}
}

// Binary finds a harness CLI: a skip on a dev box without it, a failure in
// the container, which installs them all.
func Binary(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		if os.Getenv(ContainerEnv) == "1" {
			require.NoError(t, err, "the tier 2 image installs %s", name)
		}
		t.Skipf("%s is not installed", name)
	}
	return path
}

// Target is one real harness CLI set up against a scripted model: in
// config and home directories of its own, so nothing of the user's (login,
// config, sessions) is read or written, and on a dummy key. It is the one
// place each harness's tier-2 setup lives, for the conformance suite and the
// daemon scenarios alike.
type Target struct {
	// Name is the harness's, as the config and tickets spell it.
	Name string
	// Shell is its model's shell tool.
	Shell *Shell
	// Model is what its workspaces must run on, "" for the harness's own
	// choice: the scripted provider's model (pi), or one Codex has no
	// metadata for, which keeps its tools plain (scriptedmodel's README).
	Model string
	// APIKeyOptIn is what its workspaces need: the dummy key is in the base
	// environment, which agentproc passes on only to an opted-in workspace,
	// the production path for an API-key agent.
	APIKeyOptIn bool
	// Env is what each workspace's Env must add.
	Env map[string]string
	// Guarded says its driver runs the pre-tool hook (the merge guard).
	// Codex's hooks don't run under app-server (docs/merge_guard.md).
	Guarded bool
	// Harness is the adapter.
	Harness harness.AIHarness
}

// TargetOptions are what a Target is built from.
type TargetOptions struct {
	// Binary is the CLI, or a Puppet's Launcher for it.
	Binary string
	// ServerURL is the scripted model's.
	ServerURL string
	// PreToolHook is the merge guard's argv, for the harnesses that run one.
	PreToolHook []string
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// environ is a harness's whole base environment: nothing of the user's
// beyond PATH, so no credential or config of theirs can reach it.
func environ(home string, extra ...string) func() []string {
	base := append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TERM=xterm"}, extra...)
	return func() []string { return append([]string(nil), base...) }
}

// Each harness's shell tool, as its model calls it.
var (
	ClaudeCodeShell = &Shell{Tool: "Bash", Input: func(cmd string) map[string]any {
		return map[string]any{"command": cmd, "description": "Run it"}
	}}
	// exec_command hands the model a still-running command back after
	// yield_time_ms, capped at 30s (its schema, 0.172.0): the most a long
	// tool can last. The suites release one within moments of it starting.
	CodexShell = &Shell{Tool: "exec_command", Input: func(cmd string) map[string]any {
		return map[string]any{"cmd": cmd, "yield_time_ms": 30000}
	}}
	PiShell = &Shell{Tool: "bash", Input: func(cmd string) map[string]any {
		return map[string]any{"command": cmd}
	}}
)

// ClaudeKey is the dummy Anthropic key Claude Code runs on.
const ClaudeKey = "sk-ant-scripted-model-not-a-real-key"

// ClaudeCode is Claude Code on the Anthropic Messages API.
func ClaudeCode(t *testing.T, o *TargetOptions) *Target {
	t.Helper()
	config := t.TempDir()
	seedClaudeConfig(t, config)
	return &Target{
		Name: claudecode.Name, APIKeyOptIn: true, Guarded: true,
		Shell: ClaudeCodeShell,
		Env: map[string]string{
			// A config dir of its own: nothing is written to ~/.claude or
			// ~/.claude.json, and no login of the user's is seen.
			"CLAUDE_CONFIG_DIR":   config,
			"ANTHROPIC_BASE_URL":  o.ServerURL,
			"DISABLE_AUTOUPDATER": "1", "DISABLE_TELEMETRY": "1", "DISABLE_ERROR_REPORTING": "1",
			// CLAUDE_CODE_* is dropped from the base environment (a parent
			// session's), so these go in the workspace's.
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
			// A suggested next prompt is a request with tools that the
			// script has no turn for.
			"CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION": "false",
		},
		Harness: &claudecode.Harness{
			Binary: o.Binary, Logger: discard(), Clock: clock.Real{}, PreToolHook: o.PreToolHook,
			Environ: environ(t.TempDir(), "ANTHROPIC_API_KEY="+ClaudeKey),
		},
	}
}

// seedClaudeConfig answers, in the config dir, the first-run questions
// Claude Code's TUI asks before anything else (theme, the API key, bypass
// mode), as a user who has run it before has. `claude -p` asks none of them;
// the TUI an attach runs asks each once. The folder-trust prompt, which an
// attach meets for every new worktree, is TrustClaudeFolder's.
func seedClaudeConfig(t *testing.T, dir string) {
	t.Helper()
	for name, v := range map[string]any{
		".claude.json": map[string]any{
			"hasCompletedOnboarding": true,
			// Claude Code records an approved key by its last 20 characters.
			"customApiKeyResponses": map[string]any{"approved": []string{ClaudeKey[len(ClaudeKey)-20:]}, "rejected": []string{}},
		},
		"settings.json": map[string]any{"theme": "dark", "skipDangerousModePermissionPrompt": true},
	} {
		b, err := json.Marshal(v)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), b, 0o600))
	}
}

// TrustClaudeFolder records, in the target's config dir, that the user
// trusted dir, as answering Claude Code's folder-trust prompt does. It must
// run before any Claude Code session there starts: one writes back the
// folder's entry as it read it.
//
// Answering the prompt on the screen instead is a race: Claude Code queries
// the terminal as it starts (`CSI ? u`, an OSC), and a pty that never
// answers sees the dialog re-mounted some 150ms later, its selection reset,
// so a move made before that is lost and an Enter after it picks "No, exit".
func (tg *Target) TrustClaudeFolder(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(tg.Env["CLAUDE_CONFIG_DIR"], ".claude.json")
	raw, err := os.ReadFile(path) // #nosec G304 -- the test's own config dir
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(raw, &cfg))
	projects, _ := cfg["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
	}
	projects[dir] = map[string]any{"hasTrustDialogAccepted": true}
	cfg["projects"] = projects
	out, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, out, 0o600))
}

// Codex is Codex on the OpenAI Responses API.
func Codex(t *testing.T, o *TargetOptions) *Target {
	t.Helper()
	return &Target{
		Name: codex.Name, Model: "scripted-model-1",
		Shell: CodexShell,
		Env:   map[string]string{},
		Harness: &codex.Harness{
			Binary: o.Binary, Logger: discard(), Clock: clock.Real{},
			// A CODEX_HOME of its own: the user's login, config and
			// sessions are never read or written.
			Environ:       environ(t.TempDir(), "CODEX_HOME="+t.TempDir(), "SCRIPTED_MODEL_API_KEY=scripted-model-not-a-real-key"),
			ModelProvider: &codex.ModelProvider{ID: "scripted", BaseURL: o.ServerURL + "/v1", EnvKey: "SCRIPTED_MODEL_API_KEY"},
			Config:        []string{"check_for_update_on_startup=false", "analytics.enabled=false", "feedback.enabled=false"},
		},
	}
}

// Pi is pi on OpenAI chat completions.
func Pi(t *testing.T, o *TargetOptions) *Target {
	t.Helper()
	return &Target{
		Name: pi.Name, Model: "scripted/scripted-model-1", Guarded: true,
		Shell: PiShell,
		Env: map[string]string{
			"SCRIPTED_MODEL_API_KEY": "scripted-model-not-a-real-key",
			"PI_OFFLINE":             "1", "PI_SKIP_VERSION_CHECK": "1", "PI_TELEMETRY": "0",
		},
		Harness: &pi.Harness{
			Binary: o.Binary, Logger: discard(), Clock: clock.Real{}, PreToolHook: o.PreToolHook,
			// An agent dir of its own: the user's ~/.pi/agent (login,
			// settings, sessions) is never read or written. pi's launcher
			// still finds its install under the real HOME.
			AgentDir: piAgentDir(t, o.ServerURL),
			Environ:  environ(os.Getenv("HOME")),
		},
	}
}

// piAgentDir defines the scripted model as a pi provider: OpenAI chat
// completions, with the dummy key from the environment.
func piAgentDir(t *testing.T, serverURL string) string {
	t.Helper()
	dir := t.TempDir()
	models := map[string]any{"providers": map[string]any{"scripted": map[string]any{
		"baseUrl": serverURL + "/v1",
		"api":     "openai-completions",
		"apiKey":  "$SCRIPTED_MODEL_API_KEY",
		"models": []any{map[string]any{
			"id": "scripted-model-1", "name": "scripted-model-1", "reasoning": true, "input": []string{"text"},
			"contextWindow": 128000, "maxTokens": 8192,
			"cost": map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0},
		}},
	}}}
	for name, v := range map[string]any{
		"models.json":   models,
		"settings.json": map[string]any{"enableInstallTelemetry": false, "collapseChangelog": true},
	} {
		b, err := json.Marshal(v)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), b, 0o600))
	}
	return dir
}

// Workspace adds what the target needs to ws: its model, its key's opt-in,
// and its variables, over what ws.Env already holds.
func (tg *Target) Workspace(ws *harness.Workspace) {
	if tg.Model != "" {
		ws.Model = tg.Model
	}
	ws.APIKeyOptIn = ws.APIKeyOptIn || tg.APIKeyOptIn
	if ws.Env == nil {
		ws.Env = map[string]string{}
	}
	for k, v := range tg.Env {
		ws.Env[k] = v
	}
}
