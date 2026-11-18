package mcpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestTicketPrepare_PassesTheReasonAndCountsAsProgress(t *testing.T) {
	r := newRig(t)
	r.tickets.lease = &ticket.Lease{TicketID: 1}

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_prepare", Arguments: map[string]any{"reason": "The spec asks for two things at once."},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, resultText(t, res))
	assert.Equal(t, "The spec asks for two things at once.", r.tickets.prepared)
	assert.NotEqual(t, uuid.Nil, r.tickets.preparedBy, "it acts as the authenticated agent, not as nobody")
	assert.Positive(t, r.sessions.progress)
	assert.Contains(t, resultText(t, res), "no longer hold",
		"the agent must be told the ticket is gone, or it keeps working on it")
}

// The tool takes a reason and nothing else. With no ticket id to pass, an
// agent has no way to name somebody else's ticket.
func TestTicketPrepare_CannotNameATicket(t *testing.T) {
	r := newRig(t)

	tools, err := r.session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	for _, tool := range tools.Tools {
		if tool.Name != "ticket_prepare" {
			continue
		}
		schema, ok := tool.InputSchema.(map[string]any)
		require.True(t, ok)
		props, ok := schema["properties"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, []string{"reason"}, keys(props))
		return
	}
	t.Fatal("ticket_prepare is not registered")
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The default is the service's -- preparing, for an agent -- so the tool
// passes an omitted status through as omitted rather than choosing one.
func TestTicketCreate_PassesTheStatusThrough(t *testing.T) {
	r := newRig(t)

	_, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_create", Arguments: map[string]any{"title": "Follow-up"},
	})
	require.NoError(t, err)
	require.NotNil(t, r.tickets.created)
	assert.Equal(t, ticket.Status(""), r.tickets.created.Status)
	require.NotNil(t, r.tickets.created.CreatedByAgentID, "filed as the agent, which is what makes the default preparing")

	_, err = r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_create", Arguments: map[string]any{"title": "Asked to file it open", "status": "open"},
	})
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusOpen, r.tickets.created.Status)
}

func TestTicketCreate_DescriptionSaysItStartsInPreparing(t *testing.T) {
	r := newRig(t)

	tools, err := r.session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	for _, tool := range tools.Tools {
		if tool.Name == "ticket_create" {
			assert.Contains(t, tool.Description, "starts in preparing")
			assert.Contains(t, tool.Description, "specifically asked to file it as open")
			return
		}
	}
	t.Fatal("ticket_create is not registered")
}

// A call on the ticket a human has taken away is refused with the reason,
// and does nothing else.
func TestARevokedLeaseFailsTheNextCallOnThatTicket(t *testing.T) {
	r := newRig(t)
	r.tickets.heartbeatErr = &ticket.LeaseRevokedError{TicketID: 12, Status: ticket.StatusPreparing}

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_comment", Arguments: map[string]any{"ticket_id": 12, "body": "Nearly there."},
	})
	require.NoError(t, err, "a tool error, not a protocol failure")
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "#12 was moved to Preparing by a human -- stop working on it")
	assert.Zero(t, r.tickets.commented, "the failed call must not have acted")
}

// A lease that simply isn't there is how every idle agent calls a tool;
// only a revocation fails the call.
func TestNoLeaseDoesNotFailAToolCall(t *testing.T) {
	r := newRig(t)
	r.tickets.heartbeatErr = ticket.ErrNoLease

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "checkin",
	})
	require.NoError(t, err)
	assert.False(t, res.IsError, resultText(t, res))
}

func TestTicketPrepare_AFailureIsReportedAndIsNotProgress(t *testing.T) {
	r := newRig(t)
	r.tickets.prepareErr = ticket.ErrEmptyReason

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_prepare", Arguments: map[string]any{"reason": " "},
	})
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "needs a reason")
	assert.NotContains(t, resultText(t, res), "back in preparing")
	assert.Zero(t, r.sessions.progress)
}

// A human can revoke between caller's heartbeat and the tool's own lease
// check. The tool then fails for want of a lease, and the notice rides on
// that failure rather than waiting for the agent's next call.
func TestALeaseRevokedMidCallIsReportedByThatCall(t *testing.T) {
	r := newRig(t)
	r.tickets.lease = &ticket.Lease{TicketID: 12}
	r.tickets.heartbeatErrs = []error{nil, &ticket.LeaseRevokedError{TicketID: 12, Status: ticket.StatusCancelled}}
	r.tickets.doneErr = ticket.ErrNoLease

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_finish", Arguments: map[string]any{"pr_url": testPR, "handoff": "finished"},
	})
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "#12 was cancelled by a human")
}

func TestAPlainLostLeaseIsReportedAsItself(t *testing.T) {
	r := newRig(t)
	r.tickets.lease = &ticket.Lease{TicketID: 12}
	r.tickets.heartbeatErrs = []error{nil, ticket.ErrNoLease}
	r.tickets.doneErr = ticket.ErrNoLease

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_finish", Arguments: map[string]any{"pr_url": testPR, "handoff": "finished"},
	})
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "no active lease")
}

func TestTicketCreate_ListsTheLiveTicketsItsScopesOverlap(t *testing.T) {
	r := newRig(t)
	r.tickets.overlaps = []ticket.ScopeOverlap{
		{TicketID: 7, Title: "Panel work", Status: ticket.StatusInProgress, Path: "app/src/", Other: "app/src/panel/"},
	}

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_create", Arguments: map[string]any{"title": "Broad", "paths": []string{"app/src/"}},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, resultText(t, res))
	msg := resultText(t, res)
	assert.Contains(t, msg, `#7 "Panel work" (in_progress): app/src/ overlaps app/src/panel/`)
	assert.Contains(t, msg, "narrower paths")
	structured, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	assert.Contains(t, string(structured), `"scope_overlaps":[{"id":7,`)
}

// Feedback must never turn a filed ticket into an error: the ticket exists
// whether or not the overlaps could be worked out.
func TestTicketCreate_NoOverlapsOrAFailedLookupStillFiles(t *testing.T) {
	r := newRig(t)

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_create", Arguments: map[string]any{"title": "Narrow"},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	assert.NotContains(t, resultText(t, res), "overlap")

	r.tickets.overlaps = []ticket.ScopeOverlap{{TicketID: 7}}
	r.tickets.overlapsErr = errors.New("db down")
	res, err = r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_create", Arguments: map[string]any{"title": "Still filed"},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	assert.Contains(t, resultText(t, res), "Filed #99")
	assert.NotContains(t, resultText(t, res), "overlap")
}

func TestTicketCreate_PathsDescriptionSaysToFileNarrowScopes(t *testing.T) {
	r := newRig(t)
	tools, err := r.session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	for _, tool := range tools.Tools {
		if tool.Name != "ticket_create" {
			continue
		}
		schema, ok := tool.InputSchema.(map[string]any)
		require.True(t, ok)
		props, ok := schema["properties"].(map[string]any)
		require.True(t, ok)
		paths, ok := props["paths"].(map[string]any)
		require.True(t, ok)
		desc, _ := paths["description"].(string)
		assert.Contains(t, desc, "narrowest")
		assert.Contains(t, desc, "ticket_scope_extend")
		assert.Contains(t, desc, "stops every overlapping ticket")
		return
	}
	t.Fatal("ticket_create is not registered")
}
