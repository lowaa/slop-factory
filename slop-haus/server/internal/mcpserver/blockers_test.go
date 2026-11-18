package mcpserver_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func (f *fakeTickets) AgentAddDependency(_ context.Context, agentID uuid.UUID, _, _ int64) (*ticket.DependencyChange, error) {
	f.depAgent = agentID
	return f.depChange, nil
}

func (f *fakeTickets) AgentRemoveDependency(_ context.Context, agentID uuid.UUID, _, _ int64) (*ticket.DependencyChange, error) {
	f.depAgent = agentID
	return f.depChange, nil
}

// Re-adding an edge changes nothing, so it must not reset the stop-hook
// loop breaker; a real change must.
func TestTicketBlockers_OnlyAChangeIsProgress(t *testing.T) {
	for _, tool := range []string{"ticket_blocker_add", "ticket_blocker_remove"} {
		for _, changed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s changed=%v", tool, changed), func(t *testing.T) {
				r := newRig(t)
				r.tickets.depChange = &ticket.DependencyChange{
					Changed: changed, Ticket: &ticket.Ticket{ID: 5, Status: ticket.StatusOpen},
				}
				res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
					Name: tool, Arguments: map[string]any{"ticket_id": 5, "blocker_id": 3},
				})
				require.NoError(t, err)
				require.False(t, res.IsError, resultText(t, res))
				assert.NotEqual(t, uuid.Nil, r.tickets.depAgent, "it acts as the authenticated agent")
				if changed {
					assert.Positive(t, r.sessions.progress)
					assert.NotContains(t, resultText(t, res), "nothing changed")
				} else {
					assert.Zero(t, r.sessions.progress)
					assert.Contains(t, resultText(t, res), "nothing changed")
				}
			})
		}
	}
}

// The motivating case, end to end: an agent splits its work into two
// follow-ups chained by a blocker, and reads back what it built.
func TestTicketBlockers_ThroughTheRealStore(t *testing.T) {
	r := newRealRig(t)
	held := r.openTicket("Being split")
	claimed := r.call("ticket_next", map[string]any{"ticket_id": held.ID})
	require.False(t, claimed.IsError, resultText(t, claimed))

	first := r.call("ticket_create", map[string]any{"title": "First half", "status": "open"})
	require.False(t, first.IsError, resultText(t, first))
	firstID := structuredID(t, first)

	second := r.call("ticket_create", map[string]any{"title": "Second half", "status": "open", "blocked_on": []int64{firstID}})
	require.False(t, second.IsError, resultText(t, second))
	secondID := structuredID(t, second)
	assert.Contains(t, resultText(t, second), fmt.Sprintf("Filed #%d (blocked), blocked by: #%d First half (open)", secondID, firstID))

	assert.Equal(t, []any{map[string]any{"id": float64(firstID), "title": "First half", "status": "open"}},
		structured(t, second)["blocked_by"])

	read := r.call("ticket_get", map[string]any{"ticket_id": secondID})
	assert.Equal(t, []any{map[string]any{"id": float64(firstID), "title": "First half", "status": "open"}},
		structured(t, read)["blocked_by"])
	assert.Contains(t, resultText(t, read), fmt.Sprintf("Blocked by: #%d First half (open)", firstID))
	assert.Contains(t, resultText(t, read), fmt.Sprintf("Blocked on #%d: First half", firstID), "recorded as a comment")

	// The held ticket, too, and it stays the agent's to work.
	res := r.call("ticket_blocker_add", map[string]any{"ticket_id": held.ID, "blocker_id": firstID})
	require.False(t, res.IsError, resultText(t, res))
	assert.Contains(t, resultText(t, res), fmt.Sprintf("#%d now waits on #%d. #%d is in_progress.", held.ID, firstID, held.ID))
	assert.Equal(t, map[string]any{
		"changed": true, "status": "in_progress",
		"blocked_by": []any{map[string]any{"id": float64(firstID), "title": "First half", "status": "open"}},
	}, structured(t, res))

	res = r.call("ticket_blocker_remove", map[string]any{"ticket_id": secondID, "blocker_id": firstID})
	require.False(t, res.IsError, resultText(t, res))
	assert.Contains(t, resultText(t, res), fmt.Sprintf("#%d is open. Blocked by: nothing", secondID))

	res = r.call("ticket_blocker_remove", map[string]any{"ticket_id": secondID, "blocker_id": firstID})
	require.False(t, res.IsError, resultText(t, res))
	assert.Contains(t, resultText(t, res), "nothing changed")
	assert.Equal(t, map[string]any{"changed": false, "status": "open", "blocked_by": []any{}}, structured(t, res))
}

func TestTicketBlockers_RefusalsTellTheAgentWhatToDoInstead(t *testing.T) {
	r := newRealRig(t)
	mine := r.call("ticket_create", map[string]any{"title": "Mine", "status": "open"})
	mineID := structuredID(t, mine)
	personal := r.openTicket("A person's")
	holdBack := r.openTicket("Held back")

	res := r.call("ticket_blocker_add", map[string]any{"ticket_id": personal.ID, "blocker_id": mineID})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "comment on it")

	_, err := r.tickets.AddDependency(r.ctx, ticket.UserAuthor(r.human), mineID, holdBack.ID)
	require.NoError(t, err)
	res = r.call("ticket_blocker_remove", map[string]any{"ticket_id": mineID, "blocker_id": holdBack.ID})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "only a blocker it added itself")

	res = r.call("ticket_blocker_add", map[string]any{"ticket_id": holdBack.ID, "blocker_id": holdBack.ID})
	assert.True(t, res.IsError)
}

func structured(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	out, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok, "structured output: %#v", res.StructuredContent)
	return out
}

func structuredID(t *testing.T, res *mcp.CallToolResult) int64 {
	t.Helper()
	id, ok := structured(t, res)["id"].(float64)
	require.True(t, ok)
	return int64(id)
}
