package sessionhost_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost/tmuxtest"
)

func ctxFor(t *testing.T) context.Context {
	t.Helper()
	// A safety net, never a synchronisation.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// shell starts a session running script, with only PATH (and extra) set.
func shell(ctx context.Context, t *testing.T, host *sessionhost.Tmux, name, script string, extra ...string) string {
	t.Helper()
	dir := t.TempDir()
	env := append([]string{"PATH=" + os.Getenv("PATH")}, extra...)
	require.NoError(t, host.Start(ctx, &sessionhost.StartSpec{Name: name, Argv: []string{"sh", "-c", script}, Env: env, Dir: dir}))
	return dir
}

// waitDead waits for the session's process to exit, on tmux's own signal.
func waitDead(ctx context.Context, t *testing.T, host *sessionhost.Tmux, name string) *sessionhost.Status {
	t.Helper()
	for {
		require.NoError(t, host.WaitExit(ctx, name))
		st, err := host.Status(ctx, name)
		require.NoError(t, err)
		if st.Dead {
			return st
		}
	}
}

func TestEnvironmentIsExactlyTheSpecs(t *testing.T) {
	t.Parallel()
	ctx := ctxFor(t)
	host := tmuxtest.New(t)
	// The first session starts the server; its environment must not become
	// the second's.
	dir1 := shell(ctx, t, host, "one", `env > env.txt`, "SLOP_BOT_AGENT_TOKEN=first", "ONLY_ONE=1")
	dir2 := shell(ctx, t, host, "two", `env > env.txt`, "SLOP_BOT_AGENT_TOKEN=second")
	waitDead(ctx, t, host, "one")
	waitDead(ctx, t, host, "two")

	env1 := readEnv(t, dir1)
	env2 := readEnv(t, dir2)
	assert.Equal(t, "first", env1["SLOP_BOT_AGENT_TOKEN"])
	assert.Equal(t, "second", env2["SLOP_BOT_AGENT_TOKEN"])
	assert.NotContains(t, env2, "ONLY_ONE", "one session's environment leaked into another's")
	assert.NotContains(t, env2, "HOME", "the test process's environment leaked into the session")
}

func readEnv(t *testing.T, dir string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "env.txt"))
	require.NoError(t, err)
	env := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			env[k] = v
		}
	}
	return env
}

func TestExitStatusOfADeadPane(t *testing.T) {
	t.Parallel()
	ctx := ctxFor(t)
	host := tmuxtest.New(t)
	shell(ctx, t, host, "exits", `exit 3`)
	shell(ctx, t, host, "signalled", `kill -TERM $$`)
	assert.Equal(t, 3, waitDead(ctx, t, host, "exits").ExitStatus)
	assert.Equal(t, 128+int(syscall.SIGTERM), waitDead(ctx, t, host, "signalled").ExitStatus)

	names, err := host.List(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"exits", "signalled"}, names, "dead panes remain until killed")
}

// A lost wakeup (see WaitExit) mustn't hang the caller: each wait returns
// by the recheck interval, live session or not.
func TestWaitExitIsBounded(t *testing.T) {
	t.Parallel()
	ctx := ctxFor(t)
	host := tmuxtest.New(t)
	shell(ctx, t, host, "long", `exec sleep 1000`)
	require.NoError(t, host.WaitExit(ctx, "long"))
	st, err := host.Status(ctx, "long")
	require.NoError(t, err)
	assert.False(t, st.Dead)
}

func TestKillWakesAWaiter(t *testing.T) {
	t.Parallel()
	ctx := ctxFor(t)
	host := tmuxtest.New(t)
	shell(ctx, t, host, "long", `sleep 1000`)
	waited := make(chan error, 1)
	go func() { waited <- host.WaitExit(ctx, "long") }()
	require.NoError(t, host.Kill(ctx, "long"))
	require.NoError(t, <-waited)
	_, err := host.Status(ctx, "long")
	assert.ErrorIs(t, err, sessionhost.ErrNoSession)
}

func TestKeysTextAndPaste(t *testing.T) {
	t.Parallel()
	ctx := ctxFor(t)
	host := tmuxtest.New(t)
	// Turn on bracketed paste, as a TUI does, and record the raw bytes the
	// terminal delivers up to the final Enter.
	dir := shell(ctx, t, host, "rec", `printf '\033[?2004h'; stty raw -echo; touch ready; dd bs=1 count=19 2>/dev/null | od -An -c > got.txt`)
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(dir, "ready")); return err == nil }, 10*time.Second, 5*time.Millisecond)

	require.NoError(t, host.SendText(ctx, "rec", "ab"))
	require.NoError(t, host.Paste(ctx, "rec", "x\ny"))
	require.NoError(t, host.SendKeys(ctx, "rec", "Escape", "Enter"))
	waitDead(ctx, t, host, "rec")
	b, err := os.ReadFile(filepath.Join(dir, "got.txt"))
	require.NoError(t, err)
	got := strings.Join(strings.Fields(string(b)), " ")
	// ab, then the bracketed paste (tmux sends its newline as CR), then
	// Escape and Enter.
	assert.Equal(t, `a b 033 [ 2 0 0 ~ x \r y 033 [ 2 0 1 ~ 033 \r`, got)
}

func TestCaptureShowsTheScreen(t *testing.T) {
	t.Parallel()
	ctx := ctxFor(t)
	host := tmuxtest.New(t)
	shell(ctx, t, host, "show", `printf '⣷ esc to cancel\n'; exec sleep 1000`)
	require.Eventually(t, func() bool {
		screen, err := host.Capture(ctx, "show")
		return err == nil && strings.Contains(screen, "⣷ esc to cancel")
	}, 10*time.Second, 5*time.Millisecond, "UTF-8 survives a server started with no locale")
}

func TestTagsAttachAndMissingSessions(t *testing.T) {
	t.Parallel()
	ctx := ctxFor(t)
	host := tmuxtest.New(t)

	names, err := host.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, names, "no server yet is no sessions")

	shell(ctx, t, host, "agent-1", `exec sleep 1000`)
	shell(ctx, t, host, "agent-12", `exec sleep 1000`)
	require.NoError(t, host.SetTag(ctx, "agent-1", "conversation", "c-42"))
	v, err := host.Tag(ctx, "agent-1", "conversation")
	require.NoError(t, err)
	assert.Equal(t, "c-42", v)
	v, err = host.Tag(ctx, "agent-12", "conversation")
	require.NoError(t, err)
	assert.Empty(t, v, "targets are exact, not prefixes")

	st, err := host.Status(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, &sessionhost.Status{}, st)
	detach := tmuxtest.Attach(ctx, t, host, "agent-1")
	st, err = host.Status(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, 1, st.Attached)
	detach()

	_, err = host.Status(ctx, "agent-")
	assert.ErrorIs(t, err, sessionhost.ErrNoSession)
	assert.ErrorIs(t, host.SendKeys(ctx, "nope", "Enter"), sessionhost.ErrNoSession)
	assert.Error(t, host.Start(ctx, &sessionhost.StartSpec{Name: "bad:name", Argv: []string{"true"}}))
	assert.Error(t, host.Start(ctx, &sessionhost.StartSpec{Name: "agent-1", Argv: []string{"true"}}), "names are unique")
}
