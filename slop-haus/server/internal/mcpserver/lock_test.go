package mcpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/mcpserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func callLockTool(t *testing.T, r *rig, tool string) (*mcp.CallToolResult, map[string]any) {
	t.Helper()
	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool})
	require.NoError(t, err)
	var out map[string]any
	if res.StructuredContent != nil {
		raw, err := json.Marshal(res.StructuredContent)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, &out))
	}
	return res, out
}

func TestIntegrationLock_ARefusalReachesTheAgentWithItsPlaceInTheQueue(t *testing.T) {
	r := newRig(t)
	r.tickets.lockErr = &ticket.LockHeldError{
		Holder:   &ticket.LockHolder{AgentName: "merger", TicketID: 7, TicketTitle: "Seven", AcquiredAt: time.Date(2026, 10, 3, 15, 0, 0, 0, time.UTC)},
		Position: 3,
		Ahead: []ticket.LockQueueEntry{
			{AgentName: "first", TicketID: 3, QueuedAt: time.Date(2026, 10, 3, 15, 5, 0, 0, time.UTC)},
			{AgentName: "second", TicketID: 4, QueuedAt: time.Date(2026, 10, 3, 15, 6, 0, 0, time.UTC)},
		},
	}

	res, _ := callLockTool(t, r, "integration_lock")

	require.True(t, res.IsError)
	body := resultText(t, res)
	assert.Contains(t, body, "number 3 in the queue")
	assert.Contains(t, body, `agent "first"`)
	assert.Contains(t, body, `agent "second"`)
	assert.Contains(t, body, "2026-10-03T15:06:00Z")
	assert.Contains(t, body, "end your turn")
}

type queueModeFunc func() (bool, error)

func (f queueModeFunc) Enabled(context.Context) (bool, error) { return f() }

func toolNames(t *testing.T, r *rig) map[string]bool {
	t.Helper()
	list, err := r.session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	names := map[string]bool{}
	for _, tool := range list.Tools {
		names[tool.Name] = true
	}
	return names
}

func TestLockTools_FollowTheTenantsMergeMethod(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   mcpserver.QueueMode
		offers bool
	}{
		{"worker", queueModeFunc(func() (bool, error) { return false, nil }), true},
		{"no mode wired (kill switch off)", nil, true},
		{"mode unreadable", queueModeFunc(func() (bool, error) { return false, errors.New("db down") }), true},
		{"merge_queue", queueModeFunc(func() (bool, error) { return true, nil }), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRigWith(t, &rigOptions{queueMode: tc.mode})

			names := toolNames(t, r)
			assert.Equal(t, tc.offers, names["integration_lock"])
			assert.Equal(t, tc.offers, names["integration_unlock"])
			assert.True(t, names["ticket_finish"])

			for _, tool := range []string{"integration_lock", "integration_unlock"} {
				res, _ := callLockTool(t, r, tool)
				if tc.offers {
					assert.False(t, res.IsError, tool)
					continue
				}
				require.True(t, res.IsError, tool)
				assert.Contains(t, resultText(t, res), "this tenant lands by the merge queue: call ticket_finish")
			}
		})
	}
}

func TestLockTools_ComeBackWhenTheTenantSwitchesToWorker(t *testing.T) {
	queued := true
	r := newRigWith(t, &rigOptions{queueMode: queueModeFunc(func() (bool, error) { return queued, nil })})
	assert.False(t, toolNames(t, r)["integration_lock"])

	queued = false
	assert.True(t, toolNames(t, r)["integration_lock"])
}
