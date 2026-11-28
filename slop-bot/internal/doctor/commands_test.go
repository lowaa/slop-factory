package doctor_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/doctor"
)

// A generous bound: these probes answer at once, and the timeout only
// stops a broken test hanging.
const probeTimeout = time.Minute

func TestExecCommandsReportsOutputAndExitCode(t *testing.T) {
	t.Parallel()
	sh, err := doctor.ExecCommands{}.LookPath("sh")
	require.NoError(t, err)
	res, err := doctor.ExecCommands{Timeout: probeTimeout}.Run(context.Background(), &doctor.Command{
		Path: sh, Args: []string{"-c", `read l; echo "out $l"; echo err >&2; exit 3`}, Input: "in\n",
	})
	require.NoError(t, err)
	assert.Equal(t, "out in\n", res.Stdout)
	assert.Equal(t, "err\n", res.Stderr)
	assert.Equal(t, 3, res.ExitCode)
}

// A server that would run forever is ended once Done sees its answer,
// with stdin kept open until then: exec replaces the shell with a sleep
// that only a kill ends. The timeout is far past go test's own, so only
// Done's kill can end the command: a regression hangs the test rather
// than passing late.
func TestExecCommandsEndsAServerOnceItAnswers(t *testing.T) {
	t.Parallel()
	res, err := doctor.ExecCommands{Timeout: 24 * time.Hour}.Run(context.Background(), &doctor.Command{
		Path:  "/bin/sh",
		Args:  []string{"-c", `read a; echo "one $a"; read b; echo "two $b"; exec sleep 3600`},
		Input: "a\nb\n",
		Done:  func(out string) bool { return strings.Contains(out, "two") },
	})
	require.NoError(t, err)
	assert.Equal(t, "one a\ntwo b\n", res.Stdout)
	assert.Equal(t, 0, res.ExitCode, "killed after answering counts as answered")
}

func TestExecCommandsTimesOutAHungProbe(t *testing.T) {
	t.Parallel()
	_, err := doctor.ExecCommands{Timeout: 10 * time.Millisecond}.Run(context.Background(), &doctor.Command{
		Path: "/bin/sh", Args: []string{"-c", "exec sleep 3600"},
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)

	_, err = doctor.ExecCommands{Timeout: 10 * time.Millisecond}.Run(context.Background(), &doctor.Command{
		Path: "/bin/sh", Args: []string{"-c", "exec sleep 3600"},
		Done: func(string) bool { return false },
	})
	require.ErrorIs(t, err, context.DeadlineExceeded, "a server that never answers ends at the timeout too")
}

func TestExecCommandsReportsAMissingBinary(t *testing.T) {
	t.Parallel()
	_, err := doctor.ExecCommands{}.LookPath("slop-bot-no-such-binary")
	require.Error(t, err)
	_, err = doctor.ExecCommands{Timeout: probeTimeout}.Run(context.Background(), &doctor.Command{Path: "/nonexistent/x"})
	require.Error(t, err)
}

// A probe that exits 0 but leaves a child holding its output is a success:
// its output is complete, and the child is ended with its group.
func TestExecCommandsTakesAnExitThatLeftAChildBehind(t *testing.T) {
	t.Parallel()
	res, err := doctor.ExecCommands{Timeout: probeTimeout, WaitDelay: time.Millisecond}.Run(context.Background(),
		&doctor.Command{Path: "/bin/sh", Args: []string{"-c", "sleep 3600 & echo ok"}})
	require.NoError(t, err)
	assert.Equal(t, "ok\n", res.Stdout)
	assert.Equal(t, 0, res.ExitCode)
}

// A child that left the probe's group survives the group's end; doctor
// stops reading what it holds open rather than wait on it, with or without
// Done. The timeout is far past go test's own, so a regression hangs.
func TestExecCommandsStopsReadingWhatAnEscapedChildHolds(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is not installed")
	}
	for name, done := range map[string]func(string) bool{
		"output":   nil,
		"a server": func(string) bool { return false },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			escaped, pidFile := filepath.Join(dir, "escaped"), filepath.Join(dir, "pid")
			require.NoError(t, syscall.Mkfifo(escaped, 0o600))
			// The probe exits only once the child has left its group (the
			// child's write to the fifo): otherwise the group's end could
			// catch it first.
			script := `setsid sh -c 'echo $$ > "$0"; exec sleep 3600' "$0" & read pid < "$0"; echo "$pid" > "$1"`
			res, err := doctor.ExecCommands{Timeout: 24 * time.Hour, WaitDelay: time.Millisecond, GroupDelay: time.Millisecond}.Run(context.Background(),
				&doctor.Command{Path: "/bin/sh", Args: []string{"-c", script, escaped, pidFile}, Done: done})
			require.NoError(t, err)
			assert.Equal(t, 0, res.ExitCode)
			b, err := os.ReadFile(pidFile)
			require.NoError(t, err)
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			require.NoError(t, err)
			assert.NoError(t, syscall.Kill(pid, syscall.SIGKILL), "the escaped child outlived the group")
		})
	}
}

func TestExecCommandsRunsWithTheGivenEnvironment(t *testing.T) {
	t.Parallel()
	res, err := doctor.ExecCommands{Timeout: probeTimeout}.Run(context.Background(), &doctor.Command{
		Path: "/bin/sh", Args: []string{"-c", `echo "${ONLY:-unset} ${HOME:-nohome}"`}, Env: []string{"ONLY=this"},
	})
	require.NoError(t, err)
	assert.Equal(t, "this nohome\n", res.Stdout)
}
