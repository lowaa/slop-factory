package agentproc

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
)

func sh(t *testing.T, script string) *harness.LaunchSpec {
	t.Helper()
	path, err := exec.LookPath("sh")
	require.NoError(t, err)
	return &harness.LaunchSpec{Path: path, Args: []string{"-c", script}, Env: []string{"PATH=" + os.Getenv("PATH")}, Dir: t.TempDir()}
}

func TestPipesAndExitCode(t *testing.T) {
	t.Parallel()
	p, err := Start(context.Background(), sh(t, `read line; echo "got $line"; echo oops >&2; exit 3`))
	require.NoError(t, err)
	_, err = p.Stdin.Write([]byte("hello\n"))
	require.NoError(t, err)
	line, err := bufio.NewReader(p.Stdout).ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "got hello\n", line)
	assert.Equal(t, 3, p.ExitCode())
	assert.NoError(t, p.Err())
	assert.Equal(t, "oops\n", p.Stderr())
}

func TestOutputWrittenBeforeExitIsStillReadableAfterIt(t *testing.T) {
	t.Parallel()
	// Done is awaited before any read, so the output sits unread while Wait
	// runs; a Wait that closed the read end would lose it.
	p, err := Start(context.Background(), sh(t, `echo first; echo last`))
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Stdout.Close() })
	<-p.Done()
	out, err := io.ReadAll(p.Stdout)
	require.NoError(t, err)
	assert.Equal(t, "first\nlast\n", string(out))
}

func TestKillReachesTheWholeGroup(t *testing.T) {
	t.Parallel()
	// The child shell starts a grandchild and reports its pid, then both
	// block. Killing the group must take the grandchild too.
	p, err := Start(context.Background(), sh(t, `sleep 1000 & echo $!; wait`))
	require.NoError(t, err)
	line, err := bufio.NewReader(p.Stdout).ReadString('\n')
	require.NoError(t, err)
	var grandchild int
	_, err = fmtSscan(strings.TrimSpace(line), &grandchild)
	require.NoError(t, err)

	p.Kill()
	assert.Equal(t, 128+int(syscall.SIGKILL), p.ExitCode())
	// The grandchild is gone (or a zombie awaiting init): signal 0 fails, or
	// it reports as not ours. Wait for it via its pid's absence.
	assert.Eventually(t, func() bool { return syscall.Kill(grandchild, 0) != nil }, testWait, testTick)
}

func TestSignalAfterExitIsANoOp(t *testing.T) {
	t.Parallel()
	p, err := Start(context.Background(), sh(t, `exit 0`))
	require.NoError(t, err)
	<-p.Done()
	assert.NoError(t, p.Signal(syscall.SIGTERM))
}

func TestStartRejectsAMissingCommand(t *testing.T) {
	t.Parallel()
	_, err := Start(context.Background(), &harness.LaunchSpec{})
	assert.Error(t, err)
	_, err = Start(context.Background(), &harness.LaunchSpec{Path: "/nonexistent/harness"})
	assert.ErrorContains(t, err, "/nonexistent/harness")
}

func TestTailKeepsTheLastBytes(t *testing.T) {
	t.Parallel()
	tl := &tail{max: 4}
	_, _ = tl.Write([]byte("abc"))
	_, _ = tl.Write([]byte("defg"))
	assert.Equal(t, "defg", tl.String())
}

func TestAgentEnv(t *testing.T) {
	t.Parallel()
	base := []string{
		"PATH=/bin", "HOME=/h", "GOPATH=/go", "SSH_AUTH_SOCK=/s",
		"ANTHROPIC_API_KEY=placeholder", "OPENAI_API_KEY=placeholder", "GEMINI_API_KEY=placeholder",
		"CLAUDE_CODE_SESSION_ID=parent", "CLAUDECODE=1",
		"CLAUDE_CODE_OAUTH_TOKEN=tok", "CLAUDE_CODE_USE_BEDROCK=1",
		"SLOP_BOT_AGENT_TOKEN=someone-elses", "malformed",
	}
	got := AgentEnv(&EnvOptions{
		Base: base,
		Drop: []string{"SLOP_BOT_AGENT_TOKEN"},
		Set:  map[string]string{"SLOP_BOT_AGENT_TOKEN": "mine", "HOME": "/override"},
	})
	assert.Equal(t, []string{
		"PATH=/bin", "GOPATH=/go", "SSH_AUTH_SOCK=/s",
		"CLAUDE_CODE_OAUTH_TOKEN=tok", "CLAUDE_CODE_USE_BEDROCK=1",
		"HOME=/override", "SLOP_BOT_AGENT_TOKEN=mine",
	}, got, "the user's toolchain variables pass; API keys, a parent harness's variables and another token don't")

	withKeys := AgentEnv(&EnvOptions{Base: base, APIKeyOptIn: true})
	assert.Contains(t, withKeys, "ANTHROPIC_API_KEY=placeholder")
	assert.NotContains(t, withKeys, "CLAUDECODE=1", "opting into API keys doesn't let the parent harness's variables through")
}

func TestWorkspaceEnvLayersTheWorkspaceOverTheHarness(t *testing.T) {
	t.Parallel()
	base := func() []string { return []string{"PATH=/bin", "OPENAI_API_KEY=placeholder", "MODE=user"} }
	ws := &harness.Workspace{Env: map[string]string{"SLOP_BOT_AGENT_TOKEN": "tok", "MODE": "workspace"}}
	got := WorkspaceEnv(base, ws, map[string]string{"MODE": "harness", "HARNESS_ONLY": "1"})
	assert.Equal(t, []string{"PATH=/bin", "HARNESS_ONLY=1", "MODE=workspace", "SLOP_BOT_AGENT_TOKEN=tok"}, got)

	ws.APIKeyOptIn = true
	assert.Contains(t, WorkspaceEnv(base, ws, nil), "OPENAI_API_KEY=placeholder")
	assert.NotEmpty(t, WorkspaceEnv(nil, &harness.Workspace{}, nil), "nil base is the process's own environment")
}

func TestOutputWrittenJustBeforeExitIsStillReadable(t *testing.T) {
	t.Parallel()
	p, err := Start(context.Background(), sh(t, `echo last words; exit 0`))
	require.NoError(t, err)
	// The reader only starts once the process has been waited on: the
	// harness's final line must still be there.
	<-p.Done()
	b, err := io.ReadAll(p.Stdout)
	require.NoError(t, err)
	assert.Equal(t, "last words\n", string(b))
}

func TestAChildHoldingStderrDoesntHideTheExit(t *testing.T) {
	t.Parallel()
	// The harness exits, leaving a child that keeps stderr open.
	p, err := start(context.Background(), sh(t, `sleep 1000 >/dev/null & echo $!; echo bye >&2; exit 4`), time.Millisecond)
	require.NoError(t, err)
	line, err := bufio.NewReader(p.Stdout).ReadString('\n')
	require.NoError(t, err)
	var orphan int
	_, err = fmtSscan(strings.TrimSpace(line), &orphan)
	require.NoError(t, err)
	t.Cleanup(func() { _ = syscall.Kill(orphan, syscall.SIGKILL) })

	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	select {
	case <-p.Done():
	case <-ctx.Done():
		t.Fatal("the exit was never reported")
	}
	assert.Equal(t, 4, p.ExitCode())
	// No assertion on the stderr tail: once the drain bound passes, how much
	// of it was copied is a race, and TestPipesAndExitCode covers it.
	assert.NoError(t, p.Err())
}
