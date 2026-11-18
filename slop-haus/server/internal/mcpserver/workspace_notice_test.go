package mcpserver_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// fakeWorkspace says what the test gives it and records what it was asked.
type fakeWorkspace struct {
	notice string
	scopes []string
	agent  uuid.UUID
}

func (f *fakeWorkspace) ClaimNotice(_ context.Context, agentID uuid.UUID, scopes []string) string {
	f.scopes, f.agent = scopes, agentID
	return f.notice
}

func claimWith(t *testing.T, ws *fakeWorkspace) (string, *mcp.CallToolResult, uuid.UUID) {
	t.Helper()
	r := newRigWith(t, &rigOptions{workspace: ws})
	r.tickets.claim = &ticket.ClaimResult{Ticket: &ticket.Ticket{
		ID: 7, Title: "Fix app", Status: ticket.StatusInProgress,
		Paths: []model.Path{{Path: "app/src/"}, {Path: "app/docs/"}},
	}}
	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "ticket_next"})
	require.NoError(t, err)
	return resultText(t, res), res, r.agentID
}

func TestTicketNext_AWorkspaceMismatchIsSaidAtTheClaim(t *testing.T) {
	ws := &fakeWorkspace{notice: "WORKSPACE MISMATCH: \"app\": no repo"}
	body, res, caller := claimWith(t, ws)

	assert.Equal(t, []string{"app/src/", "app/docs/"}, ws.scopes, "the check is made against the ticket's scopes")
	assert.Contains(t, body, "Granted ticket #7", "the ticket is still the agent's to release")
	assert.Equal(t, caller, ws.agent, "the check is of the agent that was granted the ticket")
	assert.True(t, strings.HasSuffix(body, ws.notice), "the notice comes last, after everything else the claim says")
	out, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, ws.notice, out["workspace_mismatch"])
}

func TestTicketNext_NothingToSayAboutTheWorkspaceSaysNothing(t *testing.T) {
	body, res, _ := claimWith(t, &fakeWorkspace{})

	assert.NotContains(t, body, "WORKSPACE")
	out, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, out, "workspace_mismatch")
}
