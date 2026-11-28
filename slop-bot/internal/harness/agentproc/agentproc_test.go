package agentproc

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

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
		"SLOP_BOT_AGENT_TOKEN=someone-elses", "malformed",
	}
	got := AgentEnv(&EnvOptions{
		Base: base,
		Drop: []string{"SLOP_BOT_AGENT_TOKEN"},
		Set:  map[string]string{"SLOP_BOT_AGENT_TOKEN": "mine", "HOME": "/override"},
	})
	assert.Equal(t, []string{
		"PATH=/bin", "GOPATH=/go", "SSH_AUTH_SOCK=/s",
		"HOME=/override", "SLOP_BOT_AGENT_TOKEN=mine",
	}, got, "the user's toolchain variables pass; API keys, a parent harness's variables and another token don't")

	withKeys := AgentEnv(&EnvOptions{Base: base, APIKeyOptIn: true})
	assert.Contains(t, withKeys, "ANTHROPIC_API_KEY=placeholder")
	assert.NotContains(t, withKeys, "CLAUDECODE=1", "opting into API keys doesn't let the parent harness's variables through")
}
