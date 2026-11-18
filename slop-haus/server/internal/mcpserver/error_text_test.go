package mcpserver_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Error values keep their package prefix for the logs; the agent reads
// the text without it.
func TestToolErrors_DropThePackagePrefix(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want string
		not  string
	}{
		"ticket":          {err: ticket.ErrAlreadyHolding, want: "agent already holds a ticket", not: "ticket:"},
		"change":          {err: change.ErrLockNotHeld, want: "you do not hold the integration lock", not: "change:"},
		"nested codehost": {err: change.ErrHeadMoved, want: "(the change's head has moved)", not: "codehost:"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.tickets.lease = &ticket.Lease{TicketID: 12}
			r.tickets.doneErr = tc.err

			body := doneFailure(t, r)
			assert.Contains(t, body, tc.want)
			assert.NotContains(t, body, tc.not)
		})
	}
}

func TestToolErrors_HeadNotReviewedEndsInASentence(t *testing.T) {
	r := newRig(t)
	r.tickets.lease = &ticket.Lease{TicketID: 12}
	r.tickets.doneErr = fmt.Errorf("%w: it is bbbb, the reviewed one is aaaa, and no diff was recorded to compare it with", change.ErrHeadNotReviewed)

	body := doneFailure(t, r)
	assert.Contains(t, body, "does not cover it: it is bbbb, the reviewed one is aaaa, and no diff was recorded to compare it with. Please hand it over again with ticket_finish.")
	assert.NotContains(t, body, "change:")
}

func doneFailure(t *testing.T, r *rig) string {
	t.Helper()
	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_finish", Arguments: map[string]any{"pr_url": testPR, "handoff": "finished"},
	})
	require.NoError(t, err)
	require.True(t, res.IsError)
	return resultText(t, res)
}

// A directed claim that cannot be granted says why, and another tenant's
// ticket reads as one that does not exist.
func TestTicketNext_ADirectedClaimThatCannotBeGrantedSaysWhy(t *testing.T) {
	r := newRealRig(t)
	preparing, err := r.tickets.Create(r.ctx, &ticket.CreateParams{Title: "Not yet", Status: ticket.StatusPreparing})
	require.NoError(t, err)
	cancelled := r.openTicket("Gone")
	status := ticket.StatusCancelled
	_, err = r.tickets.Update(r.ctx, cancelled.ID, &ticket.UpdateParams{Status: &status})
	require.NoError(t, err)

	parked := r.openTicket("Shelved")
	status = ticket.StatusParked
	_, err = r.tickets.Update(r.ctx, parked.ID, &ticket.UpdateParams{Status: &status})
	require.NoError(t, err)
	blocker := r.openTicket("Blocker")
	blocked := r.openTicket("Waiting")
	_, err = r.tickets.AddDependency(r.ctx, ticket.UserAuthor(r.human), blocked.ID, blocker.ID)
	require.NoError(t, err)

	for name, tc := range map[string]struct {
		id   int64
		want string
	}{
		"parked":      {parked.ID, fmt.Sprintf("Ticket #%d was not granted: it is parked", parked.ID)},
		"blocked":     {blocked.ID, fmt.Sprintf("Ticket #%d was not granted: it is blocked on", blocked.ID)},
		"preparing":   {preparing.ID, fmt.Sprintf("Ticket #%d was not granted: it is still Preparing", preparing.ID)},
		"cancelled":   {cancelled.ID, fmt.Sprintf("Ticket #%d was not granted: it is already cancelled", cancelled.ID)},
		"nonexistent": {999999, "Ticket #999999 was not granted: no such ticket."},
	} {
		t.Run(name, func(t *testing.T) {
			res := r.call("ticket_next", map[string]any{"ticket_id": tc.id})
			require.False(t, res.IsError, resultText(t, res))
			assert.Contains(t, resultText(t, res), tc.want)
			assert.NotContains(t, resultText(t, res), "queue is empty")
		})
	}
}
