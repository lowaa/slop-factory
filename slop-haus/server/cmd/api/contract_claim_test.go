package main

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
)

// The claim lifecycle as an agent and the board see it: ticket_next in its
// forms, what the agent is told, and a ticket's way from open to done.

// grantedID is the id of the ticket a ticket_next granted, or 0 if it granted
// none.
func grantedID(res *toolResult) int64 {
	granted, ok := res.structured["ticket"].(map[string]any)
	if !ok {
		return 0
	}
	return int64(granted["id"].(float64))
}

func TestContractClaim_PlainClaim(t *testing.T) {
	t.Parallel()
	t.Run("grants the highest priority ticket, oldest first among equals", func(t *testing.T) {
		rig := newContractRig(t)
		rig.openTicket(jsonObject{"title": "First normal"})
		rig.openTicket(jsonObject{"title": "Second normal"})
		rig.openTicket(jsonObject{"title": "Urgent", "priority": "urgent"})
		rig.openTicket(jsonObject{"title": "Low", "priority": "low"})

		var order []int64
		for range 4 {
			w := rig.worker()
			order = append(order, grantedID(w.mustCall("ticket_next", nil)))
		}
		assert.Equal(t, []int64{3, 1, 2, 4}, order)
	})

	t.Run("takes the ticket off the queue and puts it with the agent", func(t *testing.T) {
		rig := newContractRig(t)
		tk := rig.openTicket(jsonObject{"title": "Claim me"})
		id := ticketID(t, tk)
		w := rig.worker()

		got := w.mustCall("ticket_next", nil)
		assert.Equal(t, id, grantedID(got))
		assert.Equal(t, "in_progress", got.structured["ticket"].(map[string]any)["status"])

		assert.Equal(t, "in_progress", rig.ticket(id)["status"])
		lease, ok := rig.agentBody(w.name)["lease"].(map[string]any)
		require.True(t, ok, "the board shows the agent holding the ticket")
		assert.Equal(t, float64(id), lease["ticket_id"])
		assert.NotEmpty(t, lease["expires_at"])

		checkin := w.mustCall("checkin", nil)
		assert.Equal(t, float64(id), checkin.structured["held_ticket"])

		// Nobody else can have it, by queue or by asking for it.
		other := rig.worker()
		assert.Zero(t, grantedID(other.mustCall("ticket_next", nil)))
		directed := other.mustCall("ticket_next", jsonObject{"ticket_id": id})
		assert.Zero(t, grantedID(directed))
		assert.Contains(t, directed.text, fmt.Sprintf("Ticket #%d was not granted: it is held by another agent", id))
		assert.Equal(t, float64(id), rig.agentBody(w.name)["lease"].(map[string]any)["ticket_id"])
	})

	t.Run("an empty queue is not an error", func(t *testing.T) {
		rig := newContractRig(t)
		rig.openTicket(jsonObject{"title": "Not ready", "status": "preparing"})
		w := rig.worker()

		got := w.mustCall("ticket_next", nil)
		assert.Zero(t, grantedID(got))
		assert.Equal(t, "The queue is empty.", got.text)
		rig.assertGolden("claim/ticket_next_queue_empty", got)

		// A directed claim says why instead.
		notReady := ticketID(t, rig.openTicket(jsonObject{"title": "Still preparing", "status": "preparing"}))
		directed := w.mustCall("ticket_next", jsonObject{"ticket_id": notReady})
		assert.Zero(t, grantedID(directed))
		assert.Contains(t, directed.text, fmt.Sprintf("Ticket #%d was not granted: it is still Preparing", notReady))
		missing := w.mustCall("ticket_next", jsonObject{"ticket_id": 987654})
		assert.Equal(t, "Ticket #987654 was not granted: no such ticket.", missing.text)
		assert.Nil(t, rig.agentBody(w.name)["lease"])
	})

	t.Run("refuses an agent that already holds a ticket", func(t *testing.T) {
		rig := newContractRig(t)
		first := ticketID(t, rig.openTicket(jsonObject{"title": "First"}))
		second := ticketID(t, rig.openTicket(jsonObject{"title": "Second"}))
		w := rig.worker()
		require.Equal(t, first, grantedID(w.mustCall("ticket_next", nil)))

		again := w.call("ticket_next", nil)
		assert.True(t, again.isError)
		assert.Contains(t, again.text, "already holds a ticket")
		assert.NotContains(t, again.text, "ticket:")
		directed := w.call("ticket_next", jsonObject{"ticket_id": second})
		assert.True(t, directed.isError)
		assert.Contains(t, directed.text, "already holds a ticket")

		assert.Equal(t, "in_progress", rig.ticket(first)["status"])
		assert.Equal(t, "open", rig.ticket(second)["status"], "the refused claim took nothing")
	})
}

func TestContractClaim_DirectedClaim(t *testing.T) {
	t.Parallel()
	t.Run("grants the ticket asked for, whatever its place in the queue", func(t *testing.T) {
		rig := newContractRig(t)
		rig.openTicket(jsonObject{"title": "Urgent", "priority": "urgent"})
		low := ticketID(t, rig.openTicket(jsonObject{"title": "Low", "priority": "low"}))
		w := rig.worker()

		got := w.mustCall("ticket_next", jsonObject{"ticket_id": low})
		assert.Equal(t, low, grantedID(got))
		assert.Equal(t, "in_progress", rig.ticket(low)["status"])
		assert.Equal(t, "open", rig.ticket(1)["status"], "the one ahead of it in the queue is untouched")
	})

	t.Run("grants nothing for a ticket that is held, not ready or unknown", func(t *testing.T) {
		rig := newContractRig(t)
		held := ticketID(t, rig.openTicket(jsonObject{"title": "Held"}))
		preparing := ticketID(t, rig.openTicket(jsonObject{"title": "Preparing", "status": "preparing"}))
		holder := rig.worker()
		require.Equal(t, held, grantedID(holder.mustCall("ticket_next", nil)))

		other := rig.worker()
		for name, id := range map[string]int64{"held": held, "preparing": preparing, "unknown": 9999} {
			assert.Zero(t, grantedID(other.mustCall("ticket_next", jsonObject{"ticket_id": id})), name)
		}
		assert.Nil(t, rig.agentBody(other.name)["lease"])
		assert.Equal(t, "preparing", rig.ticket(preparing)["status"])
		assert.Equal(t, float64(held), rig.agentBody(holder.name)["lease"].(map[string]any)["ticket_id"])
	})
}

// The subtests share a rig and run in order: each takes the tickets the next
// one has to find already claimed or still waiting.
func TestContractClaim_RoleRouting(t *testing.T) {
	t.Parallel()
	rig := newContractRig(t)
	deploy, review := rig.role("deploy"), rig.role("review")
	both := ticketID(t, rig.openTicket(jsonObject{"title": "Needs both", "role_ids": []string{deploy, review}, "role_match": "all"}))
	either := ticketID(t, rig.openTicket(jsonObject{"title": "Needs either", "role_ids": []string{deploy, review}}))

	t.Run("a ticket's roles show on the board and to the agent", func(t *testing.T) {
		tk := rig.ticket(either)
		assert.Equal(t, "any", tk["role_match"])
		assert.Len(t, tk["required_roles"], 2)
		assert.Equal(t, true, tk["no_eligible_agent"], "no agent has the roles yet")
		assert.Equal(t, "all", rig.ticket(both)["role_match"])
	})

	t.Run("an agent without the roles is left out of the queue and told why when it asks", func(t *testing.T) {
		plain := rig.worker()
		assert.Zero(t, grantedID(plain.mustCall("ticket_next", nil)), "the queue holds only tickets it cannot take")

		for name, c := range map[string]struct {
			id     int64
			golden string
		}{"all": {both, "claim/ticket_next_roles_all"}, "any": {either, "claim/ticket_next_roles_any"}} {
			got := plain.mustCall("ticket_next", jsonObject{"ticket_id": c.id})
			assert.Zero(t, grantedID(got), name)
			assert.NotEmpty(t, got.structured["blocked"], name)
			rig.assertGolden(c.golden, got)
		}
		assert.Nil(t, rig.agentBody(plain.name)["lease"])
		assert.Equal(t, "open", rig.ticket(either)["status"])
	})

	t.Run("match any: one of the roles is enough", func(t *testing.T) {
		deployer := rig.worker("deploy")
		got := deployer.mustCall("ticket_next", nil)
		assert.Equal(t, either, grantedID(got), "it skips the ticket needing both and takes the one it qualifies for")
		assert.Equal(t, []any{"deploy", "review"}, got.structured["ticket"].(map[string]any)["roles"])
	})

	t.Run("match all: only an agent with every role", func(t *testing.T) {
		deployer := rig.worker("deploy")
		assert.Zero(t, grantedID(deployer.mustCall("ticket_next", nil)))
		refused := deployer.mustCall("ticket_next", jsonObject{"ticket_id": both})
		assert.Zero(t, grantedID(refused))
		assert.Contains(t, refused.text, "needs all of the roles deploy, review")

		both2 := rig.worker("deploy", "review")
		assert.Equal(t, both, grantedID(both2.mustCall("ticket_next", nil)))
		assert.Equal(t, false, rig.ticket(both)["no_eligible_agent"])
	})
}

// What an agent reads about the ticket it has been given and the ones it asks
// about: the texts it acts on.
func TestContractClaim_TextsAnAgentActsOn(t *testing.T) {
	t.Parallel()
	rig := newContractRig(t)
	tk := rig.openTicket(jsonObject{
		"title": "Scoped work", "body": "Build the thing.\n\nOut of scope: the other thing.",
		"priority": "high", "paths": []jsonObject{{"path": "server/internal/a/"}, {"path": "server/cmd/b.go"}},
	})
	id := ticketID(t, tk)
	w := rig.worker()

	t.Run("ticket_get of a ticket in the queue", func(t *testing.T) {
		rig.assertGolden("claim/ticket_get_open", w.mustCall("ticket_get", jsonObject{"ticket_id": id}))
	})
	t.Run("ticket_next granting it", func(t *testing.T) {
		rig.assertGolden("claim/ticket_next_granted", w.mustCall("ticket_next", nil))
	})
	t.Run("ticket_get of the ticket the agent holds", func(t *testing.T) {
		rig.assertGolden("claim/ticket_get_held", w.mustCall("ticket_get", jsonObject{"ticket_id": id}))
	})
	t.Run("ticket_get of a ticket that does not exist", func(t *testing.T) {
		missing := w.call("ticket_get", jsonObject{"ticket_id": 9999})
		assert.True(t, missing.isError)
	})
}

// The whole way a ticket goes when nothing goes wrong, with each stop checked
// on the board and in the reports.
func TestContractClaim_OpenToDone(t *testing.T) {
	t.Parallel()
	rig := newContractRig(t)
	tk := rig.openTicket(jsonObject{"title": "Happy path", "body": "Do it."})
	id := ticketID(t, tk)
	assert.Equal(t, "open", tk["status"])
	assert.Nil(t, tk["closed_at"])
	assert.Equal(t, rig.user.ID.String(), tk["created_by_user_id"])

	column := func(status string) jsonObject {
		for _, c := range rig.mustBoard(http.StatusOK, http.MethodGet, "/api/board", nil).obj(t)["columns"].([]any) {
			if c := c.(map[string]any); c["status"] == status {
				return c
			}
		}
		t.Fatalf("no %s column on the board", status)
		return nil
	}
	report := func() jsonObject {
		return rig.mustBoard(http.StatusOK, http.MethodGet, "/api/reports?days=7", nil).obj(t)
	}
	reportedState := func(r jsonObject, state string) jsonObject {
		for _, s := range r["states"].([]any) {
			if s := s.(map[string]any); s["state"] == state {
				return s
			}
		}
		t.Fatalf("no %s state in the report", state)
		return nil
	}
	waitingIn := func(status string) []any {
		return rig.mustBoard(http.StatusOK, http.MethodGet, "/api/reports/waiting?status="+status, nil).obj(t)["tickets"].([]any)
	}

	assert.Equal(t, float64(1), column("open")["count"])
	assert.Equal(t, float64(0), column("done")["count"])

	w := rig.worker()
	require.Equal(t, id, grantedID(w.mustCall("ticket_next", nil)))

	held := rig.ticket(id)
	assert.Equal(t, "in_progress", held["status"])
	assert.Nil(t, held["closed_at"])
	assert.Equal(t, float64(1), column("in_progress")["count"])
	assert.Equal(t, float64(0), column("open")["count"])
	lease := rig.agentBody(w.name)["lease"].(map[string]any)
	assert.Equal(t, float64(id), lease["ticket_id"])
	assert.NotEmpty(t, lease["acquired_at"])
	assert.NotEmpty(t, lease["expires_at"])

	// The reports see the ticket where it is now, and how long it spent in
	// each earlier state.
	inFlight := report()
	assert.Equal(t, float64(1), reportedState(inFlight, "open")["stays"])
	assert.Equal(t, float64(1), reportedState(inFlight, "in_progress")["ongoing"])
	assert.Equal(t, float64(0), inFlight["lead"].(map[string]any)["done"])
	waiting := waitingIn("in_progress")
	require.Len(t, waiting, 1)
	assert.Equal(t, float64(id), waiting[0].(map[string]any)["id"])

	w.mustComplete(jsonObject{"handoff": "Built it."})

	closed := rig.ticket(id)
	assert.Equal(t, "done", closed["status"])
	assert.NotNil(t, closed["closed_at"])
	assert.Nil(t, closed["pr_url"])
	comments := rig.mustBoard(http.StatusOK, http.MethodGet, "/api/tickets/1/comments", nil).obj(t)["comments"].([]any)
	require.Len(t, comments, 1, "the summary is the ticket's last word")
	assert.Equal(t, "Built it.", comments[0].(map[string]any)["body"])
	assert.Equal(t, w.id, comments[0].(map[string]any)["author_agent_id"])
	assert.Nil(t, rig.agentBody(w.name)["lease"], "the agent holds nothing now")
	assert.Equal(t, float64(1), column("done")["count"])
	assert.Equal(t, float64(0), column("in_progress")["count"])
	assert.Empty(t, waitingIn("in_progress"))

	finished := report()
	lead := finished["lead"].(map[string]any)
	assert.Equal(t, float64(1), lead["done"])
	assert.Equal(t, float64(1), lead["claimed_done"])
	assert.NotNil(t, lead["created_to_done"])
	assert.Equal(t, float64(1), reportedState(finished, "open")["stays"])
	assert.Equal(t, float64(1), reportedState(finished, "in_progress")["stays"])
	assert.Equal(t, float64(0), reportedState(finished, "in_progress")["ongoing"])
	assert.Equal(t, float64(1), finished["throughput"].([]any)[0].(map[string]any)["done"])

	// It is closed for good: nothing to claim, nothing to close again.
	assert.Zero(t, grantedID(w.mustCall("ticket_next", jsonObject{"ticket_id": id})))
	again := w.call("ticket_finish", jsonObject{"pr_url": "https://github.com/acme/repo/pull/1", "handoff": "Again."})
	assert.True(t, again.isError)
	assert.Contains(t, again.text, "holds no active lease")
	assert.NotContains(t, again.text, "ticket:", "the package prefix is for logs")
	assert.NotContains(t, again.text, "your last ticket", "nothing was revoked")
}

func TestContractClaim_BaseCommit(t *testing.T) {
	t.Parallel()
	const pinned = "dddddddddddddddddddddddddddddddddddddddd"
	repo := codehost.Repo{Owner: "acme", Name: "repo"}

	t.Run("the first claim pins main's head, a later claim hands out the same one", func(t *testing.T) {
		rig := newContractRig(t)
		rig.declareRepo(flowRepo)
		rig.host.configure(func(f *codehosttest.Fake) {
			f.HeadOf = map[codehost.Repo]*codehost.BranchHead{repo: {Branch: "main", SHA: pinned}}
		})
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Pin me", "paths": []jsonObject{{"path": "server/pin/"}}}))

		first := rig.worker()
		got := first.mustCall("ticket_next", nil)
		assert.Equal(t, pinned, got.structured["ticket"].(map[string]any)["base_commit"])
		assert.Contains(t, got.text, "Your branch ticket-1-a1 is ready (made at base commit "+pinned+")")
		assert.NotContains(t, got.text, "switch -c")
		rig.assertGolden("claim/ticket_next_pinned", got)
		assert.Equal(t, pinned, rig.ticket(id)["base_commit"], "the board shows it")

		first.mustCall("ticket_release", jsonObject{"reason": "handing over"})
		rig.host.configure(func(f *codehosttest.Fake) {
			f.HeadOf[repo] = &codehost.BranchHead{Branch: "main", SHA: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}
		})
		again := rig.worker().mustCall("ticket_next", nil)
		assert.Equal(t, id, grantedID(again))
		assert.Equal(t, pinned, again.structured["ticket"].(map[string]any)["base_commit"], "main moved, the pin did not")
	})

	t.Run("a host that cannot be read still grants the ticket, unpinned", func(t *testing.T) {
		rig := newContractRig(t)
		rig.declareRepo(flowRepo)
		rig.host.configure(func(f *codehosttest.Fake) { f.HeadErr = codehost.ErrUnreachable })
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Host down", "paths": []jsonObject{{"path": "server/pin/"}}}))

		got := rig.worker().mustCall("ticket_next", nil)

		assert.Equal(t, id, grantedID(got))
		assert.Contains(t, got.text, "Base commit: not pinned, so slop-haus did not make your branch. Branch from origin/main.")
		assert.NotContains(t, got.structured["ticket"], "base_commit")
		assert.Nil(t, rig.ticket(id)["base_commit"])
	})
}
