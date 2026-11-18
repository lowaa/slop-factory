package mcpserver_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// An agent in a variant whose session runs another harness is told plainly
// why it is given nothing, as an answer rather than a failure to retry, and
// not as an empty queue.
func TestTicketNext_AgentOnAnotherHarnessIsToldWhyItGetsNothing(t *testing.T) {
	variant := &ticket.ServedVariant{VersionID: uuid.New(), Name: "fast", Version: 2, Harness: "pi"}
	r := newRig(t)
	r.tickets.claimErr = &ticket.VariantClaimError{Variant: variant, SessionHarness: "claude"}
	res, err := r.session.CallTool(t.Context(), &mcp.CallToolParams{Name: "ticket_next"})
	require.NoError(t, err)
	require.False(t, res.IsError, resultText(t, res))
	got := resultText(t, res)
	assert.Contains(t, got, "variant fast v2")
	assert.NotContains(t, got, "queue is empty")
	assert.Contains(t, got, "runs the pi harness, but this session runs claude")
}
