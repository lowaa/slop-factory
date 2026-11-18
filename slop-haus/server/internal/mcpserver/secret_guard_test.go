package mcpserver_test

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Built at run time so no source line looks like a credential.
var fakeGitHubToken = "ghp_" + "aB3dE6gH9jK2mN5pQ8sT1vW4yZ7bC0eF3hJ6"

type refusalCounts struct {
	mu    sync.Mutex
	kinds map[string]int
}

func (c *refusalCounts) SecretWriteRefused(kind string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.kinds == nil {
		c.kinds = map[string]int{}
	}
	c.kinds[kind]++
}

func (c *refusalCounts) count(kind string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.kinds[kind]
}

// The agent's write is refused with the kind named, nothing is stored, and
// the value reaches no response, log line or audit record.
func TestSecretGuard_RefusesAnAgentWriteAndNeverEchoesTheValue(t *testing.T) {
	var logs, auditLogs bytes.Buffer
	counts := &refusalCounts{}
	r := newRigWith(t, &rigOptions{
		logs:      slog.NewJSONHandler(&logs, nil),
		auditLogs: slog.NewJSONHandler(&auditLogs, nil),
		refusals:  counts,
	})
	r.tickets.lease = &ticket.Lease{TicketID: 7}
	leaky := "the token is " + fakeGitHubToken + " ok"

	calls := []struct {
		tool string
		args map[string]any
	}{
		{"ticket_comment", map[string]any{"ticket_id": 7, "body": leaky}},
		{"ticket_discuss", map[string]any{"question": "Which?", "handoff": leaky}},
		{"ticket_discuss", map[string]any{"question": leaky, "handoff": "fine"}},
		{"ticket_create", map[string]any{"title": "Fine", "body": leaky}},
		{"ticket_edit", map[string]any{"revision": 0, "body": leaky}},
	}
	for _, c := range calls {
		res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: c.tool, Arguments: c.args})
		require.NoError(t, err, c.tool)
		assert.True(t, res.IsError, c.tool)
		text := resultText(t, res)
		assert.Contains(t, text, "a GitHub token", c.tool)
		assert.NotContains(t, text, fakeGitHubToken, c.tool)
	}

	assert.Nil(t, r.tickets.edited)
	assert.Nil(t, r.tickets.created)
	assert.Zero(t, r.tickets.commented)
	assert.Empty(t, r.tickets.discussed)
	assert.Zero(t, r.sessions.progress, "a refused write is not progress")
	assert.Equal(t, len(calls), r.events.Count(audit.SecretWriteRefused))
	assert.Equal(t, len(calls), counts.count("github_token"))
	assert.NotContains(t, logs.String(), fakeGitHubToken)
	assert.NotContains(t, auditLogs.String(), fakeGitHubToken)
	assert.Contains(t, auditLogs.String(), "secret_write_refused")
	assert.Contains(t, auditLogs.String(), "github_token")
	assert.Contains(t, auditLogs.String(), `"ticket_id":7`)
}

func TestSecretGuard_AllowsOrdinaryText(t *testing.T) {
	r := newRig(t)
	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_comment", Arguments: map[string]any{"ticket_id": 7,
			"body": "Merged f28ee459b6f112c642bbe0ba54beb1b2ec107ae1 for agent e29c8fdf-8ce4-454e-be84-c1a15d076278; the token is refreshed hourly."},
	})
	require.NoError(t, err)
	assert.False(t, res.IsError, resultText(t, res))
	assert.Equal(t, 1, r.tickets.commented, "an ordinary comment is stored")
	assert.Zero(t, r.events.Count(audit.SecretWriteRefused))
}

// Every propose_* tool is covered, since a console's proposal is stored for
// a person to read.
func TestSecretGuard_RefusesProposals(t *testing.T) {
	r := newRealRig(t)
	tk := r.forceStatus(ticket.StatusPreparing)
	c := r.agentSession("console", true)
	leaky := "use " + fakeGitHubToken

	for tool, args := range map[string]map[string]any{
		"propose_discussion_answer": {"ticket_id": tk.ID, "body": leaky},
		"propose_status":            {"ticket_id": tk.ID, "status": "parked", "reason": leaky},
		"propose_ticket_edit":       {"ticket_id": tk.ID, "base_revision": 0, "body": leaky},
		"propose_close_merged":      {"ticket_id": tk.ID, "reason": leaky},
	} {
		res := c.call(t, tool, args)
		assert.True(t, res.IsError, tool)
		assert.Contains(t, resultText(t, res), "a GitHub token", tool)
		assert.NotContains(t, resultText(t, res), fakeGitHubToken, tool)
	}
	assert.Zero(t, r.proposalCount())
	assert.Equal(t, 4, r.events.Count(audit.SecretWriteRefused))
}

// The guard is on the agents' door. A person's board comment goes through
// the ticket service directly and may hold whatever they choose to paste.
func TestSecretGuard_ABoardCommentIsNotChecked(t *testing.T) {
	r := newRealRig(t)
	tk := r.forceStatus(ticket.StatusPreparing)

	_, err := r.tickets.AddComment(r.ctx, tk.ID, ticket.Author{UserID: &r.human}, "pasted: "+fakeGitHubToken)
	require.NoError(t, err)
	assert.Zero(t, r.events.Count(audit.SecretWriteRefused))
}
