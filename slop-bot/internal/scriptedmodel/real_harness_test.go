package scriptedmodel_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/scriptedmodel"
)

// The seed of tier 2: each real harness CLI installed on this host plays one
// scripted turn that runs a shell command, then a final text reply, against
// an in-process server. Opt-in, since it needs the CLIs; it spends no tokens
// (the harnesses get dummy keys and talk only to the scripted model).
const realHarnessesEnv = "SLOP_BOT_TIER2"

// A bound on a stuck harness, not a wait for anything: each run finishes in
// seconds when it works.
const harnessRunLimit = 3 * time.Minute

// How long a killed run's leftover children may hold its output open.
const harnessWaitDelay = 10 * time.Second

const echoMarker = "scripted-echo-7f3a"

type realHarness struct {
	name string
	// The shell tool as the harness names it, and its input.
	tool  string
	input map[string]any
	// models the server lists, for a harness that picks from them.
	models []string
	// command builds the harness invocation; env is added to a clean
	// environment (HOME, PATH, TERM), and wins over it.
	command func(t *testing.T, serverURL, dir, prompt string) (args []string, env []string)
}

func realHarnesses() []*realHarness {
	return []*realHarness{
		{
			name:    "claude",
			tool:    "Bash",
			input:   map[string]any{"command": "echo " + echoMarker, "description": "Echo a marker"},
			command: claudeCommand,
		},
		{
			name:    "codex",
			tool:    "exec_command",
			input:   map[string]any{"cmd": "echo " + echoMarker},
			command: codexCommand,
		},
		{
			name:    "pi",
			tool:    "bash",
			input:   map[string]any{"command": "echo " + echoMarker},
			command: piCommand,
		},
		{
			name: "agy",
			tool: "run_command",
			input: map[string]any{
				"CommandLine": "echo " + echoMarker, "toolSummary": "Echo a marker", "WaitMsBeforeAsync": 5000,
			},
			// agy asks for this model when the gateway's list does not
			// change its default.
			models:  []string{agyModel},
			command: agyCommand,
		},
	}
}

func TestRealHarnesses(t *testing.T) {
	t.Parallel()
	if os.Getenv(realHarnessesEnv) != "1" {
		t.Skipf("set %s=1 to run the real harness CLIs against the scripted model", realHarnessesEnv)
	}
	for _, h := range realHarnesses() {
		t.Run(h.name, func(t *testing.T) {
			t.Parallel()
			runRealHarness(t, h)
		})
	}
}

func runRealHarness(t *testing.T, h *realHarness) {
	if _, err := exec.LookPath(h.name); err != nil {
		t.Skipf("%s is not installed", h.name)
	}
	server, err := scriptedmodel.New(&scriptedmodel.Options{Models: h.models})
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	final := "scripted-final-" + h.name
	require.NoError(t, server.Load(&scriptedmodel.Scenario{
		Name: "echo-then-reply",
		Turns: []*scriptedmodel.Turn{
			{ToolCalls: []*scriptedmodel.ToolCall{{Name: h.tool, Input: h.input}}, Usage: scriptedmodel.Usage{InputTokens: 11, OutputTokens: 3}},
			{Text: final, Usage: scriptedmodel.Usage{InputTokens: 17, OutputTokens: 5}},
		},
	}))

	dir := t.TempDir()
	args, env := h.command(t, server.URL(), dir, "Run the echo command, then reply.")
	ctx, cancel := context.WithTimeout(t.Context(), harnessRunLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) // #nosec G204 -- the test's own fixed harness commands
	cmd.Dir = dir
	cmd.Env = append([]string{"HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH"), "TERM=xterm"}, env...)
	cmd.Stdin = strings.NewReader("")
	// The harnesses' own children (shells, helpers) inherit the output pipes;
	// without a WaitDelay a killed run would still wait for them.
	cmd.WaitDelay = harnessWaitDelay
	out, runErr := cmd.CombinedOutput()

	rec := server.Recording()
	t.Logf("%s output:\n%s", h.name, out)
	for i, req := range rec.Requests {
		t.Logf("request %d: api=%s model=%s auxiliary=%v tools=%v", i, req.API, req.Model, req.Auxiliary, req.ToolNames)
	}
	require.NoError(t, runErr)
	assert.Empty(t, rec.Errors)
	assert.Zero(t, server.Remaining())
	conversation := rec.Conversation()
	require.GreaterOrEqual(t, len(conversation), 2)
	assert.Contains(t, strings.Join(conversation[1].ToolResults, "\n"), echoMarker,
		"the second request carries the echo's output as a tool result")
	assert.Contains(t, string(out), final)
}

func claudeCommand(t *testing.T, serverURL, _, prompt string) ([]string, []string) {
	// A config dir of its own keeps ~/.claude and ~/.claude.json untouched:
	// no projects dir, no trust entry to clean up.
	return []string{"claude", "-p", prompt, "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"},
		[]string{
			"CLAUDE_CONFIG_DIR=" + t.TempDir(),
			"ANTHROPIC_BASE_URL=" + serverURL,
			"ANTHROPIC_API_KEY=sk-ant-scripted-model-not-a-real-key",
			"DISABLE_AUTOUPDATER=1",
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
			"DISABLE_TELEMETRY=1",
			"DISABLE_ERROR_REPORTING=1",
			// A suggested next prompt is a request with tools that the script
			// has no turn for.
			"CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=false",
		}
}

func codexCommand(t *testing.T, serverURL, _, prompt string) ([]string, []string) {
	// A CODEX_HOME of its own keeps ~/.codex/config.toml untouched. The
	// model name is one Codex has no metadata for, which keeps its tools
	// plain (harness_e2e/harnesses/codex.py).
	provider := fmt.Sprintf(`model_providers.scripted={name="scripted",base_url="%s/v1",env_key="SCRIPTED_MODEL_API_KEY",wire_api="responses"}`, serverURL)
	return []string{
			"codex", "exec", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox",
			"-c", "model_provider=scripted", "-c", "model=scripted-model-1", "-c", provider,
			"-c", "check_for_update_on_startup=false", "-c", "analytics.enabled=false", "-c", "feedback.enabled=false",
			prompt,
		},
		[]string{"CODEX_HOME=" + t.TempDir(), "SCRIPTED_MODEL_API_KEY=scripted-model-not-a-real-key"}
}

func piCommand(t *testing.T, serverURL, _, prompt string) ([]string, []string) {
	agentDir := t.TempDir()
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
	writeJSONFile(t, filepath.Join(agentDir, "models.json"), models)
	writeJSONFile(t, filepath.Join(agentDir, "settings.json"), map[string]any{"enableInstallTelemetry": false, "collapseChangelog": true})
	return []string{"pi", "-p", "--provider", "scripted", "--model", "scripted-model-1", prompt},
		[]string{
			"PI_CODING_AGENT_DIR=" + agentDir,
			"SCRIPTED_MODEL_API_KEY=scripted-model-not-a-real-key",
			"PI_OFFLINE=1", "PI_SKIP_VERSION_CHECK=1", "PI_TELEMETRY=0",
		}
}

const agyModel = "gemini-3.1-pro-preview"

func agyCommand(t *testing.T, serverURL, dir, prompt string) ([]string, []string) {
	trusted := string(mustMarshal(map[string]any{"trustedWorkspaces": []string{dir}}))
	// A HOME of its own, for its ~/.gemini: agy resumes a conversation that
	// was cut off mid-turn the next time it starts, so a shared one would
	// replay an earlier run's turn against this run's script. The gateway
	// needs no Google sign-in, so an empty one works.
	return []string{"agy", "-p", prompt, "--dangerously-skip-permissions", "--settings", trusted},
		[]string{
			"HOME=" + t.TempDir(),
			"AGY_LLM_GATEWAY_URL=" + serverURL,
			"AGY_LLM_GATEWAY_API_KEY=scripted-model-not-a-real-key",
		}
}

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, mustMarshal(v), 0o600))
}

func mustMarshal(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
