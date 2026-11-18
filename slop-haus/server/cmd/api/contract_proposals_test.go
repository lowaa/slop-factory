package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
)

// Console proposals as the console and the person see them: the console
// proposes over MCP, nothing changes, and the person decides on the board.

const proposalPR = "https://github.com/acme/repo/pull/12"

// console is a new agent the person chose to register as their own session.
func (r *contractRig) console(name string) *contractAgent {
	r.t.Helper()
	return r.agentOfKind(name, "console")
}

// proposalID is the id of the proposal a propose_* call or withdraw answered.
func proposalID(t *testing.T, res *toolResult) string {
	t.Helper()
	id, ok := res.structured["id"].(string)
	require.True(t, ok, "no proposal id in %s", res.text)
	return id
}

// proposals are the proposals the board lists, newest first; every state
// when state is empty.
func (r *contractRig) proposals(state string) []any {
	r.t.Helper()
	path := "/api/proposals"
	if state != "" {
		path += "?state=" + state
	}
	return r.mustBoard(http.StatusOK, http.MethodGet, path, nil).obj(r.t)["proposals"].([]any)
}

func (r *contractRig) proposal(id string) jsonObject {
	r.t.Helper()
	return r.mustBoard(http.StatusOK, http.MethodGet, "/api/proposals/"+id, nil).obj(r.t)
}

func (r *contractRig) approve(id string) *boardResponse {
	return r.boardDo(http.MethodPost, "/api/proposals/"+id+"/approve", nil)
}

func (r *contractRig) reject(id, reason string) *boardResponse {
	return r.boardDo(http.MethodPost, "/api/proposals/"+id+"/reject", jsonObject{"reason": reason})
}

// discussionTicket is a ticket a worker claimed and asked the person about, and
// that worker.
func (r *contractRig) discussionTicket(title string) (int64, *contractAgent) {
	r.t.Helper()
	id := ticketID(r.t, r.openTicket(jsonObject{"title": title}))
	w := r.worker()
	w.mustCall("ticket_next", jsonObject{"ticket_id": id})
	w.mustCall("ticket_discuss", jsonObject{"question": "Which way?", "handoff": "Planned, waiting on an answer."})
	require.Equal(r.t, "discussion", r.ticket(id)["status"])
	// The round is labelled off the request that opened it; later snapshots
	// of the ticket should not catch it changing.
	require.Eventually(r.t, func() bool {
		attempts, _ := r.ticket(id)["attempts"].([]any)
		if len(attempts) == 0 {
			return false
		}
		d, _ := attempts[0].(map[string]any)["discussion"].(map[string]any)
		return d != nil && d["label"] != nil
	}, 10*time.Second, 5*time.Millisecond)
	return id, w
}

// reviewTicket is a ticket a worker handed over for review, and that worker.
func (r *contractRig) reviewTicket(title string) (int64, *contractAgent) {
	r.t.Helper()
	id := ticketID(r.t, r.openTicket(jsonObject{"title": title}))
	w := r.worker()
	w.mustCall("ticket_next", jsonObject{"ticket_id": id})
	w.mustCall("ticket_finish", jsonObject{"pr_url": proposalPR, "handoff": "Done, please review.", "request_review": true, "reason": "a person should read it"})
	require.Equal(r.t, "review", r.ticket(id)["status"])
	return id, w
}

func TestContractProposals_DiscussionAnswer(t *testing.T) {
	t.Parallel()
	rig := newContractRig(t)
	console := rig.console("console")
	id, asker := rig.discussionTicket("Needs an answer")
	before := rig.ticket(id)
	commentsBefore := rig.commentBodies(id)

	proposed := console.mustCall("propose_discussion_answer", jsonObject{"ticket_id": id, "body": "Go with the second."})
	rig.assertGolden("proposals/propose_discussion_answer", proposed)
	pid := proposalID(t, proposed)

	t.Run("nothing changes before the person approves", func(t *testing.T) {
		assert.Equal(t, before, rig.ticket(id))
		assert.Equal(t, commentsBefore, rig.commentBodies(id))
		p := rig.proposal(pid)
		assert.Equal(t, "pending", p["state"])
		assert.Equal(t, "discussion_answer", p["kind"])
		assert.Equal(t, "console", p["agent_name"])
		assert.Equal(t, "Go with the second.", p["text"])
		assert.Equal(t, "discussion", p["ticket_status"])
		assert.Empty(t, p["stale_reason"])
		assert.Equal(t, "Post the text below as your comment on #1. That answers its discussion and puts it back in the queue.", p["action_line"])
	})

	t.Run("approving posts the answer as the person and puts the ticket back to its asker", func(t *testing.T) {
		resp := rig.approve(pid)
		require.Equal(t, http.StatusOK, resp.code, resp.raw)
		p := resp.obj(t)
		assert.Equal(t, "approved", p["state"])
		assert.Equal(t, rig.user.ID.String(), p["decided_by_user_id"])
		assert.NotNil(t, p["decided_at"])

		assert.Equal(t, "open", rig.ticket(id)["status"])
		bodies := rig.commentBodies(id)
		require.Len(t, bodies, len(commentsBefore)+1)
		assert.Equal(t, "Go with the second.\n\n_Drafted by console._", bodies[len(bodies)-1])

		other := rig.worker()
		assert.Zero(t, grantedID(other.mustCall("ticket_next", nil)), "the asker is owed it")
		assert.Equal(t, id, grantedID(asker.mustCall("ticket_next", nil)))
	})

	t.Run("approving again applies nothing twice", func(t *testing.T) {
		before := rig.commentBodies(id)
		assert.Equal(t, http.StatusConflict, rig.approve(pid).code)
		assert.Equal(t, before, rig.commentBodies(id))
	})
}

func TestContractProposals_Ready(t *testing.T) {
	t.Parallel()
	rig := newContractRig(t)
	console := rig.console("console")
	id := ticketID(t, rig.openTicket(jsonObject{"title": "Not ready", "status": "preparing"}))
	before := rig.ticket(id)

	proposed := console.mustCall("propose_ready", jsonObject{"ticket_id": id})
	rig.assertGolden("proposals/propose_ready", proposed)
	pid := proposalID(t, proposed)

	assert.Equal(t, before, rig.ticket(id), "proposing changes nothing")
	assert.Zero(t, grantedID(rig.worker().mustCall("ticket_next", nil)))

	require.Equal(t, http.StatusOK, rig.approve(pid).code)
	assert.Equal(t, "open", rig.ticket(id)["status"])
	assert.Equal(t, id, grantedID(rig.worker().mustCall("ticket_next", nil)))

	t.Run("a ticket that is not preparing cannot be proposed ready", func(t *testing.T) {
		open := ticketID(t, rig.openTicket(jsonObject{"title": "Already open"}))
		res := console.call("propose_ready", jsonObject{"ticket_id": open})
		assert.True(t, res.isError)
		assert.Contains(t, res.text, "ready needs a ticket in preparing, this one is open")
	})
}

func TestContractProposals_Status(t *testing.T) {
	t.Parallel()
	t.Run("parking posts the reason and parks the ticket", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Park me"}))

		proposed := console.mustCall("propose_status", jsonObject{"ticket_id": id, "status": "parked", "reason": "Waiting on the vendor."})
		rig.assertGolden("proposals/propose_status_parked", proposed)
		assert.Equal(t, "open", rig.ticket(id)["status"])

		require.Equal(t, http.StatusOK, rig.approve(proposalID(t, proposed)).code)
		assert.Equal(t, "parked", rig.ticket(id)["status"])
		assert.Equal(t, []string{"Waiting on the vendor.\n\n_Drafted by console._"}, rig.commentBodies(id))
		assert.Zero(t, grantedID(rig.worker().mustCall("ticket_next", nil)), "a parked ticket is not worked")
	})

	t.Run("cancelling posts the reason and closes the ticket", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Cancel me"}))

		proposed := console.mustCall("propose_status", jsonObject{"ticket_id": id, "status": "cancelled", "reason": "No longer needed."})
		rig.assertGolden("proposals/propose_status_cancelled", proposed)
		require.Equal(t, http.StatusOK, rig.approve(proposalID(t, proposed)).code)
		closed := rig.ticket(id)
		assert.Equal(t, "cancelled", closed["status"])
		assert.NotNil(t, closed["closed_at"])
		assert.Equal(t, []string{"No longer needed.\n\n_Drafted by console._"}, rig.commentBodies(id))
	})

	t.Run("only parking and cancelling can be proposed", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Stay put"}))
		for _, status := range []string{"open", "done", "in_progress", "blocked", "review", "preparing", "bogus"} {
			res := console.call("propose_status", jsonObject{"ticket_id": id, "status": status, "reason": "Because."})
			assert.True(t, res.isError, status)
			assert.Contains(t, res.text, "a status proposal moves a ticket to parked or cancelled", status)
		}
		blank := console.call("propose_status", jsonObject{"ticket_id": id, "status": "parked", "reason": "  "})
		assert.True(t, blank.isError)
		assert.Contains(t, blank.text, "a reason is required")
		assert.Empty(t, rig.proposals(""))
		assert.Equal(t, "open", rig.ticket(id)["status"])
	})

	t.Run("a ticket already in the status, or finished, cannot be moved", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		parked := ticketID(t, rig.openTicket(jsonObject{"title": "Parked"}))
		rig.mustBoard(http.StatusOK, http.MethodPatch, fmt.Sprintf("/api/tickets/%d", parked), jsonObject{"status": "parked"})
		res := console.call("propose_status", jsonObject{"ticket_id": parked, "status": "parked", "reason": "Again."})
		assert.True(t, res.isError)
		assert.Contains(t, res.text, "cannot be moved to parked")

		w := rig.worker()
		done := ticketID(t, rig.openTicket(jsonObject{"title": "Done"}))
		w.mustCall("ticket_next", jsonObject{"ticket_id": done})
		w.mustComplete(jsonObject{"pr_url": "https://github.com/acme/repo/pull/1", "handoff": "Done."})
		res = console.call("propose_status", jsonObject{"ticket_id": done, "status": "cancelled", "reason": "Too late."})
		assert.True(t, res.isError)
		assert.Contains(t, res.text, "cannot be moved to cancelled")
	})
}

func TestContractProposals_ReviewDecision(t *testing.T) {
	t.Parallel()
	t.Run("approving a change in review approves it as the person", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		id, _ := rig.reviewTicket("Reviewed")
		before := rig.ticket(id)
		assert.Nil(t, before["approved_at"])

		proposed := console.mustCall("propose_review_decision", jsonObject{"ticket_id": id, "decision": "approve"})
		rig.assertGolden("proposals/propose_review_decision_approve", proposed)
		assert.Equal(t, before, rig.ticket(id), "proposing changes nothing")

		require.Equal(t, http.StatusOK, rig.approve(proposalID(t, proposed)).code)
		approved := rig.ticket(id)
		assert.Equal(t, "open", approved["status"], "approved and waiting for an agent to merge it")
		assert.NotNil(t, approved["approved_at"])
		assert.Equal(t, rig.user.ID.String(), approved["approved_by_user_id"])
		assert.Contains(t, rig.commentBodies(id), "Approved.\n\n_Drafted by console._")
	})

	t.Run("requesting changes sends the ticket back with what to address", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		id, _ := rig.reviewTicket("Sent back")

		proposed := console.mustCall("propose_review_decision", jsonObject{"ticket_id": id, "decision": "request_changes", "body": "Handle the empty case."})
		rig.assertGolden("proposals/propose_review_decision_request_changes", proposed)
		require.Equal(t, http.StatusOK, rig.approve(proposalID(t, proposed)).code)
		assert.Equal(t, "open", rig.ticket(id)["status"])
		assert.Contains(t, rig.commentBodies(id), "Handle the empty case.\n\n_Drafted by console._")
	})

	t.Run("a decision needs a ticket in review, a known decision and text for changes", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		open := ticketID(t, rig.openTicket(jsonObject{"title": "Open"}))
		res := console.call("propose_review_decision", jsonObject{"ticket_id": open, "decision": "approve"})
		assert.True(t, res.isError)
		assert.Contains(t, res.text, "review_decision needs a ticket in review, this one is open")

		id, _ := rig.reviewTicket("In review")
		res = console.call("propose_review_decision", jsonObject{"ticket_id": id, "decision": "request_changes"})
		assert.True(t, res.isError)
		assert.Contains(t, res.text, "requesting changes needs text")
		res = console.call("propose_review_decision", jsonObject{"ticket_id": id, "decision": "merge"})
		assert.True(t, res.isError)
		assert.Contains(t, res.text, "a review decision is approve or request_changes")
		assert.Empty(t, rig.proposals(""))
	})
}

func TestContractProposals_ListAndWithdraw(t *testing.T) {
	t.Parallel()
	rig := newContractRig(t)
	console := rig.console("console")
	other := rig.console("other-console")

	empty := console.mustCall("list_proposals", nil)
	assert.Equal(t, "No proposals.", empty.text)
	rig.assertGolden("proposals/list_proposals_empty", empty)

	a := ticketID(t, rig.openTicket(jsonObject{"title": "A", "status": "preparing"}))
	b := ticketID(t, rig.openTicket(jsonObject{"title": "B", "status": "preparing"}))
	c := ticketID(t, rig.openTicket(jsonObject{"title": "C", "status": "preparing"}))
	pa := proposalID(t, console.mustCall("propose_ready", jsonObject{"ticket_id": a}))
	pb := proposalID(t, console.mustCall("propose_ready", jsonObject{"ticket_id": b}))
	pc := proposalID(t, other.mustCall("propose_ready", jsonObject{"ticket_id": c}))

	require.Equal(t, http.StatusOK, rig.approve(pa).code)
	require.Equal(t, http.StatusOK, rig.reject(pb, "Not yet.").code)

	t.Run("lists the console's own proposals, newest first, with how each ended", func(t *testing.T) {
		listed := console.mustCall("list_proposals", nil)
		rig.assertGolden("proposals/list_proposals", listed)
		got := listed.structured["proposals"].([]any)
		require.Len(t, got, 2)
		assert.Equal(t, pb, got[0].(map[string]any)["id"])
		assert.Equal(t, "rejected", got[0].(map[string]any)["state"])
		assert.Equal(t, "rejected: Not yet.", got[0].(map[string]any)["outcome"], "the console reads why")
		assert.Equal(t, pa, got[1].(map[string]any)["id"])
		assert.Equal(t, "approved", got[1].(map[string]any)["state"])
	})

	t.Run("filters by state", func(t *testing.T) {
		approved := console.mustCall("list_proposals", jsonObject{"status": "approved"}).structured["proposals"].([]any)
		require.Len(t, approved, 1)
		assert.Equal(t, pa, approved[0].(map[string]any)["id"])
		assert.Equal(t, "No proposals.", console.mustCall("list_proposals", jsonObject{"status": "pending"}).text)
	})

	t.Run("another console's proposal is not listed and cannot be withdrawn", func(t *testing.T) {
		assert.Equal(t, "No proposals.", other.mustCall("list_proposals", jsonObject{"status": "approved"}).text)
		res := console.call("withdraw_proposal", jsonObject{"id": pc})
		assert.True(t, res.isError)
		assert.Equal(t, "pending", rig.proposal(pc)["state"])
	})

	t.Run("withdrawing takes a pending proposal off the person's queue", func(t *testing.T) {
		withdrawn := other.mustCall("withdraw_proposal", jsonObject{"id": pc})
		rig.assertGolden("proposals/withdraw_proposal", withdrawn)
		assert.Equal(t, "withdrawn", rig.proposal(pc)["state"])
		assert.Equal(t, "preparing", rig.ticket(c)["status"])

		assert.Equal(t, http.StatusConflict, rig.approve(pc).code, "a withdrawn proposal cannot be approved")
		assert.Equal(t, "preparing", rig.ticket(c)["status"])
		again := other.call("withdraw_proposal", jsonObject{"id": pc})
		assert.True(t, again.isError, "nor withdrawn twice")
	})

	t.Run("a decided proposal cannot be withdrawn", func(t *testing.T) {
		res := console.call("withdraw_proposal", jsonObject{"id": pa})
		assert.True(t, res.isError)
		assert.Equal(t, "approved", rig.proposal(pa)["state"])
	})

	t.Run("a malformed id is refused", func(t *testing.T) {
		res := console.call("withdraw_proposal", jsonObject{"id": "nope"})
		assert.True(t, res.isError)
		assert.Contains(t, res.text, "id must be a proposal id")
	})
}

func TestContractProposals_Decisions(t *testing.T) {
	t.Parallel()
	t.Run("rejecting leaves the ticket as it was and tells the console why", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Hold", "status": "preparing"}))
		before := rig.ticket(id)
		pid := proposalID(t, console.mustCall("propose_ready", jsonObject{"ticket_id": id}))

		resp := rig.reject(pid, "Needs more detail.")
		require.Equal(t, http.StatusOK, resp.code, resp.raw)
		assert.Equal(t, "rejected", resp.obj(t)["state"])
		assert.Equal(t, "rejected: Needs more detail.", resp.obj(t)["outcome"])
		assert.Equal(t, rig.user.ID.String(), resp.obj(t)["decided_by_user_id"])
		assert.Equal(t, before, rig.ticket(id))

		assert.Equal(t, http.StatusConflict, rig.approve(pid).code, "a rejected proposal cannot be approved")
		assert.Equal(t, http.StatusConflict, rig.reject(pid, "").code, "nor rejected twice")
		assert.Equal(t, before, rig.ticket(id))
	})

	t.Run("a rejection without a reason says it was rejected", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Hold", "status": "preparing"}))
		pid := proposalID(t, console.mustCall("propose_ready", jsonObject{"ticket_id": id}))
		assert.Equal(t, "rejected", rig.reject(pid, "").obj(t)["outcome"])
	})

	t.Run("an unknown proposal is not found", func(t *testing.T) {
		rig := newContractRig(t)
		const missing = "00000000-0000-4000-8000-000000000000"
		assert.Equal(t, http.StatusNotFound, rig.boardDo(http.MethodGet, "/api/proposals/"+missing, nil).code)
		assert.Equal(t, http.StatusNotFound, rig.approve(missing).code)
		assert.Equal(t, http.StatusNotFound, rig.reject(missing, "").code)
		assert.Equal(t, http.StatusNotFound, rig.approve("not-an-id").code)
	})

	t.Run("approve all takes only the ready and answer kinds, each on its own", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		var ready []int64
		for i := range 2 {
			ready = append(ready, ticketID(t, rig.openTicket(jsonObject{"title": fmt.Sprintf("Ready %d", i), "status": "preparing"})))
		}
		parked := ticketID(t, rig.openTicket(jsonObject{"title": "To park"}))
		discussionID, _ := rig.discussionTicket("Answer me")
		for _, id := range ready {
			console.mustCall("propose_ready", jsonObject{"ticket_id": id})
		}
		console.mustCall("propose_status", jsonObject{"ticket_id": parked, "status": "parked", "reason": "Later."})
		console.mustCall("propose_discussion_answer", jsonObject{"ticket_id": discussionID, "body": "Yes."})

		resp := rig.mustBoard(http.StatusOK, http.MethodPost, "/api/proposals/approve_all", jsonObject{"kind": "ready"})
		results := resp.obj(t)["results"].([]any)
		require.Len(t, results, 2)
		for _, r := range results {
			assert.Equal(t, "approved", r.(map[string]any)["state"])
		}
		for _, id := range ready {
			assert.Equal(t, "open", rig.ticket(id)["status"])
		}
		assert.Equal(t, "open", rig.ticket(parked)["status"], "other kinds are not swept in")
		assert.Equal(t, "discussion", rig.ticket(discussionID)["status"])
		assert.Len(t, rig.proposals("pending"), 2)

		assert.Equal(t, http.StatusUnprocessableEntity,
			rig.boardDo(http.MethodPost, "/api/proposals/approve_all", jsonObject{"kind": "status"}).code, "a status change is never batched")
		assert.Equal(t, http.StatusUnprocessableEntity,
			rig.boardDo(http.MethodPost, "/api/proposals/approve_all", jsonObject{"kind": "review_decision"}).code)
		assert.Equal(t, "open", rig.ticket(parked)["status"])

		rig.mustBoard(http.StatusOK, http.MethodPost, "/api/proposals/approve_all", jsonObject{"kind": "discussion_answer"})
		assert.Equal(t, "open", rig.ticket(discussionID)["status"])
		assert.Equal(t, "open", rig.ticket(parked)["status"])
		assert.Len(t, rig.proposals("pending"), 1)
	})

	t.Run("reject all rejects every pending proposal with the same reason", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		var ids []int64
		for i := range 3 {
			id := ticketID(t, rig.openTicket(jsonObject{"title": fmt.Sprintf("Ready %d", i), "status": "preparing"}))
			ids = append(ids, id)
			console.mustCall("propose_ready", jsonObject{"ticket_id": id})
		}
		resp := rig.mustBoard(http.StatusOK, http.MethodPost, "/api/proposals/reject_all", jsonObject{"reason": "Clearing out."})
		results := resp.obj(t)["results"].([]any)
		require.Len(t, results, 3)
		for _, r := range results {
			assert.Equal(t, "rejected", r.(map[string]any)["state"])
			assert.Equal(t, "rejected: Clearing out.", r.(map[string]any)["outcome"])
		}
		for _, id := range ids {
			assert.Equal(t, "preparing", rig.ticket(id)["status"])
		}
		assert.Empty(t, rig.proposals("pending"))
	})
}

func TestContractProposals_Stale(t *testing.T) {
	t.Parallel()
	t.Run("a ticket that moved on since is not changed by the approval", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Moves on", "status": "preparing"}))
		pid := proposalID(t, console.mustCall("propose_ready", jsonObject{"ticket_id": id}))

		// The person readies it themselves first.
		rig.mustBoard(http.StatusOK, http.MethodPost, fmt.Sprintf("/api/tickets/%d/ready", id), nil)
		listed := rig.proposal(pid)
		assert.Equal(t, "pending", listed["state"])
		assert.Contains(t, listed["stale_reason"], "it is now open, not preparing")

		resp := rig.approve(pid)
		assert.Equal(t, http.StatusConflict, resp.code, resp.raw)
		assert.Equal(t, "stale", rig.proposal(pid)["state"])
		assert.Equal(t, "open", rig.ticket(id)["status"])
	})

	t.Run("an edit to the ticket makes a proposal about it stale", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Edited", "status": "preparing"}))
		pid := proposalID(t, console.mustCall("propose_ready", jsonObject{"ticket_id": id}))
		rig.mustBoard(http.StatusOK, http.MethodPost, fmt.Sprintf("/api/tickets/%d/revisions", id), jsonObject{"title": "Edited again"})

		assert.Contains(t, rig.proposal(pid)["stale_reason"], "its title or text was edited")
		assert.Equal(t, http.StatusConflict, rig.approve(pid).code)
		assert.Equal(t, "preparing", rig.ticket(id)["status"])
	})

	t.Run("a status proposal on a ticket an agent then claims is not applied", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Claimed meanwhile"}))
		pid := proposalID(t, console.mustCall("propose_status", jsonObject{"ticket_id": id, "status": "cancelled", "reason": "Drop it."}))
		w := rig.worker()
		require.Equal(t, id, grantedID(w.mustCall("ticket_next", nil)))

		assert.Equal(t, http.StatusConflict, rig.approve(pid).code)
		assert.Equal(t, "stale", rig.proposal(pid)["state"])
		assert.Equal(t, "in_progress", rig.ticket(id)["status"])
		assert.Empty(t, rig.commentBodies(id))
	})

	t.Run("a ticket an agent holds cannot be proposed on, nor approved once held", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		held := ticketID(t, rig.openTicket(jsonObject{"title": "Held"}))
		w := rig.worker()
		w.mustCall("ticket_next", jsonObject{"ticket_id": held})
		res := console.call("propose_status", jsonObject{"ticket_id": held, "status": "parked", "reason": "Park."})
		assert.True(t, res.isError)
		assert.Contains(t, res.text, "held by an agent")
		assert.Empty(t, rig.proposals(""))

		asked, asker := rig.discussionTicket("Asked")
		pid := proposalID(t, console.mustCall("propose_discussion_answer", jsonObject{"ticket_id": asked, "body": "Yes."}))
		// The person answers by hand, which puts the ticket back to its asker,
		// who takes it up before the proposal is decided.
		rig.mustBoard(http.StatusCreated, http.MethodPost, fmt.Sprintf("/api/tickets/%d/comments", asked), jsonObject{"body": "Answered by hand."})
		assert.Equal(t, "open", rig.ticket(asked)["status"])
		require.Equal(t, asked, grantedID(asker.mustCall("ticket_next", nil)))
		resp := rig.approve(pid)
		assert.Equal(t, http.StatusConflict, resp.code, resp.raw)
		assert.Equal(t, "in_progress", rig.ticket(asked)["status"])
		assert.Equal(t, "stale", rig.proposal(pid)["state"])
	})
}

func TestContractProposals_Edit(t *testing.T) {
	t.Parallel()
	t.Run("approving an edit changes the ticket as the person", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Old title", "body": "Old body", "status": "preparing", "paths": []jsonObject{{"path": "old/"}}}))
		before := rig.ticket(id)

		proposed := console.mustCall("propose_ticket_edit", jsonObject{
			"ticket_id": id, "base_revision": before["revision"], "title": "New title", "body": "New body",
			"paths": []string{"new/"}, "priority": "high",
		})
		rig.assertGolden("proposals/propose_ticket_edit", proposed)
		assert.Equal(t, before, rig.ticket(id), "proposing changes nothing")

		card := rig.proposal(proposalID(t, proposed))
		assert.Equal(t, "edit", card["kind"])
		body := card["edit"].(map[string]any)["body"].(map[string]any)
		assert.Equal(t, "Old body", body["old"])
		assert.Equal(t, "New body", body["new"])
		assert.Equal(t, []any{
			jsonObject{"op": "delete", "text": "Old body"},
			jsonObject{"op": "insert", "text": "New body"},
		}, body["diff"])

		require.Equal(t, http.StatusOK, rig.approve(proposalID(t, proposed)).code)
		after := rig.ticket(id)
		assert.Equal(t, "New title", after["title"])
		assert.Equal(t, "New body", after["body"])
		assert.Equal(t, "high", after["priority"])
		assert.Equal(t, "preparing", after["status"], "an edit does not move the ticket")
		assert.Equal(t, float64(1), after["revision"])
	})

	t.Run("an edit with open changes the ticket and opens it in one approval", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Old title", "body": "Old body", "status": "preparing"}))

		proposed := console.mustCall("propose_ticket_edit", jsonObject{
			"ticket_id": id, "base_revision": 0, "body": "New body", "open": true,
		})
		rig.assertGolden("proposals/propose_ticket_edit_open", proposed)
		card := rig.proposal(proposalID(t, proposed))
		assert.Equal(t, true, card["edit"].(map[string]any)["open"])
		assert.Contains(t, card["action_line"], "move it out of preparing")

		require.Equal(t, http.StatusOK, rig.approve(proposalID(t, proposed)).code)
		after := rig.ticket(id)
		assert.Equal(t, "New body", after["body"])
		assert.Equal(t, "open", after["status"])
	})

	t.Run("open is refused on a ticket that is not in preparing", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		open := ticketID(t, rig.openTicket(jsonObject{"title": "Open"}))
		res := console.call("propose_ticket_edit", jsonObject{"ticket_id": open, "base_revision": 0, "body": "x", "open": true})
		assert.True(t, res.isError)
		assert.Contains(t, res.text, "needs one in preparing")
		assert.Empty(t, rig.proposals(""))
	})

	t.Run("a ready waits for a pending edit on the same ticket", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Title", "body": "Body", "status": "preparing"}))
		edit := console.mustCall("propose_ticket_edit", jsonObject{"ticket_id": id, "base_revision": 0, "body": "Rewritten"})
		ready := console.mustCall("propose_ready", jsonObject{"ticket_id": id})

		resp := rig.approve(proposalID(t, ready))
		assert.Equal(t, http.StatusConflict, resp.code, resp.raw)
		assert.Contains(t, resp.raw, "decide the pending edit for this ticket first")
		assert.Equal(t, "pending", rig.proposal(proposalID(t, ready))["state"])
		assert.Equal(t, "preparing", rig.ticket(id)["status"])

		batch := rig.mustBoard(http.StatusOK, http.MethodPost, "/api/proposals/approve_all", jsonObject{"kind": "ready"}).obj(t)
		result := batch["results"].([]any)[0].(map[string]any)
		assert.Equal(t, "pending", result["state"])
		assert.Contains(t, result["error"], "decide the pending edit for this ticket first")

		rig.mustBoard(http.StatusOK, http.MethodPost, "/api/proposals/"+proposalID(t, edit)+"/reject", jsonObject{})
		require.Equal(t, http.StatusOK, rig.approve(proposalID(t, ready)).code)
		assert.Equal(t, "open", rig.ticket(id)["status"])
	})

	t.Run("an edit made against an older revision is refused when proposed and when approved", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Title", "body": "Body", "status": "preparing"}))

		proposed := console.mustCall("propose_ticket_edit", jsonObject{"ticket_id": id, "base_revision": 0, "body": "Rewritten"})
		rig.mustBoard(http.StatusOK, http.MethodPost, fmt.Sprintf("/api/tickets/%d/revisions", id), jsonObject{"body": "The person's own edit", "base_revision": 0})

		resp := rig.approve(proposalID(t, proposed))
		assert.Equal(t, http.StatusConflict, resp.code, resp.raw)
		assert.Equal(t, "stale", rig.proposal(proposalID(t, proposed))["state"])
		assert.Equal(t, "The person's own edit", rig.ticket(id)["body"])

		res := console.call("propose_ticket_edit", jsonObject{"ticket_id": id, "base_revision": 0, "body": "Late"})
		assert.True(t, res.isError)
		assert.Contains(t, res.text, "revision 1, not the 0")
	})

	t.Run("a held, finished or empty-pathed ticket is refused", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		w := rig.worker()
		held := ticketID(t, rig.openTicket(jsonObject{"title": "Held"}))
		w.mustCall("ticket_next", jsonObject{"ticket_id": held})
		res := console.call("propose_ticket_edit", jsonObject{"ticket_id": held, "base_revision": 0, "body": "x"})
		assert.True(t, res.isError)
		assert.Contains(t, res.text, "held by an agent")
		w.mustComplete(jsonObject{"pr_url": "https://github.com/acme/repo/pull/1", "handoff": "Done."})
		res = console.call("propose_ticket_edit", jsonObject{"ticket_id": held, "base_revision": 0, "body": "x"})
		assert.True(t, res.isError)
		assert.Contains(t, res.text, "cannot be edited")

		open := ticketID(t, rig.openTicket(jsonObject{"title": "Open"}))
		res = console.call("propose_ticket_edit", jsonObject{"ticket_id": open, "base_revision": 0, "paths": []string{}})
		assert.True(t, res.isError)
		assert.Contains(t, res.text, "no paths")
		assert.Empty(t, rig.proposals(""))
	})
}

func TestContractProposals_WhoMayPropose(t *testing.T) {
	t.Parallel()
	t.Run("a worker that is not a console is refused every proposal tool", func(t *testing.T) {
		rig := newContractRig(t)
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Mine", "status": "preparing"}))
		w := rig.worker()
		calls := map[string]jsonObject{
			"propose_ready":             {"ticket_id": id},
			"propose_discussion_answer": {"ticket_id": id, "body": "x"},
			"propose_status":            {"ticket_id": id, "status": "parked", "reason": "x"},
			"propose_review_decision":   {"ticket_id": id, "decision": "approve"},
			"propose_ticket_edit":       {"ticket_id": id, "base_revision": 0, "body": "x"},
			"list_proposals":            nil,
			"withdraw_proposal":         {"id": "00000000-0000-4000-8000-000000000000"},
		}
		for tool, args := range calls {
			res := w.call(tool, args)
			assert.True(t, res.isError, tool)
			assert.Contains(t, res.text, "only a console agent", tool)
		}
		assert.Empty(t, rig.proposals(""))
		assert.Equal(t, "preparing", rig.ticket(id)["status"])
	})

	t.Run("a console proposal does not count the agent as holding anything", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Mine", "status": "preparing"}))
		console.mustCall("propose_ready", jsonObject{"ticket_id": id})
		assert.Nil(t, rig.agentBody("console")["lease"])
	})

	t.Run("disabling the agent voids what it proposed", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Voided", "status": "preparing"}))
		pid := proposalID(t, console.mustCall("propose_ready", jsonObject{"ticket_id": id}))

		rig.mustBoard(http.StatusOK, http.MethodPatch, "/api/agents/"+console.id, jsonObject{"disabled": true})
		assert.Equal(t, "voided", rig.proposal(pid)["state"])
		assert.Equal(t, http.StatusConflict, rig.approve(pid).code)
		assert.Equal(t, "preparing", rig.ticket(id)["status"])
	})

	t.Run("an agent whose grants were revoked cannot have its proposals approved", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Revoked", "status": "preparing"}))
		pid := proposalID(t, console.mustCall("propose_ready", jsonObject{"ticket_id": id}))

		rig.mustBoard(http.StatusOK, http.MethodPost, "/api/agents/"+console.id+"/setup_script", nil)
		assert.Equal(t, http.StatusConflict, rig.approve(pid).code)
		assert.Equal(t, "voided", rig.proposal(pid)["state"])
		assert.Equal(t, "preparing", rig.ticket(id)["status"])
	})
}

func TestContractProposals_Limits(t *testing.T) {
	t.Run("a console can hold only so many proposals pending", func(t *testing.T) {
		t.Setenv("PROPOSAL_MAX_PENDING_PER_AGENT", "2")
		rig := newContractRig(t)
		console := rig.console("console")
		var ids []int64
		for i := range 3 {
			ids = append(ids, ticketID(t, rig.openTicket(jsonObject{"title": fmt.Sprintf("T%d", i), "status": "preparing"})))
		}
		first := proposalID(t, console.mustCall("propose_ready", jsonObject{"ticket_id": ids[0]}))
		console.mustCall("propose_ready", jsonObject{"ticket_id": ids[1]})

		res := console.call("propose_ready", jsonObject{"ticket_id": ids[2]})
		assert.True(t, res.isError)
		assert.Contains(t, res.text, "too many pending proposals")
		assert.Len(t, rig.proposals("pending"), 2)

		// A decided one frees a place.
		require.Equal(t, http.StatusOK, rig.reject(first, "").code)
		console.mustCall("propose_ready", jsonObject{"ticket_id": ids[2]})
		assert.Len(t, rig.proposals("pending"), 2)
	})

	t.Run("a console can propose only so fast", func(t *testing.T) {
		t.Setenv("PROPOSAL_RATE_PER_MINUTE", "2")
		rig := newContractRig(t)
		console := rig.console("console")
		var ids []int64
		for i := range 3 {
			ids = append(ids, ticketID(t, rig.openTicket(jsonObject{"title": fmt.Sprintf("T%d", i), "status": "preparing"})))
		}
		console.mustCall("propose_ready", jsonObject{"ticket_id": ids[0]})
		console.mustCall("propose_ready", jsonObject{"ticket_id": ids[1]})
		res := console.call("propose_ready", jsonObject{"ticket_id": ids[2]})
		assert.True(t, res.isError)
		assert.Contains(t, res.text, "proposing too fast")
		assert.Len(t, rig.proposals(""), 2)
	})

	t.Run("a new proposal of the same kind on a ticket supersedes the pending one", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		id, _ := rig.discussionTicket("Answered twice")
		old := proposalID(t, console.mustCall("propose_discussion_answer", jsonObject{"ticket_id": id, "body": "First thought."}))
		latest := proposalID(t, console.mustCall("propose_discussion_answer", jsonObject{"ticket_id": id, "body": "Second thought."}))

		assert.Equal(t, "withdrawn", rig.proposal(old)["state"])
		assert.Equal(t, "superseded by a newer proposal", rig.proposal(old)["outcome"])
		assert.Equal(t, http.StatusConflict, rig.approve(old).code)
		require.Equal(t, http.StatusOK, rig.approve(latest).code)
		assert.Contains(t, rig.commentBodies(id)[len(rig.commentBodies(id))-1], "Second thought.")
	})

	t.Run("a blank answer and an unknown ticket are refused", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		id, _ := rig.discussionTicket("Long answer")
		res := console.call("propose_discussion_answer", jsonObject{"ticket_id": id, "body": "   "})
		assert.True(t, res.isError)
		assert.Contains(t, res.text, "an answer needs text")
		missing := console.call("propose_discussion_answer", jsonObject{"ticket_id": 9999, "body": "Hi."})
		assert.True(t, missing.isError)
		assert.Contains(t, missing.text, "ticket not found")
		assert.Empty(t, rig.proposals(""))
	})
}

func TestContractProposals_ConcurrentDecisions(t *testing.T) {
	t.Parallel()
	rig := newContractRig(t)
	console := rig.console("console")
	id, _ := rig.discussionTicket("Decided twice at once")
	pid := proposalID(t, console.mustCall("propose_discussion_answer", jsonObject{"ticket_id": id, "body": "Once."}))
	commentsBefore := len(rig.commentBodies(id))

	// Two approvals and a rejection are released together; whichever the api
	// takes first wins, and the others must find it decided.
	decide := []func() *boardResponse{
		func() *boardResponse { return rig.approve(pid) },
		func() *boardResponse { return rig.approve(pid) },
		func() *boardResponse { return rig.reject(pid, "No.") },
	}
	codes := make([]int, len(decide))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, d := range decide {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes[i] = d().code
		}()
	}
	close(start)
	wg.Wait()

	assert.Equal(t, 1, countOf(codes, http.StatusOK), "exactly one decision wins: %v", codes)
	assert.Equal(t, 2, countOf(codes, http.StatusConflict), "the others are told it is decided: %v", codes)
	final := rig.proposal(pid)["state"]
	assert.Contains(t, []any{"approved", "rejected"}, final)
	if final == "approved" {
		assert.Len(t, rig.commentBodies(id), commentsBefore+1, "the answer is posted once")
		assert.Equal(t, "open", rig.ticket(id)["status"])
	} else {
		assert.Len(t, rig.commentBodies(id), commentsBefore)
		assert.Equal(t, "discussion", rig.ticket(id)["status"])
	}
}

func countOf(codes []int, code int) int {
	n := 0
	for _, c := range codes {
		if c == code {
			n++
		}
	}
	return n
}

func TestContractProposals_WhoMayDecide(t *testing.T) {
	t.Parallel()
	t.Run("another tenant's person cannot see, approve or reject a proposal", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Ours", "status": "preparing"}))
		pid := proposalID(t, console.mustCall("propose_ready", jsonObject{"ticket_id": id}))

		_, token := rig.boot.signedInUser(t)
		as := func(method, path string, body string) *response {
			return do(t, &request{method: method, url: rig.boot.base + path, token: token, body: body})
		}
		listed := as(http.MethodGet, "/api/proposals", "")
		require.Equal(t, http.StatusOK, listed.code, listed.body)
		assert.JSONEq(t, `{"proposals":[]}`, listed.body, "they see none of ours")
		assert.Equal(t, http.StatusNotFound, as(http.MethodGet, "/api/proposals/"+pid, "").code)
		assert.Equal(t, http.StatusNotFound, as(http.MethodPost, "/api/proposals/"+pid+"/approve", "").code)
		assert.Equal(t, http.StatusNotFound, as(http.MethodPost, "/api/proposals/"+pid+"/reject", `{}`).code)

		assert.Equal(t, "pending", rig.proposal(pid)["state"], "untouched")
		assert.Equal(t, "preparing", rig.ticket(id)["status"])
	})

	t.Run("an agent token cannot list or decide on proposals", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Ours", "status": "preparing"}))
		pid := proposalID(t, console.mustCall("propose_ready", jsonObject{"ticket_id": id}))

		consoleID := uuid.MustParse(console.id)
		token := rig.boot.agentTokenFor(t, rig.user, &oauth.Decision{Approve: true, AgentID: &consoleID})
		for _, call := range []struct{ method, path, body string }{
			{http.MethodGet, "/api/proposals", ""},
			{http.MethodGet, "/api/proposals/" + pid, ""},
			{http.MethodPost, "/api/proposals/" + pid + "/approve", ""},
			{http.MethodPost, "/api/proposals/" + pid + "/reject", `{}`},
			{http.MethodPost, "/api/proposals/approve_all", `{"kind":"ready"}`},
			{http.MethodPost, "/api/proposals/reject_all", `{}`},
		} {
			resp := do(t, &request{method: call.method, url: rig.boot.base + call.path, token: token, body: call.body})
			assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, resp.code, "%s %s: %s", call.method, call.path, resp.body)
		}
		assert.Equal(t, "pending", rig.proposal(pid)["state"])
		assert.Equal(t, "preparing", rig.ticket(id)["status"])
	})

	t.Run("a worker cannot propose", func(t *testing.T) {
		rig := newContractRig(t)
		w := rig.worker()
		res := w.call("propose_ready", jsonObject{"ticket_id": 1})
		assert.True(t, res.isError)
		assert.Contains(t, res.text, "only a console agent")
	})
}

// proposalClock is the clock the api's proposals expire by, which a test
// moves instead of waiting.
type proposalClock struct {
	mu  sync.Mutex
	now time.Time
}

func newProposalClock() *proposalClock { return &proposalClock{now: time.Now()} }

func (c *proposalClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *proposalClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// revokeGrantsQuietly ends the agent's grants the way a token reuse does:
// nothing tells the proposals, so only the check made at approval can notice.
// (The board's own revokes void the agent's proposals at once, which is
// covered above.)
func (r *contractRig) revokeGrantsQuietly(agentID string) {
	r.t.Helper()
	ctx := authn.NewUserContext(context.Background(), r.user)
	oauthSvc := r.boot.consent()
	grants, err := oauthSvc.Grants(ctx, r.user)
	require.NoError(r.t, err)
	revoked := 0
	for i := range grants {
		if grants[i].AgentID.String() == agentID {
			require.NoError(r.t, oauthSvc.RevokeGrant(ctx, r.user, grants[i].ID))
			revoked++
		}
	}
	require.NotZero(r.t, revoked, "the agent held no grant")
}

// readOnlySession is an MCP session for the agent on a read-only client's
// token: a client an operator makes (there is no board route for it), signed
// in at the real token endpoint.
func (r *contractRig) readOnlySession(agentID string) *contractAgent {
	r.t.Helper()
	ctx := authn.NewUserContext(context.Background(), r.user)
	agent, err := uuid.Parse(agentID)
	require.NoError(r.t, err)
	admin := oauth.NewClientAdmin(oauth.NewPostgresStore(r.boot.appDB), oauth.ResourceFor(r.boot.cfg.PublicBaseURL), nil, slog.DiscardHandler)
	clientID, secret, err := admin.CreateReadOnlyClient(ctx, r.user.TenantID, agent, "ci-read-only")
	require.NoError(r.t, err)

	resp := do(r.t, &request{method: http.MethodPost, url: r.boot.base + "/oauth/token",
		header: map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
		body: url.Values{"grant_type": {"client_credentials"}, "client_id": {clientID}, "client_secret": {secret},
			"resource": {oauth.ResourceFor(r.boot.cfg.PublicBaseURL)}}.Encode()})
	require.Equal(r.t, http.StatusOK, resp.code, resp.body)
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	require.NoError(r.t, json.Unmarshal([]byte(resp.body), &tok))

	client := mcp.NewClient(&mcp.Implementation{Name: "contract-read-only", Version: "0.0.1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: r.boot.base + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token: tok.AccessToken}},
	}, nil)
	require.NoError(r.t, err)
	r.t.Cleanup(func() { _ = session.Close() })
	return &contractAgent{rig: r, name: "read-only", id: agentID, session: session}
}

func TestContractProposals_GrantRecheckAtApproval(t *testing.T) {
	t.Parallel()
	t.Run("a console that lost its grant without the proposals being voided cannot have them approved", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Lost grant", "status": "preparing"}))
		pid := proposalID(t, console.mustCall("propose_ready", jsonObject{"ticket_id": id}))

		rig.revokeGrantsQuietly(console.id)
		assert.Equal(t, "pending", rig.proposal(pid)["state"], "nothing voided it")

		resp := rig.approve(pid)
		assert.Equal(t, http.StatusConflict, resp.code, resp.raw)
		assert.Contains(t, resp.raw, "no live grant")
		assert.Equal(t, "voided", rig.proposal(pid)["state"])
		assert.Equal(t, "preparing", rig.ticket(id)["status"])
	})

	t.Run("a read-only grant is not a live grant to propose by", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Read only left", "status": "preparing"}))
		pid := proposalID(t, console.mustCall("propose_ready", jsonObject{"ticket_id": id}))

		// The read-only client is made after the full grants are revoked, so
		// the agent is left holding only that.
		rig.revokeGrantsQuietly(console.id)
		rig.readOnlySession(console.id)

		resp := rig.approve(pid)
		assert.Equal(t, http.StatusConflict, resp.code, resp.raw)
		assert.Contains(t, resp.raw, "no live grant", "only a read-only grant is left")
		assert.Equal(t, "preparing", rig.ticket(id)["status"])
	})
}

func TestContractProposals_ReadOnlyGrant(t *testing.T) {
	t.Parallel()
	rig := newContractRig(t)
	console := rig.console("console")
	id := ticketID(t, rig.openTicket(jsonObject{"title": "Look but do not touch", "status": "preparing"}))
	reader := rig.readOnlySession(console.id)

	t.Run("it may read a ticket", func(t *testing.T) {
		res := reader.call("ticket_get", jsonObject{"ticket_id": id})
		assert.False(t, res.isError, res.text)
	})

	t.Run("it may not propose, list or withdraw", func(t *testing.T) {
		for tool, args := range map[string]jsonObject{
			"propose_ready":             {"ticket_id": id},
			"propose_discussion_answer": {"ticket_id": id, "body": "x"},
			"propose_status":            {"ticket_id": id, "status": "parked", "reason": "x"},
			"propose_review_decision":   {"ticket_id": id, "decision": "approve"},
			"propose_ticket_edit":       {"ticket_id": id, "base_revision": 0, "body": "x"},
			"list_proposals":            nil,
			"withdraw_proposal":         {"id": "00000000-0000-4000-8000-000000000000"},
		} {
			_, err := reader.session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
			require.Error(t, err, tool)
			assert.Contains(t, err.Error(), "read-only", tool)
		}
		assert.Empty(t, rig.proposals(""))
		assert.Equal(t, "preparing", rig.ticket(id)["status"])
	})

	t.Run("it cannot take work", func(t *testing.T) {
		_, err := reader.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "ticket_next"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read-only")
	})
}

func TestContractProposals_Expiry(t *testing.T) {
	t.Parallel()
	// The default window, which the clock steps across.
	const ttl = 7 * 24 * time.Hour

	t.Run("an expired proposal is not pending and cannot be approved", func(t *testing.T) {
		clock := newProposalClock()
		rig := newContractRigWith(t, &bootOptions{proposalClock: clock.Now})
		console := rig.console("console")
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Too late", "status": "preparing"}))
		pid := proposalID(t, console.mustCall("propose_ready", jsonObject{"ticket_id": id}))
		require.Len(t, rig.proposals("pending"), 1)
		require.Equal(t, "pending", console.mustCall("list_proposals", nil).structured["proposals"].([]any)[0].(map[string]any)["state"])

		clock.Advance(ttl + time.Second)

		assert.Empty(t, rig.proposals("pending"))
		assert.Len(t, rig.proposals("expired"), 1)
		assert.Equal(t, "expired", rig.proposal(pid)["state"])

		resp := rig.approve(pid)
		assert.Equal(t, http.StatusConflict, resp.code, resp.raw)
		assert.Contains(t, resp.raw, "it is expired")
		assert.Equal(t, http.StatusConflict, rig.reject(pid, "").code, "nor can it be rejected")
		assert.Equal(t, "preparing", rig.ticket(id)["status"])

		listed := console.mustCall("list_proposals", nil).structured["proposals"].([]any)
		require.Len(t, listed, 1)
		assert.Equal(t, "expired", listed[0].(map[string]any)["state"], "the console is not told it is still waiting")
		assert.True(t, console.call("withdraw_proposal", jsonObject{"id": pid}).isError, "nor can it be withdrawn")
	})

	t.Run("approve all leaves an expired proposal alone", func(t *testing.T) {
		clock := newProposalClock()
		rig := newContractRigWith(t, &bootOptions{proposalClock: clock.Now})
		console := rig.console("console")
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Too late", "status": "preparing"}))
		console.mustCall("propose_ready", jsonObject{"ticket_id": id})
		clock.Advance(ttl + time.Second)

		resp := rig.mustBoard(http.StatusOK, http.MethodPost, "/api/proposals/approve_all", jsonObject{"kind": "ready"})
		assert.Empty(t, resp.obj(t)["results"])
		assert.Equal(t, "preparing", rig.ticket(id)["status"])
	})

	t.Run("a proposal can still be approved just inside its window", func(t *testing.T) {
		clock := newProposalClock()
		rig := newContractRigWith(t, &bootOptions{proposalClock: clock.Now})
		console := rig.console("console")
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Just in time", "status": "preparing"}))
		pid := proposalID(t, console.mustCall("propose_ready", jsonObject{"ticket_id": id}))
		clock.Advance(ttl - time.Second)

		require.Equal(t, http.StatusOK, rig.approve(pid).code)
		assert.Equal(t, "open", rig.ticket(id)["status"])
	})

	t.Run("the console may propose again once the old one lapsed", func(t *testing.T) {
		clock := newProposalClock()
		rig := newContractRigWith(t, &bootOptions{proposalClock: clock.Now})
		console := rig.console("console")
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Again", "status": "preparing"}))
		old := proposalID(t, console.mustCall("propose_ready", jsonObject{"ticket_id": id}))
		clock.Advance(ttl + time.Second)

		fresh := proposalID(t, console.mustCall("propose_ready", jsonObject{"ticket_id": id}))
		assert.NotEqual(t, old, fresh)
		assert.Equal(t, "expired", rig.proposal(old)["state"])
		require.Equal(t, http.StatusOK, rig.approve(fresh).code)
		assert.Equal(t, "open", rig.ticket(id)["status"])
	})
}

func TestContractProposals_ApprovingASettledProposal(t *testing.T) {
	t.Parallel()
	t.Run("an approved proposal is not applied again and says it is decided", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		id, _ := rig.discussionTicket("Answer once")
		pid := proposalID(t, console.mustCall("propose_discussion_answer", jsonObject{"ticket_id": id, "body": "Once."}))
		require.Equal(t, http.StatusOK, rig.approve(pid).code)
		comments := rig.commentBodies(id)

		resp := rig.approve(pid)
		assert.Equal(t, http.StatusConflict, resp.code, resp.raw)
		assert.Contains(t, resp.raw, "it is approved")
		assert.Equal(t, comments, rig.commentBodies(id), "the answer lands once")
	})

	t.Run("a withdrawn proposal says it was withdrawn", func(t *testing.T) {
		rig := newContractRig(t)
		console := rig.console("console")
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Withdrawn", "status": "preparing"}))
		pid := proposalID(t, console.mustCall("propose_ready", jsonObject{"ticket_id": id}))
		console.mustCall("withdraw_proposal", jsonObject{"id": pid})

		resp := rig.approve(pid)
		assert.Equal(t, http.StatusConflict, resp.code, resp.raw)
		assert.Contains(t, resp.raw, "it is withdrawn")
		assert.Equal(t, "preparing", rig.ticket(id)["status"])
	})
}
