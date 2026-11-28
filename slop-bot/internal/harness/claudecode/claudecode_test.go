package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
)

// fakeClaudeEnv makes the test binary act as a minimal `claude -p` speaking
// stream-json, so Start's argv, environment and hand-over are tested without
// the real CLI.
const fakeClaudeEnv = "SLOP_BOT_TEST_FAKE_CLAUDE"

func TestMain(m *testing.M) {
	if os.Getenv(fakeClaudeEnv) == "1" {
		os.Exit(fakeClaude(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeClaude answers each user record with a turn whose reply carries its
// own argv and the background-task variable, then exits 0 when stdin closes.
func fakeClaude(args []string) int {
	out := json.NewEncoder(os.Stdout)
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		_ = out.Encode(map[string]any{"type": "system", "subtype": "init", "apiKeySource": "none"})
		_ = out.Encode(map[string]any{"type": "assistant", "message": map[string]any{
			"content": []map[string]string{{"type": "text", "text": strings.Join(args, " ") + "|" + os.Getenv("CLAUDE_CODE_DISABLE_BACKGROUND_TASKS")}},
		}})
		_ = out.Encode(map[string]any{"type": "result", "subtype": "success"})
	}
	return 0
}

func fakeHarness(t *testing.T) *Harness {
	t.Helper()
	self, err := os.Executable()
	require.NoError(t, err)
	return &Harness{
		Binary: self, Logger: slog.New(slog.DiscardHandler), Clock: clock.Real{},
		Environ: func() []string { return []string{fakeClaudeEnv + "=1", "GORACE=atexit_sleep_ms=0"} },
	}
}

func next(ctx context.Context, t *testing.T, conv harness.Conversation, kind harness.EventKind) harness.Event {
	t.Helper()
	for {
		select {
		case ev, ok := <-conv.Events():
			require.True(t, ok, "events closed waiting for %s", kind)
			if ev.Kind == kind {
				return ev
			}
		case <-ctx.Done():
			require.FailNow(t, "timed out waiting for "+string(kind))
		}
	}
}

func TestIdentity(t *testing.T) {
	t.Parallel()
	h := &Harness{}
	assert.Equal(t, "claude-code", h.Name())
	assert.Equal(t, "stream-json", h.Driver())
	assert.Equal(t, harness.Capabilities{MidTurnSteer: true, ChosenIDs: true}, h.Capabilities())
}

func TestStartChoosesASessionIDAndPassesTheFlags(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ws := &harness.Workspace{Dir: t.TempDir(), Model: "opus", Env: map[string]string{"X": "y"}}
	conv, err := fakeHarness(t).Start(ctx, ws, nil)
	require.NoError(t, err)
	defer func() { _ = conv.Stop(context.Background(), true) }()
	id := conv.Ref().ID
	assert.NotEmpty(t, id)
	assert.Equal(t, harness.ConversationRef{Harness: Name, Driver: Driver, ID: id}, conv.Ref())

	require.NoError(t, conv.Send(ctx, &harness.Message{Text: "hi"}))
	reply := next(ctx, t, conv, harness.Text).Text
	assert.Equal(t, "-p --input-format stream-json --output-format stream-json --verbose --session-id "+id+" --model opus|1", reply,
		"a fresh session names its id, and background tasks are off")
	assert.Equal(t, harness.TurnCompleted, next(ctx, t, conv, harness.TurnEnded).Outcome)

	plan, err := conv.AttachPlan(ctx)
	require.NoError(t, err)
	assert.Equal(t, harness.HandOver, plan.Kind)
	assert.Equal(t, []string{"--resume", id, "--model", "opus"}, plan.Command.Args, "the TUI resumes the same conversation with the same flags")
	assert.Equal(t, ws.Dir, plan.Command.Dir)
	assert.Contains(t, plan.Command.Env, "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1")
	assert.Contains(t, plan.Command.Env, "X=y")
	assert.Equal(t, fakeHarness(t).Binary, plan.Command.Path)

	require.NoError(t, conv.Stop(ctx, false))
	assert.Equal(t, 0, next(ctx, t, conv, harness.Exited).ExitCode)
}

func TestResumeReopensTheKnownSession(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conv, err := fakeHarness(t).Start(ctx, &harness.Workspace{Dir: t.TempDir()}, &harness.ConversationRef{Harness: Name, Driver: Driver, ID: "kept-id"})
	require.NoError(t, err)
	defer func() { _ = conv.Stop(context.Background(), true) }()
	assert.Equal(t, "kept-id", conv.Ref().ID)
	require.NoError(t, conv.Send(ctx, &harness.Message{Text: "hi"}))
	assert.Contains(t, next(ctx, t, conv, harness.Text).Text, "--resume kept-id")
}

func TestStartFailsWhenTheBinaryIsMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := (&Harness{Environ: func() []string { return nil }}).Start(context.Background(), &harness.Workspace{Dir: t.TempDir()}, nil)
	require.Error(t, err)
}
