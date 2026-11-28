package pi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
)

// A tool's end too long to read (its whole output) still ends the tool.
func TestAnOversizedToolEndStillEndsTheTool(t *testing.T) {
	t.Parallel()
	c := testCodec()
	decodeAll(t, c, nil, agentStart)
	evs, err := c.Oversized([]byte(`{"type":"tool_execution_end","toolCallId":"c1","toolName":"bash","result":{"content":[{"type":"text","text":"lots`), nil)
	require.NoError(t, err)
	assert.Equal(t, []harness.Event{{Kind: harness.ToolEnded, Tool: "bash"}}, evs)
	evs, err = c.Oversized([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"lots`), nil)
	require.NoError(t, err)
	assert.Empty(t, evs, "a message cut off says nothing")
}
