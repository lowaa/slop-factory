package daemon

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/paths"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

// liveAgent records a live agent on ticket and returns a client acting as it.
func liveAgent(t *testing.T, d *Daemon, p *paths.Paths, ticket string) *control.Client {
	t.Helper()
	token, err := control.NewToken()
	require.NoError(t, err)
	_, err = d.state.Agents.Create(context.Background(), &state.NewAgent{
		ID: "a1", Ticket: ident.TicketID(ticket), Harness: "claude-code", Driver: "stream-json",
		Pool: state.PoolNormal, State: state.StateStarting, TokenHash: control.HashToken(token),
	})
	require.NoError(t, err)
	return control.NewClient(p.Socket(), token)
}

func ticketPath(id string) string { return strings.Replace(api.PathTicket, "{id}", id, 1) }

func TestASpecChangeIsRefusedWhileTheTicketsAgentLives(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	d := newDaemon(t, p)
	run(t, d, p)
	ctx := context.Background()
	user := control.NewClient(p.Socket(), "")

	var tk api.Ticket
	require.NoError(t, user.Do(ctx, http.MethodPost, api.PathTickets, &api.NewTicket{Title: "t"}, &tk))
	effort := "low"
	change := &api.TicketChange{Agent: &api.AgentSpecChange{Effort: &effort}}
	require.NoError(t, user.Do(ctx, http.MethodPatch, ticketPath(tk.ID), change, nil), "no agent yet")

	liveAgent(t, d, p, tk.ID)
	effort = "high"
	err := user.Do(ctx, http.MethodPatch, ticketPath(tk.ID), change, nil)
	var apiErr *control.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusConflict, apiErr.Status)
	assert.Contains(t, apiErr.Message, "agent is running")

	title := "renamed"
	require.NoError(t, user.Do(ctx, http.MethodPatch, ticketPath(tk.ID), &api.TicketChange{Title: &title}, nil), "other edits are fine")
}

func TestTellAllRefusesInterrupt(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	d := newDaemon(t, p)
	run(t, d, p)
	err := control.NewClient(p.Socket(), "").Do(context.Background(), http.MethodPost, api.PathTell,
		&api.Tell{All: true, Interrupt: true, Text: "stop"}, nil)
	var apiErr *control.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.Status)
	assert.Contains(t, apiErr.Message, "--interrupt")
}

func TestAnAgentsInboxSkipsWhatWasAlreadyPushedToIt(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	d := newDaemon(t, p)
	run(t, d, p)
	ctx := context.Background()
	user := control.NewClient(p.Socket(), "")

	var tk api.Ticket
	require.NoError(t, user.Do(ctx, http.MethodPost, api.PathTickets, &api.NewTicket{Title: "t"}, &tk))
	agent := liveAgent(t, d, p, tk.ID)
	msgs := d.state.Messages
	toAgent, err := msgs.Enqueue(ctx, &state.NewMessage{From: state.UserParty, To: "a1", Kind: state.KindTell, Body: "old instruction"})
	require.NoError(t, err)
	require.NoError(t, msgs.MarkDelivered(ctx, toAgent.ID, state.DeliveryTurn))
	_, err = msgs.Enqueue(ctx, &state.NewMessage{From: state.UserParty, To: "a1", Kind: state.KindTell, Body: "new instruction"})
	require.NoError(t, err)

	var got []api.Message
	require.NoError(t, agent.Do(ctx, http.MethodGet, api.PathInbox, nil, &got))
	require.Len(t, got, 1)
	assert.Equal(t, "new instruction", got[0].Body)

	// The user's inbox is every unread message, delivered or not.
	toUser, err := msgs.Enqueue(ctx, &state.NewMessage{From: "a1", To: state.UserParty, Kind: state.KindTell, Body: "fyi"})
	require.NoError(t, err)
	require.NoError(t, msgs.MarkDelivered(ctx, toUser.ID, state.DeliveryInbox))
	var mine []api.Message
	require.NoError(t, user.Do(ctx, http.MethodGet, api.PathInbox, nil, &mine))
	require.Len(t, mine, 1)
	assert.Equal(t, "fyi", mine[0].Body)
}
