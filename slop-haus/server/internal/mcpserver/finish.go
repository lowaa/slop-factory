package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func registerFinish(server *mcp.Server, h *Handler) {
	addTool(server, h, &mcp.Tool{
		Name: "ticket_finish",
		Description: "Say the work on your ticket is finished and let the server decide what happens next, Push your branch first. " +
			"The ticket goes to a human's review (it records the handoff and releases the ticket and the integration lock) when it requires approval and has none yet, " +
			"when your attempt is not the ticket's promoted one, or when you pass request_review with a reason (rare: a finished change " +
			"whose code you want a human to read, such as a risky, security-sensitive or migration change; a question about direction " +
			"is ticket_discuss). Where the tenant lands by the server's merge queue, the ticket otherwise goes into the landing queue and you no longer hold it: " +
			"slop-haus merges the base branch, waits for CI and merges the change itself, and hands the ticket back with the reason if it cannot. " +
			"Otherwise you keep the ticket and the reply gives the merge steps, which end at change_merge: that " +
			"merges the change and closes the ticket. Where the tenant has no change tools, pass pr_url, the change you merged: the " +
			"ticket is then closed with it. handoff names the branch, the PR and the pushed commit; it is required for a review. " +
			"pr_url is optional where change_open recorded the change. " +
			"A ticket that ends with nothing to merge (a split parent whose parts are all done or cancelled, an investigation that " +
			"concludes there is nothing to change) is closed as done with no_change true and a reason, which becomes the closing comment: " +
			"slop-haus refuses it while the attempt has a change or commits beyond the ticket's base commit, or while a ticket it waits on is open.",
	}, leaseHeld, h.finish)
}

type finishInput struct {
	Handoff       string `json:"handoff,omitempty" jsonschema:"where the work stands -- branch, PR, pushed commit -- for whoever picks the ticket up; required when the ticket goes to review"`
	RequestReview bool   `json:"request_review,omitempty" jsonschema:"ask for a human to read this finished change before it merges; needs a reason"`
	Reason        string `json:"reason,omitempty" jsonschema:"with request_review: why a human should read it (risky, security-sensitive, a migration you are unsure of); with no_change: why there is nothing to merge, recorded as the closing comment"`
	NoChange      bool   `json:"no_change,omitempty" jsonschema:"close the ticket as done with no change to merge; needs a reason, and slop-haus checks there really is none"`
	RedWasFlake   int64  `json:"red_was_flake,omitempty" jsonschema:"after the landing queue handed the ticket back with red checks: the id of the open or done 'CI flake:' ticket that covers the failure, which allows one more landing attempt of the same head; without it a head that already went red is refused until you push a new commit"`
	PRURL         string `json:"pr_url,omitempty" jsonschema:"the https URL of the change; needed only where change tools are off, or to hand over a change not opened with change_open"`
}

// finishNext is what ticket_finish decided.
type finishNext string

const (
	finishReview finishNext = "review"
	finishMerge  finishNext = "merge"
	finishClosed finishNext = "closed"
	// finishLanding is the ticket in the server's landing queue.
	finishLanding finishNext = "landing"
)

type finishOutput struct {
	toolNotes
	// Next is where the ticket went: review or landing (you no longer hold it), merge
	// (you still hold it and the reply lists the steps) or closed.
	Next    finishNext `json:"next"`
	Message string     `json:"message"`
}

func (h *Handler) finish(ctx context.Context, agentID uuid.UUID, in finishInput) (*mcp.CallToolResult, finishOutput, error) {
	gate, err := h.tickets.ChangeGateFor(ctx, agentID)
	if err != nil {
		return nil, finishOutput{}, h.leaseLost(ctx, agentID, err)
	}
	if in.NoChange {
		return h.finishNoChange(ctx, agentID, &in)
	}
	reviewRequired := !gate.Promoted || (gate.Ticket.RequiresApproval && gate.Ticket.ApprovedAt == nil)
	if reviewRequired || in.RequestReview {
		h.narrowToChange(ctx, agentID)
		return h.finishToReview(ctx, agentID, gate, &in)
	}
	if h.changes == nil {
		return h.finishClosed(ctx, agentID, gate, &in)
	}
	h.narrowToChange(ctx, agentID)
	if h.lander != nil {
		if next, done, err := h.finishToLanding(ctx, agentID, &in); done || err != nil {
			return next.result, next.out, err
		}
	}
	msg := "Your ticket stays in progress and you still hold it. To merge: call change_open with draft false " +
		"(the same branch) to take the change out of draft; merge the base branch into yours and push when it has moved; " +
		"wait for change_checks on the pushed commit (pass it as head_sha) to pass; take integration_lock; " +
		"then change_merge with head_sha. change_merge merges the change and closes the ticket, so there is nothing to call after it."
	h.progress(ctx, agentID)
	return text("%s", msg), finishOutput{Next: finishMerge, Message: msg}, nil
}

type finishResult struct {
	result *mcp.CallToolResult
	out    finishOutput
}

// finishToLanding enters the finished ticket in the server's landing queue,
// when the tenant lands by the queue. done is false, with nothing written,
// when it does not: the agent then merges as before.
func (h *Handler) finishToLanding(ctx context.Context, agentID uuid.UUID, in *finishInput) (finishResult, bool, error) {
	found, err := authenticatedAgent(ctx)
	if err != nil {
		return finishResult{}, false, err
	}
	landing, done, err := h.lander.Land(ctx, found.TenantID(), agentID, in.Handoff, in.RedWasFlake)
	if refused, ok := undiscussedRefusal(err); ok {
		return finishResult{refused, finishOutput{Message: err.Error()}}, true, nil
	}
	if err != nil {
		return finishResult{}, false, h.leaseLost(ctx, agentID, err)
	}
	if !done {
		return finishResult{}, false, nil
	}
	h.progress(ctx, agentID)
	msg := fmt.Sprintf("#%d is in the landing queue. You no longer hold it and need not take the integration lock: slop-haus merges the base branch into the change, "+
		"waits for CI and merges it, then closes the ticket. Take other work, or stop if there is none. "+
		"If it can't land (a conflict, red checks, a refusal) the ticket comes back to you with the reason; fix it, push and call ticket_finish again (for red checks, only with a new commit or red_was_flake set to a CI flake ticket's id).", landing.ID)
	return finishResult{text("%s", msg), finishOutput{Next: finishLanding, Message: msg}}, true, nil
}

func (h *Handler) finishToReview(ctx context.Context, agentID uuid.UUID, gate *ticket.ChangeGate, in *finishInput) (*mcp.CallToolResult, finishOutput, error) {
	prURL := in.PRURL
	if prURL == "" && gate.Review.PRURL != nil {
		prURL = *gate.Review.PRURL
	}
	reason := in.Reason
	if !in.RequestReview {
		// Only a voluntary review has a reason to give: the others are
		// decided by the server.
		reason = ""
	}
	reviewed, err := h.handOver(ctx, agentID, &ticket.ReviewParams{PRURL: prURL, Handoff: in.Handoff, Reason: reason})
	if refused, ok := undiscussedRefusal(err); ok {
		return refused, finishOutput{Message: err.Error()}, nil
	}
	if err != nil {
		return nil, finishOutput{}, h.leaseLost(ctx, agentID, err)
	}
	h.progress(ctx, agentID)
	msg := fmt.Sprintf("#%d is in review. You no longer hold it, nor the integration lock; take other work, or stop if there is none. "+
		"Don't merge the PR. When a human approves it or asks for changes it comes back to the queue with you first in line, "+
		"unless the server merges the approved change itself and closes the ticket as done, when there is nothing left to do.", reviewed.ID)
	return text("%s", msg), finishOutput{Next: finishReview, Message: msg}, nil
}

func (h *Handler) finishClosed(ctx context.Context, agentID uuid.UUID, gate *ticket.ChangeGate, in *finishInput) (*mcp.CallToolResult, finishOutput, error) {
	prURL := in.PRURL
	if strings.TrimSpace(prURL) == "" && gate.Review.PRURL != nil {
		prURL = *gate.Review.PRURL
	}
	if strings.TrimSpace(prURL) == "" {
		return nil, finishOutput{}, h.leaseLost(ctx, agentID, ticket.ErrPRURLRequired)
	}
	finished, err := h.tickets.Done(ctx, agentID, &ticket.DoneParams{Summary: in.Handoff, PRURL: prURL})
	if refused, ok := undiscussedRefusal(err); ok {
		return refused, finishOutput{Message: err.Error()}, nil
	}
	if err != nil {
		return nil, finishOutput{}, h.leaseLost(ctx, agentID, err)
	}
	h.progress(ctx, agentID)
	msg := fmt.Sprintf("Closed #%d.", finished.ID)
	return text("%s", msg), finishOutput{Next: finishClosed, Message: msg}, nil
}

func (h *Handler) finishNoChange(ctx context.Context, agentID uuid.UUID, in *finishInput) (*mcp.CallToolResult, finishOutput, error) {
	if in.RequestReview {
		return nil, finishOutput{}, errors.New("no_change closes the ticket, so it cannot be combined with request_review")
	}
	if h.changes == nil {
		return nil, finishOutput{}, errors.New("no_change is not available here: slop-haus cannot check the code host, so pass pr_url for the change you merged")
	}
	found, err := authenticatedAgent(ctx)
	if err != nil {
		return nil, finishOutput{}, err
	}
	closed, err := h.changes.FinishWithoutChange(ctx, found.TenantID(), agentID, in.Reason)
	if refused, ok := undiscussedRefusal(err); ok {
		return refused, finishOutput{Message: err.Error()}, nil
	}
	if err != nil {
		return nil, finishOutput{}, h.leaseLost(ctx, agentID, err)
	}
	h.progress(ctx, agentID)
	msg := fmt.Sprintf("Closed #%d as done with no change.", closed.ID)
	return text("%s", msg), finishOutput{Next: finishClosed, Message: msg}, nil
}
