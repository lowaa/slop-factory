package mcpserver_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

const testPR = "https://github.example/acme/repo/pull/12"

// Which part of the review flow the agent is in is said at the claim, where
// it cannot be missed.
func TestTicketNext_SaysWhereTheReviewStands(t *testing.T) {
	now := time.Now()
	pr := testPR
	cases := map[string]struct {
		ticket    ticket.Ticket
		says      []string
		neverSays []string
	}{
		"requires approval": {
			ticket:    ticket.Ticket{RequiresApproval: true},
			says:      []string{"REQUIRES HUMAN APPROVAL", "ticket_finish", "Don't merge"},
			neverSays: []string{"APPROVED:", "CHANGES REQUESTED"},
		},
		"approved": {
			ticket:    ticket.Ticket{RequiresApproval: true, PRURL: &pr, ApprovedAt: &now},
			says:      []string{"APPROVED", testPR, "integration_lock", "ticket_finish", "already merged", "ticket_finish again"},
			neverSays: []string{"REQUIRES HUMAN APPROVAL", "CHANGES REQUESTED"},
		},
		"changes requested": {
			ticket:    ticket.Ticket{RequiresApproval: true, PRURL: &pr, ChangesRequestedAt: &now},
			says:      []string{"CHANGES REQUESTED", testPR, "ticket_finish again", "Don't merge"},
			neverSays: []string{"APPROVED:"},
		},
		"ungated": {
			ticket:    ticket.Ticket{},
			neverSays: []string{"APPROVAL", "APPROVED", "CHANGES REQUESTED", "ticket_finish"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			granted := tc.ticket
			granted.ID, granted.Title, granted.Status = 7, "Gated", ticket.StatusInProgress
			r.tickets.claim = &ticket.ClaimResult{Ticket: &granted}

			res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "ticket_next"})
			require.NoError(t, err)
			body := resultText(t, res)
			for _, want := range tc.says {
				assert.Contains(t, body, want)
			}
			for _, unwanted := range tc.neverSays {
				assert.NotContains(t, body, unwanted)
			}
		})
	}
}

// ticket_get says it too: a resuming agent, or the dispatcher checking on a
// ticket, reads it there.
func TestTicketGet_SaysWhereTheReviewStands(t *testing.T) {
	now := time.Now()
	pr := testPR
	for name, tc := range map[string]struct {
		ticket ticket.Ticket
		says   string
	}{
		"in review": {ticket.Ticket{Status: ticket.StatusReview, RequiresApproval: true, PRURL: &pr}, "waiting on a human's approval"},
		"approved":  {ticket.Ticket{Status: ticket.StatusOpen, RequiresApproval: true, PRURL: &pr, ApprovedAt: &now}, "APPROVED"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			got := tc.ticket
			got.ID, got.Title = 7, "Gated"
			r.tickets.got = &got

			res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "ticket_get", Arguments: map[string]any{"ticket_id": 7},
			})
			require.NoError(t, err)
			assert.Contains(t, resultText(t, res), tc.says)
		})
	}
}

// A finished ticket keeps its approval as a record; it must not read as an
// instruction to merge it again.
func TestTicketGet_ADoneTicketSaysNothingAboutMerging(t *testing.T) {
	now := time.Now()
	pr := testPR
	r := newRig(t)
	r.tickets.got = &ticket.Ticket{
		ID: 7, Title: "Gated", Status: ticket.StatusDone, RequiresApproval: true, PRURL: &pr, ApprovedAt: &now,
	}

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_get", Arguments: map[string]any{"ticket_id": 7},
	})
	require.NoError(t, err)
	body := resultText(t, res)
	assert.NotContains(t, body, "APPROVED")
	assert.NotContains(t, body, "REQUIRES HUMAN APPROVAL")
}

// A ticket that still needs discussion can't be closed or put into review,
// and the refusal comes back as an answer naming the way out -- not as a
// tool error the agent would retry.
func TestTicketFinish_AnUndiscussedTicketIsToldToDiscuss(t *testing.T) {
	refusal := fmt.Errorf("%w (ticket #1)", ticket.ErrNeedsDiscussion)
	for name, tc := range map[string]struct {
		refuse func(*rig)
		call   *mcp.CallToolParams
	}{
		"done": {func(r *rig) { r.tickets.doneErr = refusal }, &mcp.CallToolParams{
			Name: "ticket_finish", Arguments: map[string]any{"handoff": "merged", "pr_url": testPR},
		}},
		"review": {func(r *rig) { r.tickets.reviewErr = refusal }, &mcp.CallToolParams{
			Name: "ticket_finish", Arguments: map[string]any{"pr_url": testPR, "handoff": "h", "request_review": true, "reason": "a person should read it"},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.tickets.lease = &ticket.Lease{TicketID: 1}
			tc.refuse(r)

			res, err := r.session.CallTool(context.Background(), tc.call)
			require.NoError(t, err)
			assert.False(t, res.IsError, "an answer, not a failure to retry")
			assert.Equal(t, refusal.Error(), resultText(t, res), "the refusal's own instruction, verbatim")
			assert.Zero(t, r.sessions.progress, "a refusal writes nothing")
		})
	}
}
