package main

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Discussion as an agent and the person see it: a ticket that is a direction
// rather than a spec, the plan and question an agent hands over, the person's
// answer, who gets the ticket back, the handoff the next agent resumes from,
// and the person's words reaching an agent that is working.
//
// The affinity window itself (30 minutes) is a constant the tests cannot
// shorten, so its expiry is not covered; what is covered is who is kept from
// the ticket while it runs and the one thing that ends it early, the asker
// being disabled.

// commentOnBoard is the signed-in person's comment on a ticket; fields are
// CreateTicketCommentBody's.
func (r *contractRig) commentOnBoard(id int64, fields jsonObject) *boardResponse {
	r.t.Helper()
	return r.mustBoard(http.StatusCreated, http.MethodPost, fmt.Sprintf("/api/tickets/%d/comments", id), fields)
}

// comments are a ticket's comments as the board lists them, oldest first.
func (r *contractRig) comments(id int64) []jsonObject {
	r.t.Helper()
	listed := r.mustBoard(http.StatusOK, http.MethodGet, fmt.Sprintf("/api/tickets/%d/comments", id), nil).obj(r.t)
	return objects(r.t, listed["comments"])
}

// contexts are the handoffs left on a ticket, oldest first.
func (r *contractRig) contexts(id int64) []jsonObject {
	r.t.Helper()
	listed := r.mustBoard(http.StatusOK, http.MethodGet, fmt.Sprintf("/api/tickets/%d/contexts", id), nil).obj(r.t)
	return objects(r.t, listed["contexts"])
}

func objects(t *testing.T, v any) []jsonObject {
	t.Helper()
	items, ok := v.([]any)
	require.True(t, ok, "not a list: %v", v)
	out := make([]jsonObject, 0, len(items))
	for _, item := range items {
		out = append(out, item.(map[string]any))
	}
	return out
}

// ticketGetRevision is the revision ticket_get shows for the ticket.
func ticketGetRevision(res *toolResult) int {
	return int(res.structured["revision"].(float64))
}

func TestContractDiscussion_NeedsDiscussion(t *testing.T) {
	t.Parallel()
	t.Run("is not built: the claim says so, and done and review are refused until the plan is agreed", func(t *testing.T) {
		rig := newContractRig(t)
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Rework the queue", "body": "Make it better somehow.", "needs_discussion": true}))
		w := rig.worker()

		granted := w.mustCall("ticket_next", nil)
		assert.Equal(t, id, grantedID(granted))
		assert.Contains(t, granted.text, "NEEDS DISCUSSION")
		assert.Equal(t, true, granted.structured["ticket"].(map[string]any)["needs_discussion"])
		rig.assertGolden("discussion/ticket_next_needs_discussion", granted)

		// A refusal is an answer, not a failure: retrying cannot help.
		review := w.call("ticket_finish", jsonObject{"pr_url": "https://github.com/acme/repo/pull/3", "handoff": "x", "request_review": true, "reason": "a person should read it"})
		assert.False(t, review.isError)
		assert.Contains(t, review.text, "NEEDS DISCUSSION")

		assert.Equal(t, "in_progress", rig.ticket(id)["status"], "the refused exits changed nothing")
		assert.NotNil(t, rig.agentBody(w.name)["lease"], "and the agent still holds it")
	})

	t.Run("a person setting the flag on a ticket being worked takes it back and tells the agent to stop", func(t *testing.T) {
		rig := newContractRig(t)
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Planned as a spec"}))
		w := rig.worker()
		require.Equal(t, id, grantedID(w.mustCall("ticket_next", nil)))

		answer := rig.commentOnBoard(id, jsonObject{"body": "Actually, plan this first.", "needs_discussion": true}).obj(t)
		assert.Equal(t, "open", answer["ticket"].(jsonObject)["status"])
		assert.Equal(t, true, answer["ticket"].(jsonObject)["needs_discussion"])
		assert.Nil(t, rig.agentBody(w.name)["lease"])

		stopped := w.call("ticket_edit", jsonObject{"revision": 0, "title": "Too late"})
		assert.True(t, stopped.isError)
		assert.Contains(t, stopped.text, "stop working on it")
	})

	t.Run("clearing the flag withdraws nothing from an agent that is planning", func(t *testing.T) {
		rig := newContractRig(t)
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Plan first", "needs_discussion": true}))
		w := rig.worker()
		require.Equal(t, id, grantedID(w.mustCall("ticket_next", nil)))

		rig.commentOnBoard(id, jsonObject{"body": "Plan approved, go ahead.", "needs_discussion": false})

		assert.Equal(t, "in_progress", rig.ticket(id)["status"])
		assert.Equal(t, false, rig.ticket(id)["needs_discussion"])
		assert.NotNil(t, rig.agentBody(w.name)["lease"])
		w.mustComplete(jsonObject{"pr_url": "https://github.com/acme/repo/pull/1", "handoff": "built it"})
		assert.Equal(t, "done", rig.ticket(id)["status"])
	})
}

func TestContractDiscussion_Discuss(t *testing.T) {
	t.Parallel()
	t.Run("hands the ticket to the person with the question, and leaves the handoff for whoever resumes", func(t *testing.T) {
		rig := newContractRig(t)
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Rework the queue", "body": "Make it better somehow.", "needs_discussion": true}))
		w := rig.worker()
		require.Equal(t, id, grantedID(w.mustCall("ticket_next", nil)))

		discussed := w.mustCall("ticket_discuss", jsonObject{
			"question": "Do you want the queue ordered by age or by priority?",
			"handoff":  "Branch ticket-1-queue is not started. Plan: add an ordering column, then a migration.",
		})
		rig.assertGolden("discussion/ticket_discuss_result", discussed)

		tk := rig.ticket(id)
		assert.Equal(t, "discussion", tk["status"])
		assert.Nil(t, rig.agentBody(w.name)["lease"], "the agent no longer holds it")
		assert.Equal(t, w.id, tk["resume_agent_id"], "the board records who asked")
		assert.Nil(t, tk["affinity_until"], "but nobody is kept from it until the person has answered")

		comments := rig.comments(id)
		require.Len(t, comments, 1)
		assert.Equal(t, w.id, comments[0]["author_agent_id"])
		assert.Equal(t, "Do you want the queue ordered by age or by priority?", comments[0]["body"])

		contexts := rig.contexts(id)
		require.Len(t, contexts, 1)
		assert.Equal(t, w.id, contexts[0]["agent_id"])
		assert.Equal(t, "Branch ticket-1-queue is not started. Plan: add an ordering column, then a migration.", contexts[0]["body"])

		// Nobody can take a ticket that is waiting on the person.
		other := rig.worker()
		assert.Zero(t, grantedID(other.mustCall("ticket_next", nil)))
		assert.Zero(t, grantedID(other.mustCall("ticket_next", jsonObject{"ticket_id": id})))
	})

	t.Run("another agent's comment is not an answer", func(t *testing.T) {
		rig := newContractRig(t)
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Ask twice"}))
		w := rig.worker()
		other := rig.worker()
		require.Equal(t, id, grantedID(w.mustCall("ticket_next", nil)))
		w.mustCall("ticket_discuss", jsonObject{"question": "Which?", "handoff": "Nothing built."})

		other.mustCall("ticket_comment", jsonObject{"ticket_id": id, "body": "I would like to know too."})

		assert.Equal(t, "discussion", rig.ticket(id)["status"])
		assert.Len(t, rig.comments(id), 2, "the comment is recorded")
		assert.Zero(t, grantedID(other.mustCall("ticket_next", nil)))
	})

	t.Run("is refused for an agent that holds nothing", func(t *testing.T) {
		rig := newContractRig(t)
		w := rig.worker()
		res := w.call("ticket_discuss", jsonObject{"question": "Which?", "handoff": "none"})
		assert.True(t, res.isError)
	})
}

func TestContractDiscussion_ResumeAffinity(t *testing.T) {
	t.Parallel()
	// discussed is a rig with an urgent ticket the asker has handed over, a
	// lower-priority ticket beside it that anyone may take, and the person's
	// answer posted. Without affinity the urgent one would be next for anybody.
	discussed := func(t *testing.T) (rig *contractRig, id, other int64, asker *contractAgent) {
		rig = newContractRig(t)
		id = ticketID(t, rig.openTicket(jsonObject{"title": "Needs a plan", "priority": "urgent", "needs_discussion": true}))
		other = ticketID(t, rig.openTicket(jsonObject{"title": "Anyone's", "priority": "low"}))
		asker = rig.worker()
		require.Equal(t, id, grantedID(asker.mustCall("ticket_next", nil)))
		asker.mustCall("ticket_discuss", jsonObject{"question": "Which way?", "handoff": "Nothing built."})
		return rig, id, other, asker
	}

	t.Run("the answer puts it back in the queue with the asker first in line", func(t *testing.T) {
		rig, id, _, asker := discussed(t)

		rig.commentOnBoard(id, jsonObject{"body": "The first way."})

		tk := rig.ticket(id)
		assert.Equal(t, "open", tk["status"])
		assert.Equal(t, asker.id, tk["resume_agent_id"])
		assert.NotNil(t, tk["affinity_until"])
		assert.Equal(t, false, tk["needs_discussion"], "the question cleared the flag, so the claim after the answer is a working one")
	})

	t.Run("another agent is kept from it, by the queue and by asking", func(t *testing.T) {
		rig, id, other, _ := discussed(t)
		rig.commentOnBoard(id, jsonObject{"body": "The first way."})
		second := rig.worker()
		third := rig.worker()

		assert.Zero(t, grantedID(third.mustCall("ticket_next", jsonObject{"ticket_id": id})))
		assert.Equal(t, "open", rig.ticket(id)["status"])
		assert.Equal(t, other, grantedID(second.mustCall("ticket_next", nil)),
			"the queue goes past the answered ticket to the next one")
		assert.Zero(t, grantedID(third.mustCall("ticket_next", nil)), "and has nothing else for the third agent")
	})

	t.Run("the asker gets it, with the thread and the handoff to resume from", func(t *testing.T) {
		rig, id, _, asker := discussed(t)
		rig.commentOnBoard(id, jsonObject{"body": "The first way."})
		other := rig.worker()
		other.mustCall("ticket_next", nil)

		got := asker.mustCall("ticket_next", nil)
		assert.Equal(t, id, grantedID(got))
		assert.NotContains(t, got.text, "NEEDS DISCUSSION")
		tk := rig.ticket(id)
		assert.Equal(t, "in_progress", tk["status"])
		assert.Nil(t, tk["resume_agent_id"], "taking it ends the affinity")
		assert.Nil(t, tk["affinity_until"])

		resumed := asker.mustCall("ticket_get", jsonObject{"ticket_id": id})
		assert.Equal(t, "Nothing built.", resumed.structured["handoff"])
		require.Len(t, resumed.structured["comments"], 2)
		rig.assertGolden("discussion/ticket_get_resumed", resumed)
	})

	t.Run("after the answer the asker can finish the ticket", func(t *testing.T) {
		rig, id, _, asker := discussed(t)
		rig.commentOnBoard(id, jsonObject{"body": "The first way."})
		require.Equal(t, id, grantedID(asker.mustCall("ticket_next", nil)))

		asker.mustComplete(jsonObject{"pr_url": "https://github.com/acme/repo/pull/1", "handoff": "built the first way"})
		assert.Equal(t, "done", rig.ticket(id)["status"])
	})

	t.Run("an answer that sets the flag again sends the asker round another planning pass", func(t *testing.T) {
		rig, id, _, asker := discussed(t)

		answered := rig.commentOnBoard(id, jsonObject{"body": "Not yet: plan the second way too.", "needs_discussion": true}).obj(t)

		assert.Equal(t, "open", answered["ticket"].(jsonObject)["status"])
		assert.Equal(t, true, answered["ticket"].(jsonObject)["needs_discussion"])
		assert.Equal(t, asker.id, answered["ticket"].(jsonObject)["resume_agent_id"])
		got := asker.mustCall("ticket_next", nil)
		assert.Equal(t, id, grantedID(got))
		assert.Contains(t, got.text, "NEEDS DISCUSSION")
		assert.Contains(t, asker.complete(jsonObject{"pr_url": "https://github.com/acme/repo/pull/1", "handoff": "x"}).text, "NEEDS DISCUSSION")
	})

	t.Run("once the window lapses another agent may take it, by asking and by the queue", func(t *testing.T) {
		// A zero window is already lapsed when the answer lands, so the
		// expiry is exercised without waiting for a real one.
		for _, byID := range []bool{true, false} {
			rig := newContractRigWith(t, &bootOptions{resumeAffinity: "0s"})
			id := ticketID(t, rig.openTicket(jsonObject{"title": "Needs a plan", "needs_discussion": true}))
			asker := rig.worker()
			require.Equal(t, id, grantedID(asker.mustCall("ticket_next", nil)))
			asker.mustCall("ticket_discuss", jsonObject{"question": "Which way?", "handoff": "Nothing built."})
			rig.commentOnBoard(id, jsonObject{"body": "The first way."})
			assert.Equal(t, asker.id, rig.ticket(id)["resume_agent_id"], "the asker is still recorded")

			var args jsonObject
			if byID {
				args = jsonObject{"ticket_id": id}
			}
			assert.Equal(t, id, grantedID(rig.worker().mustCall("ticket_next", args)), "directed=%v", byID)
			assert.Equal(t, "in_progress", rig.ticket(id)["status"])
		}
	})

	t.Run("a disabled asker no longer holds it back", func(t *testing.T) {
		rig, id, _, asker := discussed(t)
		rig.commentOnBoard(id, jsonObject{"body": "The first way."})
		rig.mustBoard(http.StatusOK, http.MethodPatch, "/api/agents/"+asker.id, jsonObject{"disabled": true})

		second := rig.worker()
		assert.Equal(t, id, grantedID(second.mustCall("ticket_next", nil)))
	})

	t.Run("a person's comment on a ticket nobody was waiting on holds nothing back", func(t *testing.T) {
		rig := newContractRig(t)
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Just open"}))

		rig.commentOnBoard(id, jsonObject{"body": "A thought."})

		tk := rig.ticket(id)
		assert.Equal(t, "open", tk["status"])
		assert.Nil(t, tk["resume_agent_id"])
		assert.Equal(t, id, grantedID(rig.worker().mustCall("ticket_next", nil)))
	})
}

func TestContractDiscussion_HandoffReclaim(t *testing.T) {
	t.Parallel()
	t.Run("a different agent resumes from the handoff, and the contexts carry over", func(t *testing.T) {
		rig := newContractRig(t)
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Passed along", "needs_discussion": true}))
		first := rig.worker()
		require.Equal(t, id, grantedID(first.mustCall("ticket_next", nil)))
		first.mustCall("ticket_discuss", jsonObject{"question": "Which way?", "handoff": "First agent: branch ticket-1-a, commit abc."})
		rig.commentOnBoard(id, jsonObject{"body": "The first way.", "needs_discussion": false})
		rig.mustBoard(http.StatusOK, http.MethodPatch, "/api/agents/"+first.id, jsonObject{"disabled": true})

		second := rig.worker()
		require.Equal(t, id, grantedID(second.mustCall("ticket_next", nil)))
		seen := second.mustCall("ticket_get", jsonObject{"ticket_id": id})
		assert.Equal(t, "First agent: branch ticket-1-a, commit abc.", seen.structured["handoff"])
		assert.Contains(t, seen.text, "Handoff from "+first.name+" -- resume from here:")
		rig.assertGolden("discussion/ticket_get_handoff", seen)

		// The next hand-over adds to the record; the last one is what to resume from.
		second.mustCall("ticket_discuss", jsonObject{"question": "And the second?", "handoff": "Second agent: finished the first way."})
		contexts := rig.contexts(id)
		require.Len(t, contexts, 2)
		assert.Equal(t, first.id, contexts[0]["agent_id"])
		assert.Equal(t, second.id, contexts[1]["agent_id"])
		third := rig.worker()
		rig.commentOnBoard(id, jsonObject{"body": "Yes."})
		rig.mustBoard(http.StatusOK, http.MethodPatch, "/api/agents/"+second.id, jsonObject{"disabled": true})
		require.Equal(t, id, grantedID(third.mustCall("ticket_next", nil)))
		resumed := third.mustCall("ticket_get", jsonObject{"ticket_id": id})
		assert.Equal(t, "Second agent: finished the first way.", resumed.structured["handoff"])
		assert.Len(t, rig.contexts(id), 2)
	})

	t.Run("a released ticket is open to any agent again", func(t *testing.T) {
		rig := newContractRig(t)
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Given back"}))
		first := rig.worker()
		require.Equal(t, id, grantedID(first.mustCall("ticket_next", nil)))

		released := first.mustCall("ticket_release", jsonObject{"reason": "Out of my depth."})
		assert.Equal(t, "Released #1 (now open).", released.text)
		assert.Equal(t, "open", rig.ticket(id)["status"])
		assert.Nil(t, rig.agentBody(first.name)["lease"])

		second := rig.worker()
		assert.Equal(t, id, grantedID(second.mustCall("ticket_next", nil)), "no affinity: a release owes the ticket to nobody")
	})
}

func TestContractDiscussion_ReleaseReason(t *testing.T) {
	t.Parallel()
	t.Run("the reason is the releasing agent's comment, and a blank one posts nothing", func(t *testing.T) {
		rig := newContractRig(t)
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Given back"}))
		w := rig.worker()
		require.Equal(t, id, grantedID(w.mustCall("ticket_next", nil)))
		w.mustCall("ticket_release", jsonObject{"reason": "Blocked on credentials."})

		comments := rig.comments(id)
		require.Len(t, comments, 1)
		assert.Equal(t, "Blocked on credentials.", comments[0]["body"])

		again := rig.worker()
		require.Equal(t, id, grantedID(again.mustCall("ticket_next", nil)))
		again.mustCall("ticket_release", jsonObject{"reason": " \n "})
		assert.Len(t, rig.comments(id), 1, "a blank reason adds nothing")
	})

	t.Run("blocked_on with a reason is one comment", func(t *testing.T) {
		rig := newContractRig(t)
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Waits"}))
		blocker := ticketID(t, rig.openTicket(jsonObject{"title": "Land first"}))
		w := rig.worker()
		require.Equal(t, id, grantedID(w.mustCall("ticket_next", nil)))
		w.mustCall("ticket_release", jsonObject{"reason": "needs the other first", "blocked_on": blocker})

		comments := rig.comments(id)
		require.Len(t, comments, 1)
		body := comments[0]["body"].(string)
		assert.Contains(t, body, "Blocked on #")
		assert.Contains(t, body, "needs the other first")
	})

	t.Run("a release handoff is what the next claimant sees, and none leaves the earlier one", func(t *testing.T) {
		rig := newContractRig(t)
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Passed on"}))
		first := rig.worker()
		require.Equal(t, id, grantedID(first.mustCall("ticket_next", nil)))
		first.mustCall("ticket_release", jsonObject{"reason": "stuck", "handoff": "Branch ticket-1-x, commit abc."})

		second := rig.worker()
		require.Equal(t, id, grantedID(second.mustCall("ticket_next", nil)))
		seen := second.mustCall("ticket_get", jsonObject{"ticket_id": id})
		assert.Equal(t, "Branch ticket-1-x, commit abc.", seen.structured["handoff"])

		second.mustCall("ticket_release", jsonObject{"reason": "also stuck"})
		third := rig.worker()
		require.Equal(t, id, grantedID(third.mustCall("ticket_next", nil)))
		kept := third.mustCall("ticket_get", jsonObject{"ticket_id": id})
		assert.Equal(t, "Branch ticket-1-x, commit abc.", kept.structured["handoff"], "a release without one hides nothing")
		assert.Len(t, rig.contexts(id), 1)
	})
}

func TestContractDiscussion_Prepare(t *testing.T) {
	t.Parallel()
	t.Run("sends the ticket back to preparing until the person opens it again", func(t *testing.T) {
		rig := newContractRig(t)
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Too big"}))
		first := rig.worker()
		require.Equal(t, id, grantedID(first.mustCall("ticket_next", nil)))

		prepared := first.mustCall("ticket_prepare", jsonObject{"reason": "This is three tickets, not one."})
		rig.assertGolden("discussion/ticket_prepare_result", prepared)

		assert.Equal(t, "preparing", rig.ticket(id)["status"])
		assert.Nil(t, rig.agentBody(first.name)["lease"])
		comments := rig.comments(id)
		require.Len(t, comments, 1)
		assert.Equal(t, first.id, comments[0]["author_agent_id"])
		assert.Equal(t, "This is three tickets, not one.", comments[0]["body"])

		second := rig.worker()
		assert.Zero(t, grantedID(second.mustCall("ticket_next", nil)))
		assert.Zero(t, grantedID(second.mustCall("ticket_next", jsonObject{"ticket_id": id})))

		rig.mustBoard(http.StatusOK, http.MethodPost, fmt.Sprintf("/api/tickets/%d/ready", id), nil)
		assert.Equal(t, "open", rig.ticket(id)["status"])
		assert.Nil(t, rig.ticket(id)["resume_agent_id"], "the person reshaped it, so nobody is owed it")
		assert.Equal(t, id, grantedID(second.mustCall("ticket_next", nil)))
	})

	t.Run("is refused for an agent that holds nothing", func(t *testing.T) {
		rig := newContractRig(t)
		res := rig.worker().call("ticket_prepare", jsonObject{"reason": "no"})
		assert.True(t, res.isError)
	})
}

func TestContractDiscussion_Edit(t *testing.T) {
	t.Parallel()
	t.Run("an agent edits the ticket it holds against the revision it read", func(t *testing.T) {
		rig := newContractRig(t)
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Old title", "body": "Old body."}))
		w := rig.worker()
		require.Equal(t, id, grantedID(w.mustCall("ticket_next", nil)))
		before := ticketGetRevision(w.mustCall("ticket_get", jsonObject{"ticket_id": id}))

		edited := w.mustCall("ticket_edit", jsonObject{"revision": before, "title": "New title", "body": "New body."})
		assert.Equal(t, fmt.Sprintf("Edited #%d (revision %d).", id, before+1), edited.text)

		tk := rig.ticket(id)
		assert.Equal(t, "New title", tk["title"])
		assert.Equal(t, "New body.", tk["body"])
		assert.Equal(t, float64(before+1), tk["revision"])
		revisions := rig.mustBoard(http.StatusOK, http.MethodGet, fmt.Sprintf("/api/tickets/%d/revisions", id), nil).obj(t)["revisions"].([]any)
		assert.Len(t, revisions, 1, "the edit is kept in the ticket's history")

		seen := w.mustCall("ticket_get", jsonObject{"ticket_id": id})
		assert.Equal(t, "New title", seen.structured["title"])
		assert.Len(t, seen.structured["edits"], 1)
	})

	t.Run("an edit made against a revision the person has since moved on from is refused", func(t *testing.T) {
		rig := newContractRig(t)
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Shared"}))
		w := rig.worker()
		require.Equal(t, id, grantedID(w.mustCall("ticket_next", nil)))
		read := ticketGetRevision(w.mustCall("ticket_get", jsonObject{"ticket_id": id}))

		rig.mustBoard(http.StatusOK, http.MethodPost, fmt.Sprintf("/api/tickets/%d/revisions", id),
			jsonObject{"title": "Renamed by a person"})

		stale := w.call("ticket_edit", jsonObject{"revision": read, "title": "Mine"})
		assert.True(t, stale.isError)
		assert.Contains(t, stale.text, "Read it again with ticket_get")
		assert.Equal(t, "Renamed by a person", rig.ticket(id)["title"])

		current := ticketGetRevision(w.mustCall("ticket_get", jsonObject{"ticket_id": id}))
		w.mustCall("ticket_edit", jsonObject{"revision": current, "title": "Mine"})
		assert.Equal(t, "Mine", rig.ticket(id)["title"])
	})

	t.Run("restating the current text changes nothing", func(t *testing.T) {
		rig := newContractRig(t)
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Same"}))
		w := rig.worker()
		require.Equal(t, id, grantedID(w.mustCall("ticket_next", nil)))
		read := ticketGetRevision(w.mustCall("ticket_get", jsonObject{"ticket_id": id}))

		res := w.mustCall("ticket_edit", jsonObject{"revision": read, "title": "Same"})
		assert.Contains(t, res.text, "Nothing changed")
		assert.Equal(t, float64(read), rig.ticket(id)["revision"])
	})

	t.Run("an agent cannot edit a ticket it does not hold", func(t *testing.T) {
		rig := newContractRig(t)
		rig.openTicket(jsonObject{"title": "Not yours"})
		res := rig.worker().call("ticket_edit", jsonObject{"revision": 0, "title": "Mine"})
		assert.True(t, res.isError)
		assert.Equal(t, "Not yours", rig.ticket(1)["title"])
	})
}

func TestContractDiscussion_HumanMessages(t *testing.T) {
	t.Parallel()
	t.Run("a message waits for the agent's next call, arrives once, and is then delivered", func(t *testing.T) {
		rig := newContractRig(t)
		w := rig.worker()
		rig.mustBoard(http.StatusCreated, http.MethodPost, "/api/agents/"+w.id+"/messages", jsonObject{"body": "Please wrap up the current step."})

		pending := rig.mustBoard(http.StatusOK, http.MethodGet, "/api/agent_messages", nil).obj(t)["messages"].([]any)
		require.Len(t, pending, 1)
		assert.Equal(t, "pending", pending[0].(map[string]any)["state"])

		heard := w.mustCall("checkin", nil)
		assert.Contains(t, heard.text, "MESSAGE FROM THE HUMAN:")
		assert.Contains(t, heard.text, "Please wrap up the current step.")
		assert.Equal(t, w.name, heard.structured["agent"], "the call still answers what it was asked")

		delivered := rig.mustBoard(http.StatusOK, http.MethodGet, "/api/agent_messages", nil).obj(t)["messages"].([]any)
		require.Len(t, delivered, 1)
		assert.Equal(t, "delivered", delivered[0].(map[string]any)["state"])

		again := w.mustCall("checkin", nil)
		assert.NotContains(t, again.text, "MESSAGE FROM THE HUMAN")
	})

	t.Run("reaches an agent on a call that fails too", func(t *testing.T) {
		rig := newContractRig(t)
		w := rig.worker()
		rig.mustBoard(http.StatusCreated, http.MethodPost, "/api/agents/"+w.id+"/messages", jsonObject{"body": "Stop and read this."})

		failed := w.call("ticket_finish", jsonObject{"pr_url": "https://github.com/acme/repo/pull/1", "handoff": "nothing held"})
		assert.True(t, failed.isError)
		assert.Contains(t, failed.text, "Stop and read this.")
	})

	t.Run("a person's comment on the ticket an agent holds is delivered as a comment on it", func(t *testing.T) {
		rig := newContractRig(t)
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Being worked"}))
		w := rig.worker()
		require.Equal(t, id, grantedID(w.mustCall("ticket_next", nil)))

		rig.commentOnBoard(id, jsonObject{"body": "Use the second approach."})

		heard := w.mustCall("checkin", nil)
		assert.Contains(t, heard.text, "MESSAGE FROM THE HUMAN:")
		assert.Contains(t, heard.text, fmt.Sprintf("commented on ticket #%d, which you hold", id))
		assert.Contains(t, heard.text, "Use the second approach.")
		assert.Equal(t, "in_progress", rig.ticket(id)["status"], "a comment on a held ticket does not move it")
	})

	t.Run("is refused for an empty body and for a disabled agent", func(t *testing.T) {
		rig := newContractRig(t)
		w := rig.worker()

		rig.mustBoard(http.StatusUnprocessableEntity, http.MethodPost, "/api/agents/"+w.id+"/messages", jsonObject{"body": ""})
		rig.mustBoard(http.StatusOK, http.MethodPatch, "/api/agents/"+w.id, jsonObject{"disabled": true})
		rig.mustBoard(http.StatusConflict, http.MethodPost, "/api/agents/"+w.id+"/messages", jsonObject{"body": "Hello?"})
	})
}
