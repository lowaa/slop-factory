package main

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
)

// attempts are the attempt_number of the rows of table that match where, in
// id order; nil is a row that names none.
func (r *contractRig) attempts(table, where string, args ...any) []*int {
	r.t.Helper()
	ctx := db.WithTenant(context.Background(), r.user.TenantID)
	rows, err := r.boot.appDB.Query(ctx, `SELECT attempt_number FROM `+table+` WHERE `+where+` ORDER BY 1 NULLS FIRST`, args...)
	require.NoError(r.t, err)
	defer rows.Close()
	var out []*int
	for rows.Next() {
		var n *int
		require.NoError(r.t, rows.Scan(&n))
		out = append(out, n)
	}
	require.NoError(r.t, rows.Err())
	return out
}

// agentWithToken is agentNamed that also hands back the agent's token, for the
// hooks an agent calls over HTTP.
func (r *contractRig) agentWithToken(name string) (string, *contractAgent) {
	r.t.Helper()
	token := r.boot.agentTokenFor(r.t, r.user, &oauth.Decision{Approve: true, AgentName: name})
	a := &contractAgent{rig: r, name: name, id: r.agentBody(name)["id"].(string)}
	client := mcp.NewClient(&mcp.Implementation{Name: "contract-" + name, Version: "0.0.1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: r.boot.base + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token: token}},
	}, nil)
	require.NoError(r.t, err)
	r.t.Cleanup(func() { _ = session.Close() })
	a.session = session
	return token, a
}

func one() *int { n := 1; return &n }

// commentAttempt is the attempt the one comment with this body belongs to.
func (r *contractRig) commentAttempt(id int64, body string) []*int {
	r.t.Helper()
	return r.attempts("ticket_comments", "ticket_id = $1 AND body = $2", id, body)
}

func (r *contractRig) personComments(id int64, body string) {
	r.t.Helper()
	r.mustBoard(http.StatusCreated, http.MethodPost, fmt.Sprintf("/api/tickets/%d/comments", id), jsonObject{"body": body})
}

// personCommentsOn is a person's comment addressed to the attempt, as a ticket
// with several live attempts requires.
func (r *contractRig) personCommentsOn(id int64, attempt int, body string) {
	r.t.Helper()
	r.mustBoard(http.StatusCreated, http.MethodPost, fmt.Sprintf("/api/tickets/%d/comments?attempt=%d", id, attempt), jsonObject{"body": body})
}

// The tables whose attempt-1 defaults C3c dropped are written by Go with the
// attempt named: every insert path here runs against the real schema, which
// has no defaults, and each lands on the attempt it should.
func TestChildInserts_NameTheirAttemptWithoutDefaults(t *testing.T) {
	t.Parallel()
	rig := newWebhookRig(t)

	// A claim.
	f := rig.gate(true)
	assert.Equal(t, []*int{one()}, rig.attempts("leases", "ticket_id = $1", f.id), "the lease")
	assert.Equal(t, []*int{one()}, rig.attempts("ticket_changes", "ticket_id = $1", f.id), "the change opened")

	// Before any work on a ticket, a person's comment is the ticket's own.
	prep := ticketID(t, rig.openTicket(jsonObject{"title": "Not started", "status": "preparing"}))
	rig.personComments(prep, "while preparing")
	assert.Equal(t, []*int{nil}, rig.commentAttempt(prep, "while preparing"))
	idle := ticketID(t, rig.openTicket(jsonObject{"title": "Not claimed"}))
	rig.personComments(idle, "before the first claim")
	assert.Equal(t, []*int{nil}, rig.commentAttempt(idle, "before the first claim"))

	// Comments of each kind once work has started.
	f.agent.mustCall("ticket_comment", jsonObject{"ticket_id": f.id, "body": "from the agent"})
	assert.Equal(t, []*int{one()}, rig.commentAttempt(f.id, "from the agent"))
	rig.personComments(f.id, "from a person")
	assert.Equal(t, []*int{one()}, rig.commentAttempt(f.id, "from a person"), "a person's after work started")
	// An agent without a lease on the ticket comments on the ticket itself.
	rig.worker().mustCall("ticket_comment", jsonObject{"ticket_id": f.id, "body": "from a bystander"})
	assert.Equal(t, []*int{nil}, rig.commentAttempt(f.id, "from a bystander"))

	// Blocker notes follow the same rule: the holder's is its attempt's.
	blocker := ticketID(t, rig.openTicket(jsonObject{"title": "Prerequisite"}))
	f.agent.mustCall("ticket_blocker_add", jsonObject{"ticket_id": f.id, "blocker_id": blocker})
	f.agent.mustCall("ticket_blocker_remove", jsonObject{"ticket_id": f.id, "blocker_id": blocker})
	assert.Equal(t, []*int{one()}, rig.attempts("ticket_comments", "ticket_id = $1 AND body LIKE 'Blocked on #%'", f.id))
	assert.Equal(t, []*int{one()}, rig.attempts("ticket_comments", "ticket_id = $1 AND body LIKE 'No longer blocked on #%'", f.id))

	// A handoff, and the agent's own message with it.
	f.review()
	assert.Equal(t, []*int{one()}, rig.attempts("agent_contexts", "ticket_id = $1", f.id), "the handoff")

	// A webhook delivery matched to the ticket, and one that matched none.
	require.Equal(t, http.StatusOK, rig.send(&post{event: "ping", body: jsonObject{}}).code)
	approval := &post{id: "approval", event: "pull_request_review", body: reviewEvent(reviewPayload{state: "approved"})}
	rig.received(approval, "applied: #1 approved")
	assert.Equal(t, []*int{one()}, rig.attempts("github_deliveries", "ticket_id = $1", f.id), "the delivery")
	assert.Equal(t, []*int{nil}, rig.attempts("github_deliveries", "ticket_id IS NULL AND event = 'ping'"),
		"a delivery with no ticket has no attempt")

	// GitHub redelivering one that failed starts it afresh, so it names
	// neither the ticket nor the attempt until handled again.
	ctx := db.WithTenant(context.Background(), rig.user.TenantID)
	_, err := rig.boot.appDB.Exec(ctx, `UPDATE github_deliveries SET outcome = 'error' WHERE delivery_id = 'approval'`)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rig.send(approval).code)
	assert.Equal(t, []*int{nil, nil}, rig.attempts("github_deliveries", "(delivery_id = 'approval' OR event = 'ping') AND ticket_id IS NULL"),
		"handled again, the review no longer applies")

	// Done, and a person's note on the merged change.
	require.Equal(t, f.id, grantedID(f.agent.mustCall("ticket_next", nil)))
	f.agent.mustCall("integration_lock", nil)
	f.merges(jsonObject{"head_sha": headA})
	assert.Equal(t, []*int{one()}, rig.attempts("ticket_comments", "ticket_id = $1 AND body LIKE 'Merged %'", f.id), "the merge note")
	f.agent.mustComplete(jsonObject{"handoff": "All done.", "pr_url": flowLink})
	rig.mustBoard(http.StatusOK, http.MethodPost, fmt.Sprintf("/api/tickets/%d/post_review", f.id), jsonObject{"comment": "looks fine"})
	assert.Equal(t, []*int{one()}, rig.commentAttempt(f.id, "looks fine"), "a post-review note")

	// The usage an agent reports for the work it is on.
	usageTicket := ticketID(t, rig.openTicket(jsonObject{"title": "Reported on"}))
	token, reporter := rig.agentWithToken("reporter")
	require.Equal(t, usageTicket, grantedID(reporter.mustCall("ticket_next", jsonObject{"ticket_id": usageTicket})))
	now := time.Now().UTC()
	report := fmt.Sprintf(`{"transcript_key":"t-1","harness":"pi","session_id":"s-1","scope":"main","records":[{
		"schema_version":1,"provider":"anthropic","model":"m","harness":"pi","billing_mode":"subscription",
		"requests":1,"input_tokens":10,"output_tokens":5,"cache_read_tokens":0,"cache_write_tokens":0,"reasoning_tokens":0,
		"started_at":%q,"ended_at":%q,"tools":[]}]}`, now.Add(-time.Minute).Format(time.RFC3339), now.Add(time.Second).Format(time.RFC3339))
	resp := do(t, &request{method: http.MethodPost, url: rig.boot.base + "/hooks/usage", token: token, body: report})
	require.Equal(t, http.StatusOK, resp.code, resp.body)
	assert.JSONEq(t, `{"scope":"ticket","ticket_id":`+fmt.Sprint(usageTicket)+`}`, resp.body)
	assert.Equal(t, []*int{one()}, rig.attempts("llm_usage", "ticket_id = $1", usageTicket), "the attributed usage")
}

func two() *int { n := 2; return &n }

// With attempt 1 held by the agent and attempt 2 promoted, each insert goes
// where its own rule sends it: what the agent does is its lease's attempt,
// and what a person or a console does about the ticket is the attempt it
// names (with several live, one must be named).
// A test with one attempt cannot tell the two apart.
func TestChildInserts_FollowTheirLeaseOrThePromotion(t *testing.T) {
	t.Parallel()
	rig := newContractRig(t)
	console := rig.console("console")
	f := rig.startFlow(false)
	ctx := t.Context()
	tenant := db.WithTenant(ctx, rig.user.TenantID)
	for _, stmt := range []string{
		`INSERT INTO attempts (ticket_id, number, status) VALUES ($1, 2, 'queued')`,
		`UPDATE attempts SET promoted_at = NULL WHERE ticket_id = $1 AND number = 1`,
		`UPDATE attempts SET promoted_at = now() WHERE ticket_id = $1 AND number = 2`,
		`UPDATE tickets SET base_commit = repeat('1', 40) WHERE id = $1`, // an unpromoted attempt's change needs the base branch's commit
	} {
		_, err := rig.boot.appDB.Exec(tenant, stmt, f.id)
		require.NoError(t, err, stmt)
	}

	f.agent.mustCall("ticket_comment", jsonObject{"ticket_id": f.id, "body": "from the holder"})
	assert.Equal(t, []*int{one()}, rig.commentAttempt(f.id, "from the holder"))
	rig.personCommentsOn(f.id, 2, "from a person")
	assert.Equal(t, []*int{two()}, rig.commentAttempt(f.id, "from a person"))
	require.False(t, f.open(true).isError, "an unpromoted attempt opens a draft")
	assert.Equal(t, []*int{one()}, rig.attempts("ticket_changes", "ticket_id = $1", f.id), "the change is the opening lease's")

	f.agent.mustCall("ticket_discuss", jsonObject{"question": "Which way?", "handoff": "Waiting."})
	assert.Equal(t, []*int{one()}, rig.attempts("agent_contexts", "ticket_id = $1", f.id), "the handoff is the leaving lease's")
	assert.Equal(t, []*int{one()}, rig.commentAttempt(f.id, "Which way?"))
	answer := proposalID(t, console.mustCall("propose_discussion_answer", jsonObject{"ticket_id": f.id, "attempt": 1, "body": "The first."}))
	assert.Equal(t, []*int{one()}, rig.attempts("pending_actions", "ticket_id = $1", f.id), "the proposal is for the attempt it names, the one asking")
	require.Equal(t, http.StatusOK, rig.approve(answer).code)
	assert.Equal(t, []*int{one()}, rig.attempts("ticket_comments", "ticket_id = $1 AND body LIKE 'The first.%'", f.id))
}

// An answer and a decision are about the attempt; the other proposals are the
// ticket's.
func TestChildInserts_ProposalsNameTheirAttemptWithoutDefaults(t *testing.T) {
	t.Parallel()
	rig := newContractRig(t)
	console := rig.console("console")

	asked, _ := rig.discussionTicket("Needs an answer")
	answer := proposalID(t, console.mustCall("propose_discussion_answer", jsonObject{"ticket_id": asked, "body": "Go with the second."}))
	reviewed, _ := rig.reviewTicket("Reviewed")
	decision := proposalID(t, console.mustCall("propose_review_decision", jsonObject{"ticket_id": reviewed, "decision": "approve"}))
	preparing := ticketID(t, rig.openTicket(jsonObject{"title": "Draft", "status": "preparing"}))
	ready := proposalID(t, console.mustCall("propose_ready", jsonObject{"ticket_id": preparing}))

	assert.Equal(t, []*int{one()}, rig.attempts("pending_actions", "ticket_id = $1", asked), "a discussion answer")
	assert.Equal(t, []*int{one()}, rig.attempts("pending_actions", "ticket_id = $1", reviewed), "a review decision")
	assert.Equal(t, []*int{nil}, rig.attempts("pending_actions", "ticket_id = $1", preparing), "a ticket-level proposal")

	// Approving them writes the person's comments, on the attempt.
	for _, id := range []string{answer, decision, ready} {
		require.Equal(t, http.StatusOK, rig.approve(id).code)
	}
	assert.Equal(t, []*int{one()}, rig.attempts("ticket_comments", "ticket_id = $1 AND body LIKE 'Go with the second.%'", asked))
	assert.Equal(t, []*int{one()}, rig.attempts("ticket_comments", "ticket_id = $1 AND body LIKE 'Approved.%'", reviewed))
}
