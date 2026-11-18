package mcpserver_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/mcpserver"
)

// The smoke test's credential reads a ticket and nothing else -- and the
// read leaves no trace: no last-seen time, no heartbeat on a lease.
func TestAReadOnlyCallerMayReadATicketWithoutSeemingLive(t *testing.T) {
	r := newRigAs(t, true)

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_get", Arguments: map[string]any{"ticket_id": 7},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, resultText(t, res))
	assert.Contains(t, resultText(t, res), "#7 A ticket")

	assert.Zero(t, r.sessions.touched, "a read-only caller is not the agent at work")
	assert.Zero(t, r.tickets.heartbeats, "nor keeps a lease alive")
}

// Every other tool is refused, whatever arguments it was sent. Walked from
// the server's own list, so a tool added later is covered without anyone
// remembering this test.
func TestAReadOnlyCallerIsRefusedEveryOtherTool(t *testing.T) {
	changes := &fakeChanges{}
	r := newRigWith(t, &rigOptions{readOnly: true, changes: changes})

	tools, err := r.session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	refused := 0
	for _, tool := range tools.Tools {
		if tool.Name == "ticket_get" {
			continue
		}
		_, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool.Name, Arguments: map[string]any{}})
		if assert.Error(t, err, tool.Name) {
			assert.Contains(t, err.Error(), "read-only", tool.Name)
		}
		refused++
	}
	require.Positive(t, refused)
	assert.Equal(t, refused, r.events.Count(audit.ReadOnlyRefused))
	assert.Zero(t, changes.calls(), "the change tools are among them")

	assert.Nil(t, r.tickets.created)
	assert.Zero(t, r.tickets.commented)
	assert.Zero(t, r.tickets.heartbeats)
	assert.Zero(t, r.sessions.touched)
	assert.Zero(t, r.sessions.progress)
}

// Not just tools: every MCP method outside the handshake and tools/list is
// closed to it, including ones this server doesn't implement -- the guard
// runs before the server decides what it offers.
func TestAReadOnlyCallerIsRefusedOtherMethods(t *testing.T) {
	r := newRigAs(t, true)
	_, err := r.session.ListPrompts(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read-only")
	_, err = r.session.ListResources(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read-only")
	assert.Equal(t, 2, r.events.Count(audit.ReadOnlyRefused))
}

// The same write from the agent's own credential goes through -- the guard
// keys on the credential, not the tool.
func TestAFullCredentialIsNotRefusedAsReadOnly(t *testing.T) {
	r := newRig(t)
	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_create", Arguments: map[string]any{"title": "x", "body": "y"},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, resultText(t, res))
	require.NotNil(t, r.tickets.created)
	assert.Equal(t, "x", r.tickets.created.Title)
	assert.Zero(t, r.events.Count(audit.ReadOnlyRefused))
}

// Sessions are bound to their agent (TestSession_ARequestAuthenticatedAs...)
// and to whether its credential is read-only: a read-only credential
// presenting a session its agent's full credential opened would otherwise
// act with the full identity the session carries.
func TestAReadOnlyCredentialCannotCarryOnAFullSession(t *testing.T) {
	agentID := uuid.New()
	tokens := map[string]identity{"full": {id: agentID}, "read-only": {id: agentID, readOnly: true}}
	tickets := &fakeTickets{}
	handler := mcpserver.NewHTTPHandler(mcpserver.Deps{
		Tickets: tickets, Sessions: &fakeSessions{}, Agents: fakeAgents{}, Roles: &fakeRoles{},
	}, func(_ context.Context, token string) (authn.Identity, error) {
		found, ok := tokens[token]
		if !ok {
			return nil, errors.New("unknown token")
		}
		return found, nil
	}, resourceMetadataURL, slog.New(slog.DiscardHandler))
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: srv.URL, HTTPClient: &http.Client{Transport: bearer{token: "full"}},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	body := `{"jsonrpc":"2.0","id":99,"method":"tools/call",` +
		`"params":{"name":"ticket_create","arguments":{"title":"x","body":"y"}}}`
	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer read-only")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Session-Id", session.ID())
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Nil(t, tickets.created, "the write must not have run with the full identity")
}
