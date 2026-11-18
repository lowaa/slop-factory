package mcpserver_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// withSiblingAttempt gives the ticket a second, queued attempt.
func (r *realRig) withSiblingAttempt(id int64) {
	r.t.Helper()
	_, err := r.database.Exec(r.ctx, `INSERT INTO attempts (ticket_id, number, status) VALUES ($1, 2, 'queued')`, id)
	require.NoError(r.t, err)
}

// withSiblingInStatus is a second attempt beside the first, both in status:
// for a proposal about an attempt, which needs that attempt waiting on a person.
func (r *realRig) withSiblingInStatus(id int64, status ticket.Status) {
	r.t.Helper()
	_, err := r.database.Exec(r.ctx, `UPDATE attempts SET status = $2 WHERE ticket_id = $1`, id, string(status))
	require.NoError(r.t, err)
	_, err = r.database.Exec(r.ctx, `INSERT INTO attempts (ticket_id, number, status) VALUES ($1, 2, $2)`, id, string(status))
	require.NoError(r.t, err)
}

func (r *realRig) commentAttempts(ticketID int64, body string) []int {
	r.t.Helper()
	rows, err := r.database.Query(r.ctx,
		`SELECT COALESCE(attempt_number, 0) FROM ticket_comments WHERE ticket_id = $1 AND body LIKE $2 || '%' ORDER BY id`, ticketID, body)
	require.NoError(r.t, err)
	defer rows.Close()
	var out []int
	for rows.Next() {
		var n int
		require.NoError(r.t, rows.Scan(&n))
		out = append(out, n)
	}
	require.NoError(r.t, rows.Err())
	return out
}

func TestTicketComment_AConductorAddressesAnAttempt(t *testing.T) {
	r := newRealRig(t)
	r.ownerBound()
	c := r.conductorSession("conductor")
	single := r.openTicket("Single attempt")
	two := r.openTicket("Two attempts")
	r.withSiblingAttempt(two.ID)

	res := c.call(t, "ticket_comment", map[string]any{"ticket_id": single.ID, "body": "on the only attempt"})
	require.False(t, res.IsError, resultText(t, res))
	assert.Equal(t, []int{0}, r.commentAttempts(single.ID, "on the only attempt"), "with one attempt it goes where it always did")

	res = c.call(t, "ticket_comment", map[string]any{"ticket_id": two.ID, "body": "unnamed on two"})
	require.False(t, res.IsError, resultText(t, res))
	assert.Equal(t, []int{0}, r.commentAttempts(two.ID, "unnamed on two"), "with several it is the ticket's, for every attempt")

	res = c.call(t, "ticket_comment", map[string]any{"ticket_id": two.ID, "attempt": 2, "body": "named on two"})
	require.False(t, res.IsError, resultText(t, res))
	assert.Equal(t, []int{2}, r.commentAttempts(two.ID, "named on two"))

	res = c.call(t, "ticket_comment", map[string]any{"ticket_id": two.ID, "attempt": 9, "body": "no such attempt"})
	require.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "no such attempt")
}

func TestTicketComment_AWorkerPassingAnAttemptIsRefused(t *testing.T) {
	r := newRealRig(t)
	r.ownerBound()
	held := r.openTicket("Held")
	r.withSiblingAttempt(held.ID)
	require.False(t, r.call("ticket_next", map[string]any{"ticket_id": held.ID}).IsError)

	for _, attempt := range []int{1, 2} {
		res := r.call("ticket_comment", map[string]any{"ticket_id": held.ID, "attempt": attempt, "body": "worker names one"})
		require.True(t, res.IsError, "attempt %d", attempt)
		assert.Contains(t, resultText(t, res), "leave out attempt")
	}
	assert.Empty(t, r.commentAttempts(held.ID, "worker names one"))

	res := r.call("ticket_comment", map[string]any{"ticket_id": held.ID, "body": "worker names none"})
	require.False(t, res.IsError, resultText(t, res))
	assert.Equal(t, []int{1}, r.commentAttempts(held.ID, "worker names none"), "its own attempt, by its lease")
}

func TestProposals_TakeTheAttempt(t *testing.T) {
	r := newRealRig(t)
	c := r.agentSession("console", true)
	single := r.forceStatus(ticket.StatusDiscussion)
	two := r.forceStatus(ticket.StatusDiscussion)
	r.withSiblingInStatus(two.ID, ticket.StatusDiscussion)

	// The console's newest proposal's attempt.
	latest := func() int {
		t.Helper()
		got, err := r.pending.List(r.ctx, &pendingaction.ListFilter{AgentID: &c.agentID, Limit: 1})
		require.NoError(t, err)
		require.NotEmpty(t, got)
		require.NotNil(t, got[0].Attempt)
		return *got[0].Attempt
	}

	res := c.call(t, "propose_discussion_answer", map[string]any{"ticket_id": single.ID, "body": "go"})
	require.False(t, res.IsError, resultText(t, res))
	assert.Equal(t, 1, latest(), "with one live attempt it defaults to it")

	res = c.call(t, "propose_discussion_answer", map[string]any{"ticket_id": two.ID, "body": "go"})
	require.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "attempt 2")

	res = c.call(t, "propose_discussion_answer", map[string]any{"ticket_id": two.ID, "attempt": 2, "body": "go"})
	require.False(t, res.IsError, resultText(t, res))
	assert.Equal(t, 2, latest())

	review := r.forceStatus(ticket.StatusReview)
	r.withSiblingInStatus(review.ID, ticket.StatusReview)
	res = c.call(t, "propose_review_decision", map[string]any{"ticket_id": review.ID, "decision": "approve"})
	require.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "attempt 1")
	res = c.call(t, "propose_review_decision", map[string]any{"ticket_id": review.ID, "attempt": 1, "decision": "approve"})
	require.False(t, res.IsError, resultText(t, res))
}

func TestWorkingOn_AcceptsAndValidatesTheAttempt(t *testing.T) {
	r := newRealRig(t)
	c := r.agentSession("console", true)
	single := r.forceStatus(ticket.StatusOpen)
	two := r.forceStatus(ticket.StatusOpen)
	r.withSiblingAttempt(two.ID)

	res := c.call(t, "working_on", map[string]any{"ticket_id": single.ID})
	require.False(t, res.IsError, resultText(t, res))

	res = c.call(t, "working_on", map[string]any{"ticket_id": two.ID})
	require.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "attempt 1")

	res = c.call(t, "working_on", map[string]any{"ticket_id": two.ID, "attempt": 2})
	require.False(t, res.IsError, resultText(t, res))
	_, declared := r.currentDeclaration(c.agentID)
	require.NotNil(t, declared)
	assert.Equal(t, two.ID, *declared)

	res = c.call(t, "working_on", map[string]any{"ticket_id": two.ID, "attempt": 9})
	assert.True(t, res.IsError)
	res = c.call(t, "working_on", map[string]any{"ticket_id": nil, "attempt": 1})
	assert.True(t, res.IsError, "an attempt names nothing without its ticket")
}

func TestTicketNext_NamesTheBranchPerAttemptAndTicketGetShowsItToTheHolder(t *testing.T) {
	r := newRealRig(t)
	single := r.openTicket("Split the register")

	res := r.call("ticket_next", map[string]any{"ticket_id": single.ID})
	require.False(t, res.IsError, resultText(t, res))
	assert.Contains(t, resultText(t, res), "Branch: ticket-"+itoa(single.ID)+"-a1 ")

	two := r.openTicket("Race the build")
	r.withSiblingAttempt(two.ID)
	one := r.agentSession("first", false)
	first := one.call(t, "ticket_next", map[string]any{"ticket_id": two.ID})
	require.False(t, first.IsError, resultText(t, first))
	assert.Contains(t, resultText(t, first), "Branch: ticket-"+itoa(two.ID)+"-a1 ")
	other := r.agentSession("second", false)
	second := other.call(t, "ticket_next", map[string]any{"ticket_id": two.ID})
	require.False(t, second.IsError, resultText(t, second))
	assert.Contains(t, resultText(t, second), "Branch: ticket-"+itoa(two.ID)+"-a2 ")

	// The holder finds the same branch from the ticket id alone; a session
	// that holds nothing is not told one.
	held := other.call(t, "ticket_get", map[string]any{"ticket_id": two.ID})
	assert.Contains(t, resultText(t, held), "Branch: ticket-"+itoa(two.ID)+"-a2 ")
	assert.Equal(t, "ticket-"+itoa(two.ID)+"-a2", structured(t, second)["attempt"].(map[string]any)["branch"], "the claim's structured output names it")
	assert.Equal(t, "ticket-"+itoa(two.ID)+"-a2", structured(t, held)["attempt"].(map[string]any)["branch"], "so does ticket_get's")
	outsider := r.agentSession("third", false)
	unheld := outsider.call(t, "ticket_get", map[string]any{"ticket_id": two.ID})
	assert.NotContains(t, resultText(t, unheld), "Branch:")
	assert.NotContains(t, structured(t, unheld), "attempt")
}

func TestScopeNarrow_IsRefusedWhileSeveralAttemptsAreLive(t *testing.T) {
	r := newRealRig(t)
	made, err := r.tickets.Create(r.ctx, &ticket.CreateParams{
		Title: "Wide", Status: ticket.StatusOpen, Paths: []model.Path{{Path: "a/"}, {Path: "b/"}},
	})
	require.NoError(t, err)
	r.withSiblingAttempt(made.ID)
	require.False(t, r.call("ticket_next", map[string]any{"ticket_id": made.ID}).IsError)

	res := r.call("ticket_scope_narrow", map[string]any{"remove": []string{"b/"}})
	require.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "more than one attempt")
	got, err := r.tickets.Get(r.ctx, made.ID)
	require.NoError(t, err)
	assert.Len(t, got.Paths, 2)
}

// An agent reads a ticket's conversation through ticket_get alone, and is shown
// the ticket's own comments and its attempt's, never a sibling's.
func TestTicketGet_ShowsTheTicketsOwnCommentsAndOnlyItsOwnAttempts(t *testing.T) {
	r := newRealRig(t)
	held := r.openTicket("Two attempts")
	r.withSiblingAttempt(held.ID)
	second := r.agentSession("second", false)
	require.False(t, r.call("ticket_next", map[string]any{"ticket_id": held.ID}).IsError)
	require.False(t, second.call(t, "ticket_next", map[string]any{"ticket_id": held.ID}).IsError)

	say := func(attempt *ticket.AttemptRef, body string) {
		t.Helper()
		_, err := r.tickets.PostComment(r.ctx, &ticket.HumanComment{TicketID: held.ID, UserID: r.human, Body: body, Attempt: attempt})
		require.NoError(t, err)
	}
	say(nil, "GUIDANCE-FOR-BOTH")
	say(&ticket.AttemptRef{TicketID: held.ID, Number: 1}, "ONLY-ATTEMPT-ONE")
	say(&ticket.AttemptRef{TicketID: held.ID, Number: 2}, "ONLY-ATTEMPT-TWO")

	first := resultText(t, r.call("ticket_get", map[string]any{"ticket_id": held.ID}))
	assert.Contains(t, first, "GUIDANCE-FOR-BOTH")
	assert.Contains(t, first, "ONLY-ATTEMPT-ONE")
	assert.NotContains(t, first, "ONLY-ATTEMPT-TWO")

	sibling := resultText(t, second.call(t, "ticket_get", map[string]any{"ticket_id": held.ID}))
	assert.Contains(t, sibling, "GUIDANCE-FOR-BOTH")
	assert.Contains(t, sibling, "ONLY-ATTEMPT-TWO")
	assert.NotContains(t, sibling, "ONLY-ATTEMPT-ONE")
}

// A comment from a person whose account is gone reads as theirs, not as an
// ordinary "human" and not by id.
func TestTicketGet_ADeletedPersonsCommentAndEditAreLabelledDeleted(t *testing.T) {
	r := newRealRig(t)
	held := r.openTicket("Edited and discussed")
	require.False(t, r.call("ticket_next", map[string]any{"ticket_id": held.ID}).IsError)
	_, err := r.tickets.PostComment(r.ctx, &ticket.HumanComment{TicketID: held.ID, UserID: r.human, Body: "COMMENT-FROM-A-PERSON"})
	require.NoError(t, err)
	title := "Edited by a person"
	_, err = r.tickets.Edit(r.ctx, held.ID, r.human, &ticket.EditParams{Title: &title})
	require.NoError(t, err)
	_, err = r.database.Global().Exec(context.Background(), `DELETE FROM global_users WHERE id = $1`, r.human)
	require.NoError(t, err)

	res := r.call("ticket_get", map[string]any{"ticket_id": held.ID})
	got := resultText(t, res)
	assert.Contains(t, got, "- deleted user (", "the comment")
	assert.Contains(t, got, " by deleted user at ", "the edit")
	assert.NotContains(t, got, "- human (")
	assert.NotContains(t, got, " by human at ")
	assert.NotContains(t, got, r.human.String())
	assert.Contains(t, got, "COMMENT-FROM-A-PERSON")
}
