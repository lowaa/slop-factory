package pi

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/scriptedmodel"
)

// scriptedEnv is the scripted model's opt-in for running real harness CLIs
// (scriptedmodel/real_harness_test.go). The real pi runs against the
// in-process scripted model through Harness.AgentDir: no tokens, and nothing
// touches the user's ~/.pi.
const scriptedEnv = "SCRIPTED_MODEL_REAL_HARNESSES"

const scriptedModel = "scripted-model-1"

// scriptedAgentDir writes the custom provider tier 2 uses: OpenAI chat
// completions at the scripted model, with a dummy key from the environment.
func scriptedAgentDir(t *testing.T, serverURL string) string {
	t.Helper()
	dir := t.TempDir()
	models := map[string]any{"providers": map[string]any{"scripted": map[string]any{
		"baseUrl": serverURL + "/v1",
		"api":     "openai-completions",
		"apiKey":  "$SCRIPTED_MODEL_API_KEY",
		"models": []any{map[string]any{
			"id": scriptedModel, "name": scriptedModel, "reasoning": true, "input": []string{"text"},
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

func scripted(t *testing.T) (*Harness, *harness.Workspace, *scriptedmodel.Server) {
	t.Helper()
	if os.Getenv(scriptedEnv) != "1" {
		t.Skipf("set %s=1 to run the real pi against the scripted model", scriptedEnv)
	}
	if _, err := exec.LookPath("pi"); err != nil {
		t.Skip("pi is not installed")
	}
	server, err := scriptedmodel.New(&scriptedmodel.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	h := &Harness{AgentDir: scriptedAgentDir(t, server.URL()), Logger: slog.New(slog.DiscardHandler), Clock: clock.Real{}}
	ws := &harness.Workspace{
		Dir: t.TempDir(), Model: "scripted/" + scriptedModel, Effort: "low",
		Env: map[string]string{"SCRIPTED_MODEL_API_KEY": "scripted-model-not-a-real-key", "PI_OFFLINE": "1", "PI_TELEMETRY": "0"},
	}
	return h, ws, server
}

func TestScriptedTurnSteerInterruptAndResume(t *testing.T) {
	t.Parallel()
	h, ws, server := scripted(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	conv, err := h.Start(ctx, ws, nil)
	require.NoError(t, err)
	defer func() { _ = conv.Stop(context.Background(), true) }()
	until(ctx, t, conv, harness.Ready, nil)

	// A tool call, then a reply held at a gate, so the steer is sent while
	// pi is mid-run; it reaches the model in the same turn, as a third call.
	require.NoError(t, server.Load(&scriptedmodel.Scenario{Name: "steer", Turns: []*scriptedmodel.Turn{
		{ToolCalls: []*scriptedmodel.ToolCall{{Name: "bash", Input: map[string]any{"command": "echo hi"}}}, Usage: scriptedmodel.Usage{InputTokens: 11, OutputTokens: 3}},
		{Text: "first", Gate: "reply", Usage: scriptedmodel.Usage{InputTokens: 17, OutputTokens: 5}},
		{Text: "after-steer", Usage: scriptedmodel.Usage{InputTokens: 19, OutputTokens: 7}},
	}}))
	require.NoError(t, conv.Send(ctx, &harness.Message{Text: "remember KUMQUAT"}))
	until(ctx, t, conv, harness.ToolStarted, nil)
	require.NoError(t, server.WaitHeld(ctx, "reply"))
	require.NoError(t, conv.Send(ctx, &harness.Message{Text: "STEERED-IN"}))
	server.Release("reply")
	var text strings.Builder
	var usage *harness.Usage
	ended := until(ctx, t, conv, harness.TurnEnded, func(e harness.Event) {
		text.WriteString(e.Text)
		if e.Kind == harness.UsageReport {
			usage = e.Usage
		}
	})
	assert.Equal(t, harness.TurnCompleted, ended.Outcome, ended.Text)
	assert.Equal(t, "firstafter-steer", text.String())
	require.NotNil(t, usage)
	assert.Equal(t, int64(3+5+7), usage.OutputTokens, "the whole run's usage")
	assert.Zero(t, server.Remaining())
	reqs := server.Recording().Conversation()
	assert.Contains(t, reqs[len(reqs)-1].MessageText, "STEERED-IN")

	// An abort while the model call is held is an interrupt.
	require.NoError(t, server.Load(&scriptedmodel.Scenario{Name: "abort", Turns: []*scriptedmodel.Turn{
		{Text: "never", Gate: "held"},
	}}))
	require.NoError(t, conv.Send(ctx, &harness.Message{Text: "wait"}))
	require.NoError(t, server.WaitHeld(ctx, "held"))
	require.NoError(t, conv.Interrupt(ctx))
	assert.Equal(t, harness.TurnInterrupted, until(ctx, t, conv, harness.TurnEnded, nil).Outcome)

	ref := conv.Ref()
	require.NoError(t, conv.Stop(ctx, false))
	assert.Equal(t, 0, until(ctx, t, conv, harness.Exited, nil).ExitCode)

	// A new process on the same id sends the earlier conversation.
	require.NoError(t, server.Load(&scriptedmodel.Scenario{Name: "resume", Turns: []*scriptedmodel.Turn{{Text: "back"}}}))
	resumed, err := h.Start(ctx, ws, &ref)
	require.NoError(t, err)
	defer func() { _ = resumed.Stop(context.Background(), true) }()
	until(ctx, t, resumed, harness.Ready, nil)
	require.NoError(t, resumed.Send(ctx, &harness.Message{Text: "and now?"}))
	assert.Equal(t, harness.TurnCompleted, until(ctx, t, resumed, harness.TurnEnded, nil).Outcome)
	reqs = server.Recording().Conversation()
	require.NotEmpty(t, reqs)
	assert.Contains(t, reqs[0].MessageText, "remember KUMQUAT")
	assert.Contains(t, reqs[0].MessageText, "STEERED-IN")
	require.NoError(t, resumed.Stop(ctx, false))
}
