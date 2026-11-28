package doctor_test

import (
	"context"
	"strings"
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
