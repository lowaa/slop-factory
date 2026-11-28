package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"

	"github.com/lowaa/slop-factory/slop-bot/internal/daemon"
	sbharness "github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/agy"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/agy/agytest"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost/tmuxtest"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/tuidriver"
)

// liveAgyEnv opts into running real agy through the daemon, on the user's
// own Google sign-in (see the agy package's live test): it spends a little
// plan quota, a handful of model calls.
const liveAgyEnv = "SLOP_BOT_LIVE_AGY"

// liveAgy is real agy for the daemon, on the user's home and sign-in, with
// the agent's slop-bot calls and hooks (the test binary, run as the CLI)
// reaching this test's daemon. It records what to clean up.
type liveAgy struct {
	*agy.Harness
	h *harness

	mu   sync.Mutex
	ids  []string
	dirs []string
}

func (l *liveAgy) Start(ctx context.Context, ws *sbharness.Workspace, resume *sbharness.ConversationRef) (sbharness.Conversation, error) {
	ws.Env[cliReexec] = "1"
	ws.Env["XDG_RUNTIME_DIR"] = l.h.runtime
	l.mu.Lock()
	l.dirs = append(l.dirs, ws.Dir)
	l.mu.Unlock()
	conv, err := l.Harness.Start(ctx, ws, resume)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.ids = append(l.ids, conv.Ref().ID)
	l.mu.Unlock()
	return conv, nil
}

// TestLiveAgyWorksATicketThroughTheDaemon is the smoke: real agy, pointed
// at its inbox for the kick-off, reads it there and calls done, all through
// the daemon's hooks and socket.
func TestLiveAgyWorksATicketThroughTheDaemon(t *testing.T) {
	if os.Getenv(liveAgyEnv) != "1" {
		t.Skipf("set %s=1 to run real agy through the daemon", liveAgyEnv)
	}
	for _, bin := range []string{"agy", "tmux"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " is not installed")
		}
	}
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	h := newHarness(t)
	// A repo of the test's own, with no remote: the merge the agent is
	// told to try could do nothing even if the guard let it through.
	repo := filepath.Join(h.home, "scratch")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", repo},
		{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		out, err := exec.Command("git", args...).CombinedOutput() // #nosec G204 -- the test's own repo
		require.NoError(t, err, string(out))
	}
	require.NoError(t, os.WriteFile(h.config, []byte("user: ada\ndefault_harness: agy\nharnesses: {agy: {}}\n"+
		"repos: [{name: scratch, path: "+repo+"}]\n"), 0o600))
	b := make([]byte, 6)
	_, err = rand.Read(b)
	require.NoError(t, err)
	h.deps.tmuxSocket = "sbt-agy-" + hex.EncodeToString(b)
	host := tmuxtest.Host(t, &sessionhost.TmuxOptions{Socket: h.deps.tmuxSocket})
	live := &liveAgy{h: h}
	h.deps.tuiHarnesses = func(kit *tuidriver.Sessions) daemon.Harnesses {
		s := *kit
		s.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
		live.Harness = &agy.Harness{Sessions: s}
		return daemon.Harnesses{"agy": live}
	}
	// Last first: after the daemon has stopped the agent.
	t.Cleanup(func() {
		live.mu.Lock()
		defer live.mu.Unlock()
		for _, id := range live.ids {
			if err := agytest.ForgetConversation(home, id); err != nil {
				t.Errorf("removing agy's records of %s: %v", id, err)
			}
		}
		for _, dir := range live.dirs {
			if err := agytest.DropHistory(home, dir); err != nil {
				t.Errorf("removing the test's prompts from agy's history: %v", err)
			}
		}
	})
	self, err := os.Executable()
	require.NoError(t, err)
	e := &e2e{t: t, h: h, self: self, stop: startDaemon(t, h), timeout: 4 * time.Minute}
	// Before the daemon stops: what a failure needs to be understood.
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		if h.run(t, "events") == 0 {
			t.Logf("events:\n%s", h.out.String())
		}
		names, _ := host.List(context.Background())
		for _, n := range names {
			screen, _ := host.Capture(context.Background(), n)
			t.Logf("session %s:\n%s", n, screen)
		}
	})

	e.h.ticket(t, "ticket", "create", "--assign", "--effort", "low", "--title", "Live smoke test", "--repo", "scratch",
		"--body", "This is an automated test of slop-bot. Do exactly this, and nothing else. First, run the shell "+
			"command `gh pr merge 1` with run_command, once, whatever it answers. Then run the shell command `"+
			self+" done --summary smoke`, then reply with just: ok")
	// agy's PreToolUse put the merge to the guard, which refused it: the
	// ticket has no AutoMerge, and the user hasn't authorised one.
	refused := e.await(daemon.EventMergeRefused, nil)
	agent := e.await("agent.finished", nil).Agent
	assert.Equal(t, agent, refused.Agent)

	// It got the ticket by following its pointer: the kick-off was read
	// from its inbox.
	var log []api.Message
	require.NoError(t, control.NewClient(h.runtime+"/slop-bot/slop-bot.sock", "").Do(context.Background(), "GET",
		api.PathInbox+"?log&target="+agent, nil, &log))
	read := false
	for _, m := range log {
		read = read || (strings.Contains(m.Body, "Live smoke test") && m.Status == "read")
	}
	assert.True(t, read, "the kick-off, read through the inbox: %+v", log)
	live.mu.Lock()
	t.Logf("agy conversations, removed at cleanup: %v", live.ids)
	live.mu.Unlock()
}
