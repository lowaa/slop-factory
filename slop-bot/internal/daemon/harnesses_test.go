package daemon

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/claudecode"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/codex"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/pi"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/mergeguard"
)

// TestEveryHarnessIsGuarded: Claude Code and pi run the hook, Codex asks
// the guard in-process (docs/merge_guard.md).
func TestEveryHarnessIsGuarded(t *testing.T) {
	t.Parallel()
	var asked []*mergeguard.Call
	guard := guardFunc(func(_ context.Context, _ ident.Principal, call *mergeguard.Call) *mergeguard.Verdict {
		asked = append(asked, call)
		return &mergeguard.Verdict{Allow: true}
	})
	o := &buildOptions{preToolHook: []string{"/bin/slop-bot", "hook", "pre-tool"}, guard: guard}

	cc, ok := builders[[2]string{config.ClaudeCode, claudecode.Driver}](o).(*claudecode.Harness)
	require.True(t, ok)
	assert.Equal(t, o.preToolHook, cc.PreToolHook)

	p, ok := builders[[2]string{config.Pi, pi.Driver}](o).(*pi.Harness)
	require.True(t, ok)
	assert.Equal(t, o.preToolHook, p.PreToolHook)

	cx, ok := builders[[2]string{config.Codex, codex.Driver}](o).(*codex.Harness)
	require.True(t, ok)
	require.NotNil(t, cx.Guard)
	call := &mergeguard.Call{Tool: "exec_command", Command: "ls"}
	cx.Guard.Check(context.Background(), ident.AgentPrincipal("a1", "7"), call)
	assert.Equal(t, []*mergeguard.Call{call}, asked, "Codex is given the daemon's guard")
}

// TestTheDaemonGuardsCodex: the daemon's own build hands Codex its guard,
// so a Codex agent isn't left on approvalPolicy never.
func TestTheDaemonGuardsCodex(t *testing.T) {
	t.Parallel()
	d := &Daemon{opts: &Options{
		Config: &config.Config{Harnesses: map[string]*config.HarnessConfig{config.Codex: {Driver: codex.Driver}}},
		Logger: slog.New(slog.DiscardHandler),
	}}
	hs, err := d.buildHarnesses("/bin/slop-bot")
	require.NoError(t, err)
	h, ok := hs.Harness(config.Codex)
	require.True(t, ok)
	cx, ok := h.(*codex.Harness)
	require.True(t, ok)
	assert.NotNil(t, cx.Guard)
}
