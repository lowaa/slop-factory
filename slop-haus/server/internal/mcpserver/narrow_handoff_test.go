package mcpserver_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestHandOff_NarrowsTheScopeToTheChangeFirst(t *testing.T) {
	cases := map[string]struct {
		tool string
		args map[string]any
	}{
		"release":          {"ticket_release", map[string]any{"reason": "x"}},
		"discuss":          {"ticket_discuss", map[string]any{"question": "q", "handoff": "h"}},
		"finish -> review": {"ticket_finish", map[string]any{"handoff": "h", "request_review": true, "reason": "risky", "pr_url": testPR}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			changes := &fakeChanges{review: &ticket.Ticket{ID: 1}}
			r := newChangesRig(t, changes)
			r.tickets.lease = &ticket.Lease{TicketID: 1}
			r.tickets.releaseOutcome = &ticket.ReleaseOutcome{Ticket: &ticket.Ticket{ID: 1}}

			res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})

			require.NoError(t, err)
			require.False(t, res.IsError, resultText(t, res))
			assert.Equal(t, []uuid.UUID{r.agentID}, changes.narrows)
		})
	}
}

func TestHandOff_AScopeThatCannotBeNarrowedDoesNotFailIt(t *testing.T) {
	changes := &fakeChanges{narrowErr: errors.New("host unreachable")}
	r := newChangesRig(t, changes)
	r.tickets.lease = &ticket.Lease{TicketID: 1}
	r.tickets.releaseOutcome = &ticket.ReleaseOutcome{Ticket: &ticket.Ticket{ID: 1}}

	res := r.release(t, map[string]any{"reason": "x"})

	require.False(t, res.IsError, resultText(t, res))
	require.NotNil(t, r.tickets.released, "the ticket was still released")
	assert.Len(t, changes.narrows, 1)
}

func TestMidRun_ChangeToolsDoNotNarrowTheScope(t *testing.T) {
	changes := &fakeChanges{report: &change.Report{}}
	r := newChangesRig(t, changes)
	r.tickets.lease = &ticket.Lease{TicketID: 1}

	_, _ = r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "change_checks", Arguments: map[string]any{}})

	assert.Empty(t, changes.narrows)
}

func TestFinish_NarrowsTheScopeBeforeTheChangeIsLanded(t *testing.T) {
	changes := &fakeChanges{}
	r := newChangesRig(t, changes)
	r.tickets.lease = &ticket.Lease{TicketID: 1}

	_, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_finish", Arguments: map[string]any{"handoff": "h", "pr_url": testPR},
	})

	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{r.agentID}, changes.narrows)
}
