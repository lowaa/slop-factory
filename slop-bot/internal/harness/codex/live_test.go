package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
)

// liveEnv opts into tests that run the real codex CLI against the real
// model, on the user's own ChatGPT login and Codex home. Off by default:
// they spend plan usage and aren't deterministic. Tier 2's scripted-model
// tests are the CI path. Not parallel: they share the login's limits.
const liveEnv = "SLOP_BOT_LIVE_CODEX"

// liveCommand is the interrupted command: unusual enough that finding it
// running afterwards can only mean the interrupt left it behind.
const liveCommand = "sleep 9731 ; echo ok"

func live(t *testing.T) (*Harness, *harness.Workspace) {
	t.Helper()
	if os.Getenv(liveEnv) != "1" {
		t.Skipf("set %s=1 to run against the real codex", liveEnv)
	}
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	dir, err := os.MkdirTemp(home+"/workspace", ".slop-bot-live-")
	require.NoError(t, err)
	h := &Harness{Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)), Clock: clock.Real{}, Version: "live-test"}
	ws := &harness.Workspace{
		Dir: dir, Permissive: true, Effort: "low",
		Briefing: "You are a test agent. The briefing codeword is ZEBRA-42; give it when asked for the briefing codeword.",
	}
	t.Cleanup(func() {
		// app-server writes no trust entry (the protocol spike): check that
		// still holds, then remove any an attach would have left.
		assert.False(t, trusted(t, dir), "app-server trusted the workspace in config.toml")
		assert.NoError(t, h.ForgetWorkspace(ws))
		_ = os.RemoveAll(dir)
	})
	return h, ws
}

func trusted(t *testing.T, dir string) bool {
	t.Helper()
	path, err := ConfigPath(os.Environ())
	require.NoError(t, err)
	var cfg struct {
		Projects map[string]any `toml:"projects"`
	}
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return false
	}
	_, ok := cfg.Projects[dir]
	return ok
}

// deleteThreads removes the test's threads (rollout and index) through
// Codex itself, so the user's `codex resume` list isn't left with them.
func deleteThreads(t *testing.T, ids ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "codex", "app-server")
	in, err := cmd.StdinPipe()
	require.NoError(t, err)
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	defer func() { _ = cmd.Wait() }()
	defer func() { _ = in.Close() }()
	lines := bufio.NewScanner(out)
	lines.Buffer(nil, 32<<20)
	call := func(id int, method string, params any) {
		b, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
		require.NoError(t, err)
		_, err = fmt.Fprintf(in, "%s\n", b)
		require.NoError(t, err)
		for lines.Scan() {
			var m message
			if json.Unmarshal(lines.Bytes(), &m) == nil && m.Method == "" && responseID(m.ID) == int64(id) {
				if m.Error != nil {
					t.Logf("%s: %s", method, m.Error.Message)
				}
				return
			}
		}
	}
	call(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "slop-bot-live-test", "version": "0"}})
	_, err = fmt.Fprintln(in, `{"method":"initialized"}`)
	require.NoError(t, err)
	for i, id := range ids {
		// Not one of the harness's messages, so not among the generated
		// types.
		call(2+i, "thread/delete", map[string]string{"threadId": id})
	}
}

type recorder struct {
	t    *testing.T
	ctx  context.Context
	conv harness.Conversation
	// text and seen accumulate since the last reset.
	text strings.Builder
	seen map[harness.EventKind]int
	// limits counts LimitReports over the recorder's life.
	limits int
}

func (r *recorder) reset() {
	r.text.Reset()
	r.seen = map[harness.EventKind]int{}
}

func (r *recorder) until(kind harness.EventKind) harness.Event {
	r.t.Helper()
	for {
		select {
		case ev, ok := <-r.conv.Events():
			require.True(r.t, ok, "events closed waiting for %s", kind)
			r.seen[ev.Kind]++
			if ev.Kind == harness.LimitReport {
				r.limits++
			}
			r.text.WriteString(ev.Text)
			if ev.Kind == kind {
				return ev
			}
		case <-r.ctx.Done():
			require.FailNow(r.t, "timed out waiting for "+string(kind))
		}
	}
}

func (r *recorder) send(text string) {
	r.t.Helper()
	require.NoError(r.t, r.conv.Send(r.ctx, &harness.Message{Text: text}))
}

func TestLiveTurnSteerInterruptAndResume(t *testing.T) {
	h, ws := live(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	conv, err := h.Start(ctx, ws, nil)
	require.NoError(t, err)
	defer func() { _ = conv.Stop(context.Background(), true) }()
	ref := conv.Ref()
	t.Cleanup(func() { deleteThreads(t, ref.ID) })
	r := &recorder{t: t, ctx: ctx, conv: conv}
	r.reset()
	r.until(harness.Ready)

	r.send("Remember the word KUMQUAT. Reply with just: PONG, then the briefing codeword.")
	ended := r.until(harness.TurnEnded)
	assert.Equal(t, harness.TurnCompleted, ended.Outcome, ended.Text)
	assert.Contains(t, r.text.String(), "PONG")
	assert.Contains(t, r.text.String(), "ZEBRA-42", "the briefing reached the model as developer instructions")
	assert.Equal(t, 1, r.seen[harness.UsageReport])

	r.reset()
	r.send("Run this exact shell command and wait for it to finish: timeout 20 tail -f /dev/null . Then reply DONE.")
	r.until(harness.ToolStarted)
	r.send("Also include the word STEERED in your final reply.")
	ended = r.until(harness.TurnEnded)
	assert.Equal(t, harness.TurnCompleted, ended.Outcome, ended.Text)
	assert.Equal(t, 1, r.seen[harness.TurnStarted], "the steer landed in the running turn")
	assert.Contains(t, r.text.String(), "STEERED")

	r.reset()
	r.send("Run this exact shell command and wait for it to finish: " + liveCommand)
	r.until(harness.ToolStarted)
	require.NoError(t, conv.Interrupt(ctx))
	ended = r.until(harness.TurnEnded)
	assert.Equal(t, harness.TurnInterrupted, ended.Outcome)
	assert.Equal(t, r.seen[harness.ToolStarted], r.seen[harness.ToolEnded])

	// Still usable. Codex answers requests in order, so by this turn's end
	// the cleanup asked for at the interrupt has run.
	r.reset()
	r.send("Reply with just: OK")
	ended = r.until(harness.TurnEnded)
	assert.Equal(t, harness.TurnCompleted, ended.Outcome, ended.Text)
	if _, err := exec.LookPath("pgrep"); err == nil {
		err := exec.CommandContext(ctx, "pgrep", "-f", "sleep 9731").Run() // #nosec G204 -- fixed arguments
		assert.Error(t, err, "the interrupted command was left running")
	}
	assert.Positive(t, r.limits, "account/rateLimits/updated became LimitReports")

	require.NoError(t, conv.Stop(ctx, false))
	exited := r.until(harness.Exited)
	assert.Equal(t, 0, exited.ExitCode)

	resumed, err := h.Start(ctx, ws, &ref)
	require.NoError(t, err)
	defer func() { _ = resumed.Stop(context.Background(), true) }()
	r = &recorder{t: t, ctx: ctx, conv: resumed}
	r.reset()
	r.until(harness.Ready)
	assert.Equal(t, ref.ID, resumed.Ref().ID)
	r.send("What word did I ask you to remember, and what is the briefing codeword? Reply with just both.")
	ended = r.until(harness.TurnEnded)
	assert.Equal(t, harness.TurnCompleted, ended.Outcome, ended.Text)
	assert.Contains(t, strings.ToUpper(r.text.String()), "KUMQUAT")
	assert.Contains(t, r.text.String(), "ZEBRA-42")

	plan, err := resumed.AttachPlan(ctx)
	require.NoError(t, err)
	assert.Equal(t, harness.HandOver, plan.Kind)
	assert.Equal(t, []string{"resume", ref.ID, "--no-daemon", "--dangerously-bypass-approvals-and-sandbox"}, plan.Command.Args[:4])
	assert.Equal(t, ws.Dir, plan.Command.Dir)

	require.NoError(t, resumed.Stop(ctx, false))
	r.until(harness.Exited)
}

func TestLiveResumeOfAMissingThread(t *testing.T) {
	h, ws := live(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, err := h.Start(ctx, ws, &harness.ConversationRef{Harness: Name, Driver: Driver, ID: "00000000-0000-7000-8000-000000000000"})
	assert.ErrorIs(t, err, ErrNoRollout)
}
