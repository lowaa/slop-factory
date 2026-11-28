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

// A probe that exits 0 without reading its input, while a child it left
// behind holds its stdin, is a success (exec reports that as ErrWaitDelay):
// more input than a pipe holds keeps the copy into stdin blocked, so this
// does not depend on how fast anything runs. The shell points a background
// job's stdin at /dev/null, so the child gets the pipe as fd 3 instead.
func TestExecCommandsTakesAnExitThatLeftAChildHoldingItsStdin(t *testing.T) {
	t.Parallel()
	res, err := doctor.ExecCommands{Timeout: probeTimeout, WaitDelay: time.Millisecond}.Run(context.Background(), &doctor.Command{
		Path:  "/bin/sh",
		Args:  []string{"-c", `exec 3<&0; sleep 3600 & echo ok`},
		Input: strings.Repeat("x", 8<<20),
	})
	require.NoError(t, err)
	assert.Equal(t, "ok\n", res.Stdout)
	assert.Equal(t, 0, res.ExitCode)
}
