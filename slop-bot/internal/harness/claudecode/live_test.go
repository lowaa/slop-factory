package claudecode

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
)

// liveEnv opts into tests that run the real claude CLI against the real
// model, on the user's own login. Off by default: they spend plan usage and
// aren't deterministic. Tier 2's scripted-model tests are the CI path.
const liveEnv = "SLOP_BOT_LIVE_CLAUDE"

func live(t *testing.T) (*Harness, *harness.Workspace) {
	t.Helper()
	if os.Getenv(liveEnv) != "1" {
		t.Skipf("set %s=1 to run against the real claude", liveEnv)
	}
	// Under the user's workspace, not /tmp: Claude Code treats its own temp
	// tree specially (spike notes).
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	dir, err := os.MkdirTemp(home+"/workspace", ".slop-bot-live-")
	require.NoError(t, err)
	// Claude Code keeps each cwd's transcripts under ~/.claude/projects, in
	// a directory named after the path with / and . turned into -.
	projects := filepath.Join(home, ".claude", "projects", strings.NewReplacer("/", "-", ".", "-").Replace(dir))
	t.Cleanup(func() { _ = os.RemoveAll(dir); _ = os.RemoveAll(projects) })
	return &Harness{Logger: slog.New(slog.DiscardHandler), Clock: clock.Real{}},
		&harness.Workspace{Dir: dir, Permissive: true, Model: "claude-haiku-5-5"}
}

func until(t *testing.T, ctx context.Context, conv harness.Conversation, kind harness.EventKind, seen func(harness.Event)) harness.Event {
	t.Helper()
	for {
		select {
		case ev, ok := <-conv.Events():
			require.True(t, ok, "events closed waiting for %s", kind)
			if seen != nil {
				seen(ev)
			}
			if ev.Kind == kind {
				return ev
			}
		case <-ctx.Done():
			require.FailNow(t, "timed out waiting for "+string(kind))
		}
	}
}

func TestLiveTurnInterruptAndResume(t *testing.T) {
	h, ws := live(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	conv, err := h.Start(ctx, ws, nil)
	require.NoError(t, err)
	defer func() { _ = conv.Stop(context.Background(), true) }()
	until(t, ctx, conv, harness.Ready, nil)

	require.NoError(t, conv.Send(ctx, &harness.Message{Text: "Remember the word KUMQUAT. Reply with just: PONG"}))
	var text strings.Builder
	ended := until(t, ctx, conv, harness.TurnEnded, func(e harness.Event) { text.WriteString(e.Text) })
	assert.Equal(t, harness.TurnCompleted, ended.Outcome)
	assert.Contains(t, text.String(), "PONG")

	require.NoError(t, conv.Send(ctx, &harness.Message{Text: "Run this exact shell command with your Bash tool and wait for it: timeout 60 tail -f /dev/null"}))
	until(t, ctx, conv, harness.ToolStarted, nil)
	require.NoError(t, conv.Interrupt(ctx))
	ended = until(t, ctx, conv, harness.TurnEnded, nil)
	assert.Equal(t, harness.TurnInterrupted, ended.Outcome)

	ref := conv.Ref()
	require.NoError(t, conv.Stop(ctx, false))
	until(t, ctx, conv, harness.Exited, nil)

	resumed, err := h.Start(ctx, ws, &ref)
	require.NoError(t, err)
	defer func() { _ = resumed.Stop(context.Background(), true) }()
	until(t, ctx, resumed, harness.Ready, nil)
	assert.Equal(t, ref.ID, resumed.Ref().ID)
	require.NoError(t, resumed.Send(ctx, &harness.Message{Text: "What word did I ask you to remember? Reply with just the word."}))
	text.Reset()
	until(t, ctx, resumed, harness.TurnEnded, func(e harness.Event) { text.WriteString(e.Text) })
	assert.Contains(t, strings.ToUpper(text.String()), "KUMQUAT")

	plan, err := resumed.AttachPlan(ctx)
	require.NoError(t, err)
	assert.Equal(t, harness.HandOver, plan.Kind)
	assert.Equal(t, []string{"--resume", ref.ID}, plan.Command.Args[:2])
}
