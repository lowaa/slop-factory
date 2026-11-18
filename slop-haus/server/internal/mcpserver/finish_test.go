package mcpserver_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func finishGate(promoted, requiresApproval, approved bool, prURL string) *ticket.ChangeGate {
	now := time.Now()
	g := &ticket.ChangeGate{Ticket: &ticket.Ticket{ID: 1, RequiresApproval: requiresApproval}, Promoted: promoted}
	if approved {
		g.Ticket.ApprovedAt = &now
	}
	if prURL != "" {
		g.Review.PRURL = &prURL
	}
	return g
}

func TestTicketFinish_DecidesTheNextState(t *testing.T) {
	cases := map[string]struct {
		gate    *ticket.ChangeGate
		args    map[string]any
		changes bool
		next    string
		review  *ticket.ReviewParams
		done    *ticket.DoneParams
	}{
		"requires approval, not yet approved -> review": {
			gate: finishGate(true, true, false, testPR), args: map[string]any{"handoff": "h"}, changes: true, next: "review",
			review: &ticket.ReviewParams{PRURL: testPR, Handoff: "h"},
		},
		"requires approval, not approved: a reason is not passed on": {
			gate: finishGate(true, true, false, testPR), args: map[string]any{"handoff": "h", "reason": "x"}, changes: true, next: "review",
			review: &ticket.ReviewParams{PRURL: testPR, Handoff: "h"},
		},
		"unpromoted attempt -> review": {
			gate: finishGate(false, false, false, testPR), args: map[string]any{"handoff": "h"}, changes: true, next: "review",
			review: &ticket.ReviewParams{PRURL: testPR, Handoff: "h"},
		},
		"voluntary review -> review with the reason": {
			gate: finishGate(true, false, false, testPR), args: map[string]any{"handoff": "h", "request_review": true, "reason": "risky"}, changes: true, next: "review",
			review: &ticket.ReviewParams{PRURL: testPR, Handoff: "h", Reason: "risky"},
		},
		"an explicit pr_url wins over the recorded one": {
			gate: finishGate(true, true, false, "https://other.example/pr/1"), args: map[string]any{"handoff": "h", "pr_url": testPR}, changes: true, next: "review",
			review: &ticket.ReviewParams{PRURL: testPR, Handoff: "h"},
		},
		"ungated -> stays in progress to merge": {
			gate: finishGate(true, false, false, testPR), args: map[string]any{"handoff": "h"}, changes: true, next: "merge",
		},
		"approved -> stays in progress to merge": {
			gate: finishGate(true, true, true, testPR), args: map[string]any{"handoff": "h"}, changes: true, next: "merge",
		},
		"no change tools, ungated -> closed with the pr": {
			gate: finishGate(true, false, false, ""), args: map[string]any{"handoff": "h", "pr_url": testPR}, next: "closed",
			done: &ticket.DoneParams{Summary: "h", PRURL: testPR},
		},
		"no change tools, needs approval -> review": {
			gate: finishGate(true, true, false, ""), args: map[string]any{"handoff": "h", "pr_url": testPR}, next: "review",
			review: &ticket.ReviewParams{PRURL: testPR, Handoff: "h"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fc := &fakeChanges{review: &ticket.Ticket{ID: 1, Status: ticket.StatusReview}, merged: &change.Merged{}}
			var r *rig
			if tc.changes {
				r = newChangesRig(t, fc)
			} else {
				r = newRig(t)
			}
			r.tickets.lease = &ticket.Lease{TicketID: 1}
			r.tickets.gate = tc.gate

			res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "ticket_finish", Arguments: tc.args})
			require.NoError(t, err)
			require.False(t, res.IsError, resultText(t, res))
			sc, ok := res.StructuredContent.(map[string]any)
			require.True(t, ok)
			assert.Equal(t, tc.next, sc["next"])
			assert.Positive(t, r.sessions.progress)

			var reviewed *ticket.ReviewParams
			switch {
			case tc.changes && len(fc.reviews) > 0:
				reviewed = &fc.reviews[0]
			case !tc.changes:
				reviewed = r.tickets.reviewed
			}
			assert.Equal(t, tc.review, reviewed)
			assert.Equal(t, tc.done, r.tickets.done)
			assert.Empty(t, fc.merges, "finish never merges")
			if tc.next == "merge" {
				assert.Contains(t, resultText(t, res), "change_merge")
				assert.Nil(t, r.tickets.reviewed)
			}
		})
	}
}

func TestTicketFinish_AVoluntaryReviewWithoutAReasonIsAnAnswer(t *testing.T) {
	r := newRig(t)
	r.tickets.lease = &ticket.Lease{TicketID: 1}
	r.tickets.gate = finishGate(true, false, false, "")
	r.tickets.reviewErr = ticket.ErrReviewNeedsReason

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_finish", Arguments: map[string]any{"handoff": "h", "pr_url": testPR, "request_review": true},
	})
	require.NoError(t, err)
	require.NotNil(t, r.tickets.reviewed)
	assert.Empty(t, r.tickets.reviewed.Reason)
	assert.False(t, res.IsError, "an answer, not a failure to retry")
	assert.Contains(t, resultText(t, res), "merge it yourself")
	assert.Zero(t, r.sessions.progress)
}

func TestTicketFinish_AReviewThatFailsIsAnErrorAndNotProgress(t *testing.T) {
	r := newRig(t)
	r.tickets.lease = &ticket.Lease{TicketID: 1}
	r.tickets.gate = finishGate(true, true, false, testPR)
	r.tickets.reviewErr = ticket.ErrInvalidPRURL

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_finish", Arguments: map[string]any{"handoff": "h"},
	})
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Zero(t, r.sessions.progress)
}

func TestTicketFinish_WithoutChangeToolsTheRecordedPRClosesWhenNoneIsPassed(t *testing.T) {
	r := newRig(t)
	r.tickets.lease = &ticket.Lease{TicketID: 1}
	r.tickets.gate = finishGate(true, false, false, testPR)

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_finish", Arguments: map[string]any{"handoff": "h"},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, resultText(t, res))
	assert.Equal(t, &ticket.DoneParams{Summary: "h", PRURL: testPR}, r.tickets.done)
}

func TestTicketFinish_WithoutChangeToolsAMergedFinishNeedsThePR(t *testing.T) {
	r := newRig(t)
	r.tickets.lease = &ticket.Lease{TicketID: 1}

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_finish", Arguments: map[string]any{"handoff": "h"},
	})
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "URL of the PR")
	assert.Nil(t, r.tickets.done)
	assert.Zero(t, r.sessions.progress)
}

func TestTicketFinish_AFailedDecisionOrCloseIsAnErrorAndNotProgress(t *testing.T) {
	cases := map[string]func(*fakeTickets){
		"the gate cannot be read": func(f *fakeTickets) { f.gateErr = ticket.ErrNoLease },
		"the close fails":         func(f *fakeTickets) { f.doneErr = ticket.ErrInvalidPRURL },
	}
	for name, break_ := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.tickets.lease = &ticket.Lease{TicketID: 1}
			break_(r.tickets)

			res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "ticket_finish", Arguments: map[string]any{"handoff": "h", "pr_url": testPR},
			})
			require.NoError(t, err)
			assert.True(t, res.IsError)
			assert.Zero(t, r.sessions.progress)
		})
	}
}

func TestTicketFinish_NoChangeClosesThroughTheChangeService(t *testing.T) {
	fc := &fakeChanges{review: &ticket.Ticket{ID: 7}}
	r := newChangesRig(t, fc)
	r.tickets.lease = &ticket.Lease{TicketID: 7}
	r.tickets.gate = finishGate(true, false, false, "")

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_finish", Arguments: map[string]any{"no_change": true, "reason": "all parts landed"},
	})

	require.NoError(t, err)
	assert.False(t, res.IsError)
	assert.Equal(t, []string{"all parts landed"}, fc.noChange)
	assert.Contains(t, resultText(t, res), "Closed #7")
	assert.Nil(t, r.tickets.reviewed)
}

func TestTicketFinish_NoChangeRefusalIsAnErrorAndNotProgress(t *testing.T) {
	fc := &fakeChanges{failure: errors.New("change: this attempt has a change")}
	r := newChangesRig(t, fc)
	r.tickets.lease = &ticket.Lease{TicketID: 7}
	r.tickets.gate = finishGate(true, false, false, "")

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_finish", Arguments: map[string]any{"no_change": true, "reason": "r"},
	})

	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "this attempt has a change")
	assert.Zero(t, r.sessions.progress)
}

func TestTicketFinish_NoChangeNeedsChangeTools(t *testing.T) {
	r := newRig(t)
	r.tickets.lease = &ticket.Lease{TicketID: 7}
	r.tickets.gate = finishGate(true, false, false, "")

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_finish", Arguments: map[string]any{"no_change": true, "reason": "r"},
	})

	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Nil(t, r.tickets.done)
}

func TestTicketFinish_NoChangeCannotBeCombinedWithAReviewRequest(t *testing.T) {
	fc := &fakeChanges{review: &ticket.Ticket{ID: 7}}
	r := newChangesRig(t, fc)
	r.tickets.lease = &ticket.Lease{TicketID: 7}
	r.tickets.gate = finishGate(true, false, false, "")

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_finish", Arguments: map[string]any{"no_change": true, "request_review": true, "reason": "r"},
	})

	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Empty(t, fc.noChange)
	assert.Zero(t, r.sessions.progress)
}

type fakeLander struct {
	landed  bool
	err     error
	handoff string
	calls   int
}

func (f *fakeLander) Land(_ context.Context, _, _ uuid.UUID, handoff string, _ int64) (*ticket.Ticket, bool, error) {
	f.calls++
	f.handoff = handoff
	return &ticket.Ticket{ID: 7}, f.landed, f.err
}

func finishWithLander(t *testing.T, l *fakeLander, gate *ticket.ChangeGate) (*rig, *mcp.CallToolResult) {
	t.Helper()
	r := newRigWith(t, &rigOptions{changes: &fakeChanges{merged: &change.Merged{}, review: &ticket.Ticket{ID: 1, Status: ticket.StatusReview}}, lander: l})
	r.tickets.lease = &ticket.Lease{TicketID: 1}
	r.tickets.gate = gate
	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "ticket_finish", Arguments: map[string]any{"handoff": "h"}})
	require.NoError(t, err)
	return r, res
}

func TestTicketFinish_ATenantOnTheMergeQueueLandsByTheServer(t *testing.T) {
	l := &fakeLander{landed: true}
	r, res := finishWithLander(t, l, finishGate(true, false, false, testPR))
	require.False(t, res.IsError, resultText(t, res))
	assert.Equal(t, "landing", res.StructuredContent.(map[string]any)["next"])
	assert.Equal(t, "h", l.handoff)
	assert.Positive(t, r.sessions.progress)
	assert.Contains(t, resultText(t, res), "landing queue")
}

func TestTicketFinish_ATenantNotOnTheQueueKeepsTheMergeSteps(t *testing.T) {
	l := &fakeLander{landed: false}
	_, res := finishWithLander(t, l, finishGate(true, false, false, testPR))
	assert.Equal(t, "merge", res.StructuredContent.(map[string]any)["next"])
	assert.Equal(t, 1, l.calls)
}

func TestTicketFinish_AReviewIsNotLanded(t *testing.T) {
	l := &fakeLander{landed: true}
	_, res := finishWithLander(t, l, finishGate(true, true, false, testPR))
	assert.Equal(t, "review", res.StructuredContent.(map[string]any)["next"])
	assert.Zero(t, l.calls, "an unapproved gated ticket goes to a human first")
}

func TestTicketFinish_AFailedEnqueueIsAnErrorAndNotProgress(t *testing.T) {
	l := &fakeLander{err: errors.New("db down")}
	r, res := finishWithLander(t, l, finishGate(true, false, false, testPR))
	assert.True(t, res.IsError)
	assert.Zero(t, r.sessions.progress)
}

func TestTicketFinish_ARedRequeueRefusalIsTheAnswerNotAnError(t *testing.T) {
	l := &fakeLander{err: fmt.Errorf("%w: use red_was_flake", landing.ErrRedRequeue)}
	r, res := finishWithLander(t, l, finishGate(true, false, false, testPR))
	assert.False(t, res.IsError)
	assert.Contains(t, resultText(t, res), "red_was_flake")
	assert.Zero(t, r.sessions.progress)
}
