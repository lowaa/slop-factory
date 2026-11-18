package mcpserver_test

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// A tenant with nothing set up is told so as an answer to stop on, not as a
// tool error to retry and not as an empty queue.
func TestTicketNext_SetupIncompleteIsAnAnswerNamingTheGap(t *testing.T) {
	r := newRig(t)
	r.tickets.claimErr = &ticket.SetupIncompleteError{Gap: "no repo declared"}

	res, err := r.session.CallTool(t.Context(), &mcp.CallToolParams{Name: "ticket_next"})

	require.NoError(t, err)
	require.False(t, res.IsError, resultText(t, res))
	got := resultText(t, res)
	assert.Contains(t, got, "no repo declared")
	assert.NotContains(t, got, "queue is empty")
}
