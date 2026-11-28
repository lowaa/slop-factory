package codex

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/scriptedmodel"
)

// scriptedEnv is scriptedmodel's opt-in for tests that run real harness
// CLIs against it: they need codex installed, and spend no tokens.
const scriptedEnv = "SCRIPTED_MODEL_REAL_HARNESSES"

// TestScriptedModelTurn is the seam tier 2 builds on: the harness, pointed
// at the scripted model through ModelProvider, with a Codex home of its own
// (so neither the user's login nor config is touched), runs a scripted turn
// with a tool call. The conformance suite against it is task 7.4.
func TestScriptedModelTurn(t *testing.T) {
	if os.Getenv(scriptedEnv) != "1" {
		t.Skipf("set %s=1 to run codex against the scripted model", scriptedEnv)
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex is not installed")
	}
	t.Parallel()
	server, err := scriptedmodel.New(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	require.NoError(t, server.Load(&scriptedmodel.Scenario{Name: "codex-turn", Turns: []*scriptedmodel.Turn{
		{ToolCalls: []*scriptedmodel.ToolCall{{Name: "exec_command", Input: map[string]any{"cmd": "echo scripted-7f3a"}}}},
		{Text: "DONE", Usage: scriptedmodel.Usage{InputTokens: 100, OutputTokens: 7, CacheReadInputTokens: 40}},
	}}))

	home := t.TempDir()
	h := &Harness{
		Logger: slog.New(slog.DiscardHandler), Clock: clock.Real{},
		Environ: func() []string {
			return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "CODEX_HOME=" + home, "SCRIPTED_MODEL_API_KEY=not-a-real-key"}
		},
		ModelProvider: &ModelProvider{ID: "scripted", BaseURL: server.URL() + "/v1", EnvKey: "SCRIPTED_MODEL_API_KEY"},
		// A model Codex has no metadata for keeps its tools plain
		// (scriptedmodel's README).
		Config: []string{"check_for_update_on_startup=false", "analytics.enabled=false", "feedback.enabled=false"},
	}
	ws := &harness.Workspace{Dir: t.TempDir(), Permissive: true, Model: "scripted-model-1", Briefing: "BRIEFING-MARKER-91"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	conv, err := h.Start(ctx, ws, nil)
	require.NoError(t, err)
	defer func() { _ = conv.Stop(context.Background(), true) }()

	r := &recorder{t: t, ctx: ctx, conv: conv}
	r.reset()
	r.until(harness.Ready)
	r.send("run the echo")
	ended := r.until(harness.TurnEnded)
	assert.Equal(t, harness.TurnCompleted, ended.Outcome, ended.Text)
	assert.Equal(t, 1, r.seen[harness.ToolStarted])
	assert.Equal(t, 1, r.seen[harness.ToolEnded])
	assert.Contains(t, r.text.String(), "DONE")
	assert.Equal(t, 1, r.seen[harness.UsageReport])

	rec := server.Recording()
	assert.Empty(t, rec.Errors)
	conversation := rec.Conversation()
	require.Len(t, conversation, 2)
	assert.Contains(t, conversation[0].System+conversation[0].MessageText, "BRIEFING-MARKER-91", "the briefing reached the model")
	assert.True(t, strings.Contains(strings.Join(conversation[1].ToolResults, "\n"), "scripted-7f3a"), "the tool ran")

	require.NoError(t, conv.Stop(ctx, false))
	assert.Equal(t, 0, r.until(harness.Exited).ExitCode)
}
