package doctortest_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/doctor"
	"github.com/lowaa/slop-factory/slop-bot/internal/doctor/doctortest"
)

const (
	initialize  = `{"id":1,"method":"initialize","params":{}}`
	initialized = `{"method":"initialized"}`
	modelList   = `{"id":2,"method":"model/list","params":{}}`
)

// The fake Codex refuses the model list to a client that sent initialized
// without waiting for initialize's reply, as a strict app-server may.
func TestCodexServerRefusesABatchedHandshake(t *testing.T) {
	t.Parallel()
	c := doctortest.NewCommands()
	c.Serve("codex app-server", doctortest.CodexServer)
	res, err := c.Run(context.Background(), &doctor.Command{
		Path: "/bin/codex", Args: []string{"app-server"},
		Input: initialize + "\nnot json\n" + initialized + "\n" + modelList + "\n",
	})
	require.NoError(t, err, "without Done, the output is whatever the server said")
	assert.Contains(t, res.Stdout, `{"id":2,"error":{"code":-32600,"message":"Not initialized"}}`)
	assert.Equal(t, [][]string{{initialize, "not json", initialized, modelList}}, c.Writes("codex app-server"))
}

func TestAServerThatNeverGivesTheAnswerFails(t *testing.T) {
	t.Parallel()
	c := doctortest.NewCommands()
	c.Serve("codex app-server", doctortest.CodexServer)
	_, err := c.Run(context.Background(), &doctor.Command{
		Path: "/bin/codex", Args: []string{"app-server"}, Input: initialize + "\n",
		Done: func(string) bool { return false },
	})
	require.ErrorContains(t, err, "never gave the answer Done waits for")
}
