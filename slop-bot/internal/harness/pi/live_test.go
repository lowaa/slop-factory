package pi

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

// liveEnv opts into tests that run the real pi against the real model, on
// the user's own login and configuration. Off by default: they spend plan
// usage and aren't deterministic. Tier 2's scripted-model tests are the CI
// path (see Harness.AgentDir).
const liveEnv = "SLOP_BOT_LIVE_PI"

// liveModel is set explicitly so the test doesn't depend on DefaultModel.
const liveModel = "openai/gpt-6.2-sol"

func live(t *testing.T) (*Harness, *harness.Workspace) {
	t.Helper()
	if os.Getenv(liveEnv) != "1" {
		t.Skipf("set %s=1 to run against the real pi", liveEnv)
	}
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	dir, err := os.MkdirTemp(filepath.Join(home, "workspace"), ".slop-bot-live-")
	require.NoError(t, err)
	// pi keeps each cwd's sessions under ~/.pi/agent/sessions, in a
	// directory named after the path (session-manager.js: the leading slash
	// dropped, / \ and : turned into -, wrapped in --).
	sessions := filepath.Join(home, ".pi", "agent", "sessions",
		"--"+strings.NewReplacer("/", "-", ":", "-").Replace(strings.TrimPrefix(dir, "/"))+"--")
	t.Cleanup(func() { _ = os.RemoveAll(dir); _ = os.RemoveAll(sessions) })
	return &Harness{Logger: slog.New(slog.DiscardHandler), Clock: clock.Real{}},
		&harness.Workspace{Dir: dir, Permissive: true, Model: liveModel, Effort: "low", Briefing: "Be terse."}
}

// until returns the next event of kind, passing every event to seen. A
// TurnEnded that arrives first fails the test unless kind is TurnEnded.
func until(ctx context.Context, t *testing.T, conv harness.Conversation, kind harness.EventKind, seen func(harness.Event)) harness.Event {
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
			if ev.Kind == harness.TurnEnded {
				require.FailNowf(t, "the turn ended early", "waiting for %s: %s %s", kind, ev.Outcome, ev.Text)
			}
		case <-ctx.Done():
			require.FailNow(t, "timed out waiting for "+string(kind))
		}
	}
}

func TestLiveTurnSteerInterruptAndResume(t *testing.T) {
	h, ws := live(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	conv, err := h.Start(ctx, ws, nil)
	require.NoError(t, err)
	defer func() { _ = conv.Stop(context.Background(), true) }()
	until(ctx, t, conv, harness.Ready, nil)

	var text strings.Builder
	collect := func(e harness.Event) { text.WriteString(e.Text) }

	require.NoError(t, conv.Send(ctx, &harness.Message{Text: "Remember the word KUMQUAT. Reply with just: PONG"}))
	ended := until(ctx, t, conv, harness.TurnEnded, collect)
	assert.Equal(t, harness.TurnCompleted, ended.Outcome, ended.Text)
	assert.Contains(t, text.String(), "PONG")

	// A steer during a tool call lands in the same turn.
	text.Reset()
	require.NoError(t, conv.Send(ctx, &harness.Message{Text: "Run exactly `sleep 10` with your bash tool, then reply with just: SLEPT"}))
	until(ctx, t, conv, harness.ToolStarted, nil)
	require.NoError(t, conv.Send(ctx, &harness.Message{Text: "Also end your reply with the word STEER-SEEN"}))
	ended = until(ctx, t, conv, harness.TurnEnded, collect)
	assert.Equal(t, harness.TurnCompleted, ended.Outcome, ended.Text)
	assert.Contains(t, text.String(), "STEER-SEEN")

	require.NoError(t, conv.Send(ctx, &harness.Message{Text: "Run exactly `sleep 120` with your bash tool and wait for it."}))
	until(ctx, t, conv, harness.ToolStarted, nil)
	require.NoError(t, conv.Interrupt(ctx))
	ended = until(ctx, t, conv, harness.TurnEnded, nil)
	assert.Equal(t, harness.TurnInterrupted, ended.Outcome)

	ref := conv.Ref()
	require.NoError(t, conv.Stop(ctx, false))
	assert.Equal(t, 0, until(ctx, t, conv, harness.Exited, nil).ExitCode)

	resumed, err := h.Start(ctx, ws, &ref)
	require.NoError(t, err)
	defer func() { _ = resumed.Stop(context.Background(), true) }()
	until(ctx, t, resumed, harness.Ready, nil)
	assert.Equal(t, ref.ID, resumed.Ref().ID)
	text.Reset()
	require.NoError(t, resumed.Send(ctx, &harness.Message{Text: "What word did I ask you to remember? Reply with just the word."}))
	until(ctx, t, resumed, harness.TurnEnded, collect)
	assert.Contains(t, strings.ToUpper(text.String()), "KUMQUAT")

	plan, err := resumed.AttachPlan(ctx)
	require.NoError(t, err)
	assert.Equal(t, harness.HandOver, plan.Kind)
	assert.Equal(t, []string{"--session", ref.ID, "--model", liveModel}, plan.Command.Args[:4])
	require.NoError(t, resumed.Stop(ctx, false))
}
