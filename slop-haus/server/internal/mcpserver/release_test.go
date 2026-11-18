package mcpserver_test

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func (r *rig) release(t *testing.T, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "ticket_release", Arguments: args})
	require.NoError(t, err)
	return res
}

func TestTicketRelease_RetryAfterIsParsedAndTheCooldownReported(t *testing.T) {
	r := newRig(t)
	r.tickets.lease = &ticket.Lease{TicketID: 1}
	until := time.Date(2026, 10, 10, 14, 30, 0, 0, time.UTC)
	r.tickets.releaseOutcome = &ticket.ReleaseOutcome{
		Ticket: &ticket.Ticket{ID: 1, Status: ticket.StatusBlocked}, RetryAt: &until,
	}

	res := r.release(t, map[string]any{"reason": "gate not met", "retry_after": "90m"})

	require.False(t, res.IsError, resultText(t, res))
	assert.Equal(t, 90*time.Minute, r.tickets.released.RetryAfter)
	assert.Contains(t, resultText(t, res), "14:30 on 10 Oct UTC")
}

func TestTicketRelease_ABadRetryAfterIsRefusedBeforeReleasing(t *testing.T) {
	r := newRig(t)
	r.tickets.lease = &ticket.Lease{TicketID: 1}

	res := r.release(t, map[string]any{"reason": "x", "retry_after": "soon"})

	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "retry_after")
	assert.Nil(t, r.tickets.released, "the ticket is still held")
}

func TestTicketRelease_AnEscalationTellsTheAgentItIsInDiscussion(t *testing.T) {
	r := newRig(t)
	r.tickets.lease = &ticket.Lease{TicketID: 1}
	r.tickets.releaseOutcome = &ticket.ReleaseOutcome{
		Ticket: &ticket.Ticket{ID: 1, Status: ticket.StatusDiscussion}, Escalated: true, Checks: 3,
	}

	res := r.release(t, map[string]any{"reason": "still not deployed"})

	require.False(t, res.IsError)
	assert.Contains(t, resultText(t, res), "#1 is in discussion")
	assert.Contains(t, resultText(t, res), "3 releases")
}
