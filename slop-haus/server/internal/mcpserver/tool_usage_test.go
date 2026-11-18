package mcpserver_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/usagemetrics"
)

type toolCall struct {
	tool, kind string
	outcome    usagemetrics.Outcome
}

type fakeToolUsage struct {
	mu    sync.Mutex
	known []string
	calls []toolCall
}

func (f *fakeToolUsage) Known(tool string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.known = append(f.known, tool)
}

func (f *fakeToolUsage) ToolCall(tool, kind string, outcome usagemetrics.Outcome, _ time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, toolCall{tool, kind, outcome})
}

func (f *fakeToolUsage) last(t *testing.T) toolCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.NotEmpty(t, f.calls)
	return f.calls[len(f.calls)-1]
}

func TestToolUsage_EveryCallIsCountedOnceWithItsOutcome(t *testing.T) {
	usage := &fakeToolUsage{}
	r := newRigWith(t, &rigOptions{toolUsage: usage})
	r.tickets.lease = &ticket.Lease{TicketID: 7}
	ctx := context.Background()

	assert.Contains(t, usage.known, "ticket_comment")

	call := func(tool string, args map[string]any) {
		_, err := r.session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
		require.NoError(t, err)
	}

	call("ticket_comment", map[string]any{"ticket_id": 7, "body": "fine"})
	assert.Equal(t, toolCall{"ticket_comment", "worker", usagemetrics.OutcomeOK}, usage.last(t))

	call("ticket_comment", map[string]any{"ticket_id": 7, "body": "token " + fakeGitHubToken})
	assert.Equal(t, toolCall{"ticket_comment", "worker", usagemetrics.OutcomeRefused}, usage.last(t))

	call("ticket_comment", map[string]any{"ticket_id": "not a number"})
	assert.Equal(t, toolCall{"ticket_comment", "worker", usagemetrics.OutcomeInvalid}, usage.last(t))

	assert.Len(t, usage.calls, 3, "one count per call")
}

func TestToolUsage_ASecretNeverReachesTheLabels(t *testing.T) {
	usage := &fakeToolUsage{}
	r := newRigWith(t, &rigOptions{toolUsage: usage})
	_, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "ticket_comment",
		Arguments: map[string]any{"ticket_id": 7, "body": fakeGitHubToken}})
	require.NoError(t, err)
	for _, c := range usage.calls {
		assert.NotContains(t, c.tool+c.kind, fakeGitHubToken)
	}
}

func TestToolUsage_ReadOnlyRefusalIsCounted(t *testing.T) {
	usage := &fakeToolUsage{}
	r := newRigWith(t, &rigOptions{toolUsage: usage, readOnly: true})
	_, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "ticket_comment",
		Arguments: map[string]any{"ticket_id": 7, "body": "x"}})
	require.Error(t, err)
	assert.Equal(t, toolCall{"ticket_comment", "worker", usagemetrics.OutcomeRefused}, usage.last(t))
}

func TestToolUsage_QueueLockRefusalIsRefusedNotInvalid(t *testing.T) {
	usage := &fakeToolUsage{}
	r := newRigWith(t, &rigOptions{toolUsage: usage, queueMode: queueModeFunc(func() (bool, error) { return true, nil })})
	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "integration_lock", Arguments: map[string]any{}})
	require.NoError(t, err)
	require.True(t, res.IsError)
	assert.Equal(t, toolCall{"integration_lock", "worker", usagemetrics.OutcomeRefused}, usage.last(t))
}
