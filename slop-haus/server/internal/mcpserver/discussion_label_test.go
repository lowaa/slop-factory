package mcpserver_test

import (
	"errors"
	"strings"

	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// openDiscussions is the fake of the one read ticket_get makes: it returns
// what the test gives it.
type openDiscussions map[int]*ticket.AttemptDiscussionBody

func (o openDiscussions) AttemptDiscussions(context.Context, int64) (map[int]*ticket.AttemptDiscussionBody, error) {
	return o, nil
}

func routineLabel() openDiscussions {
	label := "routine"
	return openDiscussions{1: {Round: 2, Label: &label, Category: "rebase_retry", Reason: "templated"}}
}

func TestTicketGet_DiscussionLabelGoesToTheOwnersSideOnly(t *testing.T) {
	r := newRealRigWith(t, dbtest.WithDB(t), &realRigOptions{discussions: routineLabel()})
	tk := r.forceStatus(ticket.StatusDiscussion)
	args := map[string]any{"ticket_id": tk.ID}

	conductor := r.conductorSession("conductor")
	got := resultText(t, conductor.call(t, "ticket_get", args))
	assert.Contains(t, got, "labelled routine by the steward, category rebase_retry")
	assert.Contains(t, got, "Answer with steward")

	console := r.agentSession("console", true)
	assert.Contains(t, resultText(t, console.call(t, "ticket_get", args)), "labelled routine")

	worker := resultText(t, r.call("ticket_get", args))
	assert.NotContains(t, worker, "routine")
	assert.NotContains(t, worker, "Answer with steward")
}

func TestTicketGet_DiscussionLabelShownToWorkersWhenFlagged(t *testing.T) {
	r := newRealRigWith(t, dbtest.WithDB(t), &realRigOptions{discussions: routineLabel(), labelToWorkers: true})
	tk := r.forceStatus(ticket.StatusDiscussion)

	assert.Contains(t, resultText(t, r.call("ticket_get", map[string]any{"ticket_id": tk.ID})), "labelled routine")
}

func structuredDiscussions(t *testing.T, res *mcp.CallToolResult) []any {
	t.Helper()
	out, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok, "structured output: %#v", res.StructuredContent)
	got, _ := out["discussions"].([]any)
	return got
}

func TestTicketGet_DiscussionLabelStructuredAndOrdered(t *testing.T) {
	routine, yours, notAssessed := "routine", "yours", "not_assessed"
	r := newRealRigWith(t, dbtest.WithDB(t), &realRigOptions{discussions: openDiscussions{
		3: {Round: 1, Label: &yours, Reason: "needs judgment"},
		1: {Round: 2, Label: &routine, Category: "rebase_retry"},
		2: {Round: 1, Label: &notAssessed, Reason: "daily steward cap reached"},
		4: {Round: 1},
	}})
	tk := r.forceStatus(ticket.StatusDiscussion)
	args := map[string]any{"ticket_id": tk.ID}

	res := r.conductorSession("conductor").call(t, "ticket_get", args)

	got := structuredDiscussions(t, res)
	require.Len(t, got, 4)
	var attempts []any
	for _, d := range got {
		attempts = append(attempts, d.(map[string]any)["attempt"])
	}
	assert.Equal(t, []any{1.0, 2.0, 3.0, 4.0}, attempts)
	assert.Equal(t, map[string]any{"attempt": 1.0, "round": 2.0, "label": "routine", "category": "rebase_retry"}, got[0])
	text := resultText(t, res)
	assert.Contains(t, text, "attempt 3, round 1): labelled yours by the steward (needs judgment).")
	assert.Contains(t, text, "labelled not_assessed by the steward (daily steward cap reached).")
	assert.Contains(t, text, "attempt 4, round 1): not labelled yet.")
	assert.Equal(t, 1, strings.Count(text, "Answer with steward"), "only the routine round points at the draft")

	assert.Empty(t, structuredDiscussions(t, r.call("ticket_get", args)), "a worker's structured result has no label")
}

func TestTicketGet_DiscussionLabelStructuredHiddenFromWorkersUnlessFlagged(t *testing.T) {
	r := newRealRigWith(t, dbtest.WithDB(t), &realRigOptions{discussions: routineLabel(), labelToWorkers: true})
	tk := r.forceStatus(ticket.StatusDiscussion)

	assert.Len(t, structuredDiscussions(t, r.call("ticket_get", map[string]any{"ticket_id": tk.ID})), 1)
}

type failingDiscussions struct{}

func (failingDiscussions) AttemptDiscussions(context.Context, int64) (map[int]*ticket.AttemptDiscussionBody, error) {
	return nil, errors.New("labels are down")
}

func TestTicketGet_FailedLabelReadStillReturnsTheTicket(t *testing.T) {
	r := newRealRigWith(t, dbtest.WithDB(t), &realRigOptions{discussions: failingDiscussions{}})
	tk := r.forceStatus(ticket.StatusDiscussion)

	res := r.conductorSession("conductor").call(t, "ticket_get", map[string]any{"ticket_id": tk.ID})

	require.False(t, res.IsError, resultText(t, res))
	assert.Empty(t, structuredDiscussions(t, res))
}
