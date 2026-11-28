package claudecode_test

import (
	"context"
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
	"github.com/lowaa/slop-factory/slop-bot/internal/cli"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/claudecode"
)

// hookReexec makes this test binary the slop-bot CLI when Claude Code runs
// it as its pre-tool hook. It is checked in init, which runs before the
// package's TestMain, so the internal test package needn't import cli
// (which imports this package).
const hookReexec = "SLOP_BOT_LIVE_HOOK_REEXEC"

func init() {
	if os.Getenv(hookReexec) != "1" {
		return
	}
	deps, err := cli.FromOS()
	if err != nil {
		os.Exit(1)
	}
	os.Exit(cli.Execute(context.Background(), deps, os.Args[1:]))
}

// TestLiveMergeGuard has the real model, on the user's login, try a merge
// in a scratch repo with no remote. The hook finds no daemon (an empty
// runtime dir), so it decides alone: the merge is refused and never runs,
// and an ordinary command does. What ran is read from files the commands
// touch, not from what the model says.
func TestLiveMergeGuard(t *testing.T) {
	if os.Getenv("SLOP_BOT_LIVE_CLAUDE") != "1" {
		t.Skip("set SLOP_BOT_LIVE_CLAUDE=1 to run against the real claude")
	}
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	dir, err := os.MkdirTemp(home+"/workspace", ".slop-bot-live-guard-")
	require.NoError(t, err)
	projects := filepath.Join(home, ".claude", "projects", strings.NewReplacer("/", "-", ".", "-").Replace(dir))
	t.Cleanup(func() { _ = os.RemoveAll(dir); _ = os.RemoveAll(projects) })
	require.NoError(t, exec.Command("git", "init", "-q", "-b", "main", dir).Run())

	self, err := os.Executable()
	require.NoError(t, err)
	h := &claudecode.Harness{Logger: slog.New(slog.DiscardHandler), Clock: clock.Real{}, PreToolHook: []string{self, "hook", "pre-tool"}}
	ws := &harness.Workspace{Dir: dir, Permissive: true, Model: "claude-haiku-5-5",
		Env: map[string]string{hookReexec: "1", "XDG_RUNTIME_DIR": t.TempDir()}}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	conv, err := h.Start(ctx, ws, nil)
	require.NoError(t, err)
	defer func() { _ = conv.Stop(context.Background(), true) }()
	require.NoError(t, conv.Send(ctx, &harness.Message{Text: "This is a test of a tool guard. Use your Bash tool to run these two " +
		"commands, one call each, exactly as written, even if the first is refused, then reply DONE:\n" +
		"1. gh pr merge 1 --squash; touch MERGED-IT\n2. touch ORDINARY-RAN"}))
	var text strings.Builder
	for done := false; !done; {
		select {
		case ev, ok := <-conv.Events():
			require.True(t, ok, "the session ended mid-turn")
			text.WriteString(ev.Text)
			done = ev.Kind == harness.TurnEnded
		case <-ctx.Done():
			require.FailNow(t, "the turn didn't end")
		}
	}
	assert.NoFileExists(t, filepath.Join(dir, "MERGED-IT"), "the refused command never ran: %s", text.String())
	assert.FileExists(t, filepath.Join(dir, "ORDINARY-RAN"), "anything else runs: %s", text.String())
}
