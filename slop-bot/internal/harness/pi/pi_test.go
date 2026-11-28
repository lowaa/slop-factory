package pi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
)

// fakePiEnv makes the test binary act as a minimal `pi --mode rpc`, so the
// harness's launch, handshake and hand-over are tested end to end without
// the real pi or a model.
const fakePiEnv = "SLOP_BOT_TEST_FAKE_PI"

func TestMain(m *testing.M) {
	if os.Getenv(fakePiEnv) == "1" {
		os.Exit(fakePi(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakePi answers get_state with the --session-id it was given and the
// --model, and answers each prompt with a run whose reply echoes the
// message and the PI_CODING_AGENT_DIR it sees. Closing stdin exits 0. Like
// pi, it keeps a session by the cwd: each prompt adds a line to a file
// there, whose length get_state reports as the message count.
func fakePi(args []string) int {
	flag := func(name string) string {
		if i := slices.Index(args, name); i >= 0 && i+1 < len(args) {
			return args[i+1]
		}
		return ""
	}
	out := json.NewEncoder(os.Stdout)
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var cmd command
		if err := json.Unmarshal(sc.Bytes(), &cmd); err != nil {
			return 2
		}
		session := fakeSessionFile(flag("--session-id"))
		switch cmd.Type {
		case "get_state":
			_ = out.Encode(map[string]any{"id": cmd.ID, "type": "response", "command": "get_state", "success": true,
				"data": map[string]any{"sessionId": flag("--session-id"), "messageCount": fakeMessageCount(session),
					"model": map[string]string{"provider": "p", "id": flag("--model")}}})
		case "prompt":
			if err := appendLine(session, cmd.Message); err != nil {
				return 3
			}
			_ = out.Encode(map[string]any{"id": cmd.ID, "type": "response", "command": "prompt", "success": true, "data": map[string]string{"disposition": "started"}})
			_ = out.Encode(map[string]any{"type": "agent_start"})
			_ = out.Encode(map[string]any{"type": "message_end", "message": map[string]any{
				"role": "assistant", "stopReason": "stop",
				"content": []map[string]string{{"type": "text", "text": "echo:" + cmd.Message + ":" + os.Getenv("PI_CODING_AGENT_DIR")}},
			}})
			_ = out.Encode(map[string]any{"type": "agent_settled", "aborted": false})
		}
	}
	return 0
}

// fakeSessionFile is where fakePi keeps a session, in the cwd as pi does.
func fakeSessionFile(id string) string { return ".fake-pi-session-" + id }

func fakeMessageCount(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return bytes.Count(b, []byte("\n"))
}

func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(f, line)
	return errors.Join(err, f.Close())
}

func fakeHarness(t *testing.T) *Harness {
	t.Helper()
	self, err := os.Executable()
	require.NoError(t, err)
	return &Harness{
		Binary: self, AgentDir: "/agent-dir",
		Logger: slog.New(slog.DiscardHandler), Clock: clock.Real{},
		Environ: func() []string {
			return []string{fakePiEnv + "=1", "OPENAI_API_KEY=sk-test", "GORACE=atexit_sleep_ms=0"}
		},
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

func TestStartHandshakeTurnAndAttachPlan(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := fakeHarness(t)
	brief := filepath.Join(t.TempDir(), "briefing.md")
	require.NoError(t, os.WriteFile(brief, []byte("brief"), 0o600))
	ws := &harness.Workspace{Dir: t.TempDir(), Effort: "low", Briefing: "brief", BriefingFile: brief}

	conv, err := h.Start(ctx, ws, nil)
	require.NoError(t, err)
	defer func() { _ = conv.Stop(context.Background(), true) }()
	next(ctx, t, conv, harness.Ready)
	id := conv.Ref().ID
	assert.NotEmpty(t, id, "slop-bot chooses the id")
	assert.Equal(t, harness.ConversationRef{Harness: "pi", Driver: "rpc", ID: id}, conv.Ref())

	require.NoError(t, conv.Send(ctx, &harness.Message{Text: "hello"}))
	assert.Equal(t, "echo:hello:/agent-dir", next(ctx, t, conv, harness.Text).Text, "the agent dir hook reaches pi")
	assert.Equal(t, harness.TurnCompleted, next(ctx, t, conv, harness.TurnEnded).Outcome)

	plan, err := conv.AttachPlan(ctx)
	require.NoError(t, err)
	assert.Equal(t, harness.HandOver, plan.Kind)
	assert.Equal(t, []string{"--session", id, "--model", DefaultModel, "--thinking", "low", "--append-system-prompt", brief}, plan.Command.Args)
	assert.Equal(t, ws.Dir, plan.Command.Dir)
	assert.Contains(t, plan.Command.Env, "PI_CODING_AGENT_DIR=/agent-dir")
	assert.NotContains(t, plan.Command.Env, "OPENAI_API_KEY=sk-test", "an API key would move billing off the ChatGPT login")

	require.NoError(t, conv.Stop(ctx, false))
	assert.Equal(t, 0, next(ctx, t, conv, harness.Exited).ExitCode)
}

func TestResumeReopensTheSameID(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	require.NoError(t, appendLine(filepath.Join(dir, fakeSessionFile("kept-id")), "kick-off"))
	conv, err := fakeHarness(t).Start(ctx, &harness.Workspace{Dir: dir}, &harness.ConversationRef{Harness: Name, Driver: Driver, ID: "kept-id"})
	require.NoError(t, err)
	defer func() { _ = conv.Stop(context.Background(), true) }()
	assert.Equal(t, "kept-id", conv.Ref().ID)
}

// pi finds a session by the cwd's project, and --session-id makes a missing
// one: resumed anywhere else, the conversation would carry on empty.
func TestAResumeThatFindsNoSessionFails(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := fakeHarness(t).Start(ctx, &harness.Workspace{Dir: t.TempDir()}, &harness.ConversationRef{Harness: Name, Driver: Driver, ID: "kept-id"})
	assert.ErrorIs(t, err, ErrEmptySession)
}

func TestCommonFlags(t *testing.T) {
	t.Parallel()
	h := &Harness{}
	brief := filepath.Join(t.TempDir(), "briefing.md")
	require.NoError(t, os.WriteFile(brief, []byte("from the file"), 0o600))
	cases := map[string]struct {
		h    *Harness
		ws   *harness.Workspace
		want []string
	}{
		"the model is always passed": {h, &harness.Workspace{}, []string{"--model", DefaultModel}},
		"the configured default":     {&Harness{DefaultModel: "openai/other"}, &harness.Workspace{}, []string{"--model", "openai/other"}},
		"the workspace's model wins": {&Harness{DefaultModel: "openai/other"}, &harness.Workspace{Model: "scripted/m1"}, []string{"--model", "scripted/m1"}},
		"effort is thinking":         {h, &harness.Workspace{Effort: "xhigh"}, []string{"--model", DefaultModel, "--thinking", "xhigh"}},
		"briefing text":              {h, &harness.Workspace{Briefing: "brief"}, []string{"--model", DefaultModel, "--append-system-prompt", "brief"}},
		"the briefing file is preferred": {
			h, &harness.Workspace{Briefing: "brief", BriefingFile: brief},
			[]string{"--model", DefaultModel, "--append-system-prompt", brief},
		},
		"a missing briefing file falls back to the text": {
			h, &harness.Workspace{Briefing: "brief", BriefingFile: brief + ".gone"},
			[]string{"--model", DefaultModel, "--append-system-prompt", "brief"},
		},
		"permissive and extra dirs need no flag": {
			h, &harness.Workspace{Permissive: true, ExtraDirs: []string{"/ws/lib"}}, []string{"--model", DefaultModel},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.h.common(tc.ws)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestAnUnknownEffortIsRefused(t *testing.T) {
	t.Parallel()
	_, err := (&Harness{}).common(&harness.Workspace{Effort: "ludicrous"})
	assert.ErrorContains(t, err, `"ludicrous"`)
}

func TestEnv(t *testing.T) {
	t.Parallel()
	base := func() []string { return []string{"PATH=/bin", "OPENAI_API_KEY=sk", "PI_CODING_AGENT_DIR=/users/own"} }
	t.Run("the user's own pi config by default", func(t *testing.T) {
		t.Parallel()
		env := (&Harness{Environ: base}).env(&harness.Workspace{Env: map[string]string{"SLOP_BOT_AGENT_TOKEN": "tok"}})
		assert.Contains(t, env, "PI_CODING_AGENT_DIR=/users/own")
		assert.Contains(t, env, "SLOP_BOT_AGENT_TOKEN=tok")
		assert.NotContains(t, env, "OPENAI_API_KEY=sk")
	})
	t.Run("the agent dir hook overrides it", func(t *testing.T) {
		t.Parallel()
		env := (&Harness{Environ: base, AgentDir: "/scripted"}).env(&harness.Workspace{APIKeyOptIn: true})
		assert.Contains(t, env, "PI_CODING_AGENT_DIR=/scripted")
		assert.NotContains(t, env, "PI_CODING_AGENT_DIR=/users/own")
		assert.Contains(t, env, "OPENAI_API_KEY=sk", "opted in")
	})
}
