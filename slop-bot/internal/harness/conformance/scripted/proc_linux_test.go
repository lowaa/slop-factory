//go:build linux

package scripted

import (
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A harness launched through the launcher is ended at cleanup, and a pid
// merely reported (not the reporter's own) is never touched. The sleeps are
// this test's own children, which it reaps.
func TestEndLaunchedKillsOnlyWhatTheLauncherStarted(t *testing.T) {
	t.Parallel()
	sleep, err := exec.LookPath("sleep")
	require.NoError(t, err)
	p := newBarePuppet(t)

	bin, env, err := p.Launcher(sleep)
	require.NoError(t, err)
	// Short, so an endLaunched that kills nothing fails rather than hangs.
	launched := exec.Command(bin, "20") // #nosec G204 -- this test binary, as the launcher
	launched.Env = os.Environ()
	for k, v := range env {
		launched.Env = append(launched.Env, k+"="+v)
	}
	require.NoError(t, launched.Start())
	pid, err := receive(t.Context(), p.pids, "no launch")
	require.NoError(t, err)
	require.Equal(t, launched.Process.Pid, pid, "the launcher reports the pid it execs in")

	bystander := exec.Command(sleep, "600") // #nosec G204 -- a fixed command
	require.NoError(t, bystander.Start())
	t.Cleanup(func() {
		_ = bystander.Process.Kill()
		_ = bystander.Wait()
	})
	require.NoError(t, reportPid(env[envLaunchSocket], bystander.Process.Pid))
	_, err = receive(t.Context(), p.pids, "no report")
	require.NoError(t, err)

	p.endLaunched()
	err = launched.Wait()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	status, ok := exit.Sys().(syscall.WaitStatus)
	require.True(t, ok)
	assert.Equal(t, syscall.SIGKILL, status.Signal(), "the launched harness was killed")
	// Not kill(pid, 0): a killed child that isn't reaped yet still answers.
	var ws syscall.WaitStatus
	reaped, err := syscall.Wait4(bystander.Process.Pid, &ws, syscall.WNOHANG, nil)
	require.NoError(t, err)
	assert.Zero(t, reaped, "a reported pid that isn't the reporter's is left alone (it exited: %v)", ws)
}
