package agy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/conformance"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake/faketest"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost/tmuxtest"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/tuidriver"
	"github.com/lowaa/slop-factory/slop-bot/internal/transcript"
)

// liveEnv opts into running the conformance suite against the real agy and
// model, in tmux, on the user's own Google sign-in. Off by default: it
// spends plan quota and isn't deterministic, and on a consumer sign-in it is
// the use Google's terms restrict (the user's call; see the package doc).
// Tier 2's scripted Gemini model (task 8.4) is the CI path.
const liveEnv = "SLOP_BOT_LIVE_AGY"

// liveTimeout bounds each step: a real model, and a boot turn at each start.
const liveTimeout = 3 * time.Minute

// TestLiveConformance runs the suite every harness runs against real agy.
// The model is the puppet's hands: the briefing tells it what "do the long
// thing" means, and the puppet checks what agy recorded.
func TestLiveConformance(t *testing.T) {
	if os.Getenv(liveEnv) != "1" {
		t.Skipf("set %s=1 to run against the real agy", liveEnv)
	}
	if _, err := exec.LookPath("agy"); err != nil {
		t.Skip("agy is not installed")
	}
	conformance.Run(t, newLiveSubject)
}

// livePuppet drives real agy (conformance.Puppet). Reply has nothing to
// arrange: the model answers on its own, and the suite checks only how
// turns end.
type livePuppet struct {
	h       *Harness
	socket  string
	release string
	starts  chan liveStart

	mu  sync.Mutex
	cur liveStart
	ids []string
}

type liveStart struct {
	conv    harness.Conversation
	resumed bool
}

// liveHarness records each conversation it starts, for the puppet.
type liveHarness struct {
	*Harness
	p *livePuppet
}

func (h *liveHarness) Start(ctx context.Context, ws *harness.Workspace, resume *harness.ConversationRef) (harness.Conversation, error) {
	conv, err := h.Harness.Start(ctx, ws, resume)
	if err != nil {
		return nil, err
	}
	h.p.mu.Lock()
	h.p.ids = append(h.p.ids, conv.Ref().ID)
	h.p.mu.Unlock()
	h.p.starts <- liveStart{conv: conv, resumed: resume != nil}
	return conv, nil
}

func newLiveSubject(t *testing.T) *conformance.Subject {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	// Not under the test's temp dir: like the other live tests, a workspace
	// where the user's own setup applies.
	dir, err := os.MkdirTemp(filepath.Join(home, "workspace"), ".slop-bot-live-agy-")
	require.NoError(t, err)
	b := make([]byte, 6)
	_, err = rand.Read(b)
	require.NoError(t, err)
	socket := "sbt-agy-" + hex.EncodeToString(b)
	host := tmuxtest.Host(t, &sessionhost.TmuxOptions{Socket: socket, ExitRecheck: 200 * time.Millisecond})
	self, err := os.Executable()
	require.NoError(t, err)
	hub, err := fake.NewHookHub(self)
	require.NoError(t, err)

	p := &livePuppet{socket: socket, release: filepath.Join(dir, ".release"), starts: make(chan liveStart, 2)}
	p.h = &Harness{Sessions: tuidriver.Sessions{
		Host: host, Hub: hub, Pointer: faketest.Pointer,
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})), Clock: clock.Real{},
	}}
	long := filepath.Join(dir, ".long.sh")
	// It reads the release fifo, so it runs until the puppet's release
	// opens it: bounded, so a lost release can't leave it running for long.
	script := fmt.Sprintf("timeout 180 cat %s >/dev/null\necho finished\n", shellQuote(p.release))
	require.NoError(t, os.WriteFile(long, []byte(script), 0o600))
	ws := &harness.Workspace{
		Agent: "a1", Dir: dir, Permissive: true, Effort: "low",
		Briefing: "You are an agent in an automated test. Follow these rules exactly.\n\n" +
			"1. When a message says \"do the long thing\", run exactly this shell command with run_command: sh " + long +
			"\n   It takes a while. Wait for it to finish, even after it moves to the background, then reply with just: done\n" +
			"2. Never run any other command, and never run that command again after it was interrupted.\n" +
			"3. Otherwise, reply in five words or fewer.\n",
		// The hooks' relay is this test binary, as the fake's is.
		Env: map[string]string{"SLOP_BOT_FAKE_REEXEC": "1", "GORACE": "atexit_sleep_ms=0"},
	}
	// Cleanups run last first: the conversations stop before this.
	t.Cleanup(func() {
		_ = hub.Close()
		p.mu.Lock()
		ids := p.ids
		p.mu.Unlock()
		for _, id := range ids {
			if err := forgetConversation(home, id); err != nil {
				t.Errorf("removing agy's records of %s: %v", id, err)
			}
		}
		// By folder, not id: it catches a conversation whose start failed
		// after agy had named it.
		if err := dropHistory(filepath.Join(home, ".gemini", "antigravity-cli", "history.jsonl"), dir); err != nil {
			t.Errorf("removing the test's prompts from agy's history: %v", err)
		}
		_ = p.h.ForgetWorkspace(ws)
		_ = os.RemoveAll(dir)
	})
	return &conformance.Subject{
		Harness:   &liveHarness{Harness: p.h, p: p},
		Workspace: func(*testing.T) *harness.Workspace { return ws },
		Puppet:    p,
		Timeout:   liveTimeout,
	}
}

// Started implements conformance.Puppet.
func (p *livePuppet) Started(ctx context.Context) (string, bool, error) {
	select {
	case s := <-p.starts:
		p.mu.Lock()
		p.cur = s
		p.mu.Unlock()
		return s.conv.Ref().ID, s.resumed, nil
	case <-ctx.Done():
		return "", false, ctx.Err()
	}
}

// Reply implements conformance.Puppet.
func (p *livePuppet) Reply(context.Context, string) error { return nil }

// LongTool implements conformance.Puppet: the briefing's long thing reads
// a fifo, and release opens its other end. That open returns only once the
// command is reading, so release also proves agy ran the tool call (a
// PreToolUse that doesn't allow it refuses it, but its hook has already
// fired, so ToolStarted alone shows nothing).
func (p *livePuppet) LongTool(context.Context) (func(context.Context) error, error) {
	if err := os.Remove(p.release); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := syscall.Mkfifo(p.release, 0o600); err != nil {
		return nil, err
	}
	return func(ctx context.Context) error {
		opened := make(chan error, 1)
		go func() {
			f, err := os.OpenFile(p.release, os.O_WRONLY, 0) // #nosec G304 -- the test's own fifo
			if err == nil {
				err = f.Close()
			}
			opened <- err
		}()
		select {
		case err := <-opened:
			return err
		case <-ctx.Done():
			return fmt.Errorf("the long thing never ran: %w", ctx.Err())
		}
	}, nil
}

// Accepted implements conformance.Puppet: nothing shows a held message
// before the next model call, whose PreInvocation injects it.
func (p *livePuppet) Accepted(context.Context, string) error { return nil }

// Received implements conformance.Puppet, from agy's step log: a prompt is
// a user input step, and a message delivered mid-turn a system message a
// hook injected.
func (p *livePuppet) Received(_ context.Context, text string, midTurn bool) error {
	kind := "USER_INPUT"
	if midTurn {
		kind = "SYSTEM_MESSAGE"
	}
	return p.logged(kind, text)
}

// Remembers implements conformance.Puppet: the resumed session carries on
// the same step log, the earlier session's prompt included.
func (p *livePuppet) Remembers(_ context.Context, text string) error {
	return p.logged("USER_INPUT", text)
}

func (p *livePuppet) logged(kind, text string) error {
	p.mu.Lock()
	id := p.cur.conv.Ref().ID
	p.mu.Unlock()
	path, err := p.h.Transcripts().Locate(id)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- agy's own step log
	if err != nil {
		return err
	}
	for _, line := range transcript.Lines(data) {
		var s step
		if json.Unmarshal(line, &s) == nil && s.Type == kind && strings.Contains(s.Content, text) {
			return nil
		}
	}
	return fmt.Errorf("no %s step with %q in %s", kind, text, path)
}

// Interrupted implements conformance.Puppet: agy reports a cancel on screen
// only.
func (p *livePuppet) Interrupted(ctx context.Context) error {
	screen, err := p.h.Host.Capture(ctx, SessionName("a1"))
	if err != nil {
		return err
	}
	if !interruptedMarker.MatchString(screen) {
		return fmt.Errorf("no cancel on screen:\n%s", screen)
	}
	return nil
}

// Crash implements conformance.Puppet: SIGKILL to agy, the pane's process.
func (p *livePuppet) Crash(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "tmux", "-L", p.socket, "display-message", "-p", "-t", SessionName("a1"), "#{pane_pid}").Output() // #nosec G204 -- the test's own tmux server
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return err
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}

// forgetConversation removes what agy keeps of a conversation under home,
// so the user's `agy --continue` and conversation list aren't left with the
// test's.
func forgetConversation(home, id string) error {
	root := filepath.Join(home, ".gemini", "antigravity-cli")
	errs := []error{os.RemoveAll(filepath.Join(root, "brain", id))}
	for _, f := range []string{
		filepath.Join("conversations", id+".db"), filepath.Join("conversations", id+".db-wal"),
		filepath.Join("conversations", id+".db-shm"), filepath.Join("annotations", id+".pbtxt"),
	} {
		if err := os.Remove(filepath.Join(root, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// dropHistory removes the prompts typed in workspace from agy's prompt
// history, keeping every other line as it was. A prompt agy records as it
// dies, after this ran, stays.
func dropHistory(path, workspace string) error {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- agy's own history
	if err != nil {
		return err
	}
	var kept bytes.Buffer
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		var h struct {
			Workspace string `json:"workspace"`
		}
		if json.Unmarshal(bytes.TrimSpace(line), &h) == nil && h.Workspace == workspace {
			continue
		}
		kept.Write(line)
	}
	if kept.Len() == len(data) {
		return nil
	}
	tmp := path + ".slop-bot-tmp"
	if err := os.WriteFile(tmp, kept.Bytes(), fi.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
