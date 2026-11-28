package scripted_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/cli"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/conformance/scripted"
	"github.com/lowaa/slop-factory/slop-bot/internal/scriptedmodel"
)

// hookReexec makes this test binary the slop-bot CLI when a harness runs it
// as its pre-tool hook, so the real `slop-bot hook pre-tool` answers.
const hookReexec = "SLOP_BOT_TIER2_HOOK_REEXEC"

// execIfHook runs the CLI and exits, when this process is a hook.
func execIfHook() {
	if os.Getenv(hookReexec) != "1" {
		return
	}
	deps, err := cli.FromOS()
	if err != nil {
		os.Exit(1)
	}
	os.Exit(cli.Execute(context.Background(), deps, os.Args[1:]))
}

// guardWorkspace is a workspace whose hook finds no daemon (an empty
// runtime dir), so the guard decides alone: merges refused, the rest run.
// That is the real hook's own path, end to end through the harness.
func guardWorkspace(t *testing.T) (*harness.Workspace, []string) {
	t.Helper()
	self, err := os.Executable()
	require.NoError(t, err)
	brief := filepath.Join(t.TempDir(), "briefing.md")
	require.NoError(t, os.WriteFile(brief, []byte("You are a test agent."), 0o600))
	return &harness.Workspace{
		Agent: "a1", Ticket: "7", Dir: t.TempDir(), Permissive: true,
		Briefing: "You are a test agent.", BriefingFile: brief,
		Env: map[string]string{hookReexec: "1", "XDG_RUNTIME_DIR": t.TempDir()},
	}, []string{self, "hook", "pre-tool"}
}

// guardScript has the model try a merge, then an ordinary command, then
// stop.
func guardScript(tool string, input func(string) map[string]any) *scriptedmodel.Scenario {
	return &scriptedmodel.Scenario{Name: "merge-guard", Turns: []*scriptedmodel.Turn{
		{ToolCalls: []*scriptedmodel.ToolCall{{Name: tool, Input: input("gh pr merge 12 --squash; echo MERGED-IT")}}},
		{ToolCalls: []*scriptedmodel.ToolCall{{Name: tool, Input: input("echo ORDINARY-RAN")}}},
		{Text: "stopping"},
	}}
}

// runGuardTurn sends one message and waits for the turn to end.
func runGuardTurn(t *testing.T, h harness.AIHarness, ws *harness.Workspace) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*stepTimeout)
	defer cancel()
	conv, err := h.Start(ctx, ws, nil)
	require.NoError(t, err)
	defer func() { _ = conv.Stop(context.Background(), true) }()
	require.NoError(t, conv.Send(ctx, &harness.Message{Text: "merge the PR"}))
	for {
		select {
		case ev, ok := <-conv.Events():
			require.True(t, ok, "the session ended mid-turn")
			if ev.Kind == harness.TurnEnded {
				require.Equal(t, harness.TurnCompleted, ev.Outcome, ev.Text)
				return
			}
		case <-ctx.Done():
			require.FailNow(t, "the turn didn't end")
		}
	}
}

// assertGuarded checks what the model was sent back: the merge refused by
// the guard (and not run), the ordinary command run.
func assertGuarded(t *testing.T, server *scriptedmodel.Server) {
	t.Helper()
	var results []string
	for _, r := range server.Recording().Conversation() {
		results = append(results, r.ToolResults...)
	}
	all := strings.Join(results, "\n")
	assert.Contains(t, all, "merge guard refused this", "the hook's reason reaches the model")
	assert.NotContains(t, all, "MERGED-IT", "the refused command never ran")
	assert.Contains(t, all, "ORDINARY-RAN", "anything else runs")
	assert.Zero(t, server.Remaining())
}

// guardTest runs the merge guard's script on one harness. Permissive is
// --dangerously-skip-permissions for Claude Code: the hook's deny must hold
// even there.
func guardTest(t *testing.T, name string, target newTarget) {
	t.Helper()
	bin := binary(t, name)
	server, err := scriptedmodel.New(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	ws, hook := guardWorkspace(t)
	tg := target(t, &scripted.TargetOptions{Binary: bin, ServerURL: server.URL(), PreToolHook: hook})
	require.NoError(t, server.Load(guardScript(tg.Shell.Tool, tg.Shell.Input)))
	tg.Workspace(ws)
	runGuardTurn(t, tg.Harness, ws)
	assertGuarded(t, server)
}

func TestClaudeCodeMergeGuard(t *testing.T) {
	t.Parallel()
	optIn(t)
	guardTest(t, "claude", scripted.ClaudeCode)
}

func TestPiMergeGuard(t *testing.T) {
	t.Parallel()
	optIn(t)
	guardTest(t, "pi", scripted.Pi)
}
