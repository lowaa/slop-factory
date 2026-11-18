package mcpserver_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/mcpserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// switchableBearer sends whichever token the test last set, as a client
// does across a token refresh.
type switchableBearer struct {
	mu    sync.Mutex
	token string
}

func (b *switchableBearer) use(token string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.token = token
}

func (b *switchableBearer) RoundTrip(req *http.Request) (*http.Response, error) {
	b.mu.Lock()
	token := b.token
	b.mu.Unlock()
	return bearer{token: token}.RoundTrip(req)
}

// Tools read the caller from the session's context, which the SDK builds
// from the initialize request. Without the session being bound to its
// initializer, a request carrying another agent's valid token would run as
// the initializer: whoever holds a token and someone else's session id
// could act as them.
func TestSession_ARequestAuthenticatedAsAnotherAgentIsRefused(t *testing.T) {
	alice, bob := identity{id: uuid.New()}, identity{id: uuid.New()}
	tokens := map[string]identity{"alice-token": alice, "alice-refreshed": alice, "bob-token": bob}
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

	aliceAuth := &switchableBearer{token: "alice-token"}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: srv.URL, HTTPClient: &http.Client{Transport: aliceAuth},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	tickets.lease = &ticket.Lease{TicketID: 1}

	// Sent by hand: the MCP client tears its session down on a refusal,
	// and the point after it is that alice's session is still hers.
	body := `{"jsonrpc":"2.0","id":99,"method":"tools/call",` +
		`"params":{"name":"ticket_discuss","arguments":{"question":"q","handoff":"h"}}}`
	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer bob-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Session-Id", session.ID())
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "bob's token must not drive alice's session")
	assert.Empty(t, tickets.discussed, "the refused call must not have run as anyone")

	// The session is bound to the agent, not the token: a client that has
	// refreshed its token keeps its session.
	aliceAuth.use("alice-refreshed")
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_discuss", Arguments: map[string]any{"question": "q", "handoff": "h"},
	})
	require.NoError(t, err, "alice's own session carries on")
	require.False(t, res.IsError, resultText(t, res))
	assert.Equal(t, alice.id, tickets.discussedBy)
}
