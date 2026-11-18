package main

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scopes, park and cancel, blockers and the status history, as agents over MCP
// and the person on the board see them.

func scopePaths(paths ...string) []jsonObject {
	out := make([]jsonObject, 0, len(paths))
	for _, p := range paths {
		out = append(out, jsonObject{"path": p})
	}
	return out
}

// pathsOf are the scope paths the board shows on a ticket.
func pathsOf(tk jsonObject) []string {
	out := []string{}
	for _, p := range tk["paths"].([]any) {
		out = append(out, p.(map[string]any)["path"].(string))
	}
	return out
}

func (r *contractRig) patchStatus(id int64, status string) *boardResponse {
	r.t.Helper()
	return r.boardDo(http.MethodPatch, fmt.Sprintf("/api/tickets/%d", id), jsonObject{"status": status})
}

func (r *contractRig) mustPatchStatus(id int64, status string) jsonObject {
	r.t.Helper()
	return r.mustBoard(http.StatusOK, http.MethodPatch, fmt.Sprintf("/api/tickets/%d", id), jsonObject{"status": status}).obj(r.t)
}

func (r *contractRig) addBlocker(id, blocker int64) *boardResponse {
	r.t.Helper()
	return r.boardDo(http.MethodPost, fmt.Sprintf("/api/tickets/%d/blockers", id), jsonObject{"blocker_id": blocker})
}

func (r *contractRig) removeBlocker(id, blocker int64) *boardResponse {
	r.t.Helper()
	return r.boardDo(http.MethodDelete, fmt.Sprintf("/api/tickets/%d/blockers/%d", id, blocker), nil)
}

// queueBlockage is what the board says the queue is waiting on, nil if it
// waits on no scope.
func (r *contractRig) queueBlockage() jsonObject {
	r.t.Helper()
	b := r.mustBoard(http.StatusOK, http.MethodGet, "/api/queue/blockage", nil).obj(r.t)["blockage"]
	if b == nil {
		return nil
	}
	return b.(map[string]any)
}

func (r *contractRig) commentBodies(id int64) []string {
	r.t.Helper()
	listed := r.mustBoard(http.StatusOK, http.MethodGet, fmt.Sprintf("/api/tickets/%d/comments", id), nil).obj(r.t)["comments"].([]any)
	out := make([]string, 0, len(listed))
	for _, c := range listed {
		out = append(out, c.(map[string]any)["body"].(string))
	}
	return out
}

func TestContractScopes_ClaimConflict(t *testing.T) {
	t.Parallel()
	t.Run("a claim that collides with a held scope is refused, and the board says why", func(t *testing.T) {
		rig := newContractRig(t)
		held := ticketID(t, rig.openTicket(jsonObject{"title": "Holds the directory", "paths": scopePaths("srv/a/")}))
		waiting := ticketID(t, rig.openTicket(jsonObject{"title": "Wants a file inside", "paths": scopePaths("srv/a/x.go")}))
		holder := rig.worker()
		require.Equal(t, held, grantedID(holder.mustCall("ticket_next", nil)))
		assert.Nil(t, rig.queueBlockage(), "nothing is waiting yet")

		other := rig.worker()
		refused := other.mustCall("ticket_next", nil)
		assert.Zero(t, grantedID(refused))
		assert.NotEmpty(t, refused.structured["blocked"])
		rig.assertGolden("scopes/ticket_next_scope_held", refused)
		directed := other.mustCall("ticket_next", jsonObject{"ticket_id": waiting})
		assert.Zero(t, grantedID(directed))
		assert.Equal(t, refused.text, directed.text, "asking for it by number changes nothing")

		assert.Equal(t, "open", rig.ticket(waiting)["status"])
		assert.Nil(t, rig.agentBody(other.name)["lease"])
		blockage := rig.queueBlockage()
		require.NotNil(t, blockage)
		assert.Equal(t, float64(waiting), blockage["ticket_id"])
		assert.Equal(t, float64(held), blockage["holder_ticket_id"])
		assert.Equal(t, "in_progress", blockage["holder_status"])
		assert.Equal(t, holder.name, blockage["holder_agent_name"])
		assert.Equal(t, "srv/a/", blockage["path"])

		// Finishing the holder frees the scope.
		holder.mustComplete(jsonObject{"pr_url": "https://github.com/acme/repo/pull/1", "handoff": "Done."})
		assert.Nil(t, rig.queueBlockage())
		assert.Equal(t, waiting, grantedID(other.mustCall("ticket_next", nil)))
	})

	t.Run("a ticket in the way does not hold up the ones behind it", func(t *testing.T) {
		rig := newContractRig(t)
		held := ticketID(t, rig.openTicket(jsonObject{"title": "Holds", "paths": scopePaths("srv/a/")}))
		rig.openTicket(jsonObject{"title": "Collides", "paths": scopePaths("srv/a/")})
		clear := ticketID(t, rig.openTicket(jsonObject{"title": "Elsewhere", "paths": scopePaths("srv/b/")}))
		require.Equal(t, held, grantedID(rig.worker().mustCall("ticket_next", nil)))

		assert.Equal(t, clear, grantedID(rig.worker().mustCall("ticket_next", nil)))
		assert.Equal(t, "open", rig.ticket(2)["status"])
	})

	t.Run("a ticket with no scope collides with nothing", func(t *testing.T) {
		rig := newContractRig(t)
		first := ticketID(t, rig.openTicket(jsonObject{"title": "Scoped", "paths": scopePaths("srv/a/")}))
		second := ticketID(t, rig.openTicket(jsonObject{"title": "Unscoped"}))
		assert.Equal(t, first, grantedID(rig.worker().mustCall("ticket_next", nil)))
		assert.Equal(t, second, grantedID(rig.worker().mustCall("ticket_next", nil)))
	})

	t.Run("releasing the ticket frees its scope", func(t *testing.T) {
		rig := newContractRig(t)
		held := ticketID(t, rig.openTicket(jsonObject{"title": "Holds", "paths": scopePaths("srv/a/")}))
		waiting := ticketID(t, rig.openTicket(jsonObject{"title": "Waits", "paths": scopePaths("srv/a/")}))
		holder := rig.worker()
		require.Equal(t, held, grantedID(holder.mustCall("ticket_next", nil)))
		other := rig.worker()
		require.Zero(t, grantedID(other.mustCall("ticket_next", nil)))

		released := holder.mustCall("ticket_release", jsonObject{"reason": "Not for me."})
		assert.Equal(t, "Released #1 (now open).", released.text)
		// Asked for by number, so the released ticket ahead of it in the queue
		// is not what the answer turns on.
		assert.Equal(t, waiting, grantedID(other.mustCall("ticket_next", jsonObject{"ticket_id": waiting})))
		assert.Equal(t, "open", rig.ticket(held)["status"])
	})
}

func TestContractScopes_Extend(t *testing.T) {
	t.Parallel()
	rig := newContractRig(t)
	a := ticketID(t, rig.openTicket(jsonObject{"title": "A", "paths": scopePaths("srv/a/")}))
	b := ticketID(t, rig.openTicket(jsonObject{"title": "B", "paths": scopePaths("srv/b/")}))
	wa, wb := rig.worker(), rig.worker()
	require.Equal(t, a, grantedID(wa.mustCall("ticket_next", nil)))
	require.Equal(t, b, grantedID(wb.mustCall("ticket_next", nil)))

	t.Run("a path nobody holds is granted and the board shows it", func(t *testing.T) {
		got := wa.mustCall("ticket_scope_extend", jsonObject{"paths": []string{"srv/c/"}})
		assert.Equal(t, true, got.structured["granted"])
		assert.Equal(t, "Scope extended.", got.text)
		assert.Equal(t, []string{"srv/a/", "srv/c/"}, pathsOf(rig.ticket(a)))
	})

	t.Run("a path another ticket holds is denied and changes nothing", func(t *testing.T) {
		// srv/c/x.go is inside what A now holds.
		denied := wb.mustCall("ticket_scope_extend", jsonObject{"paths": []string{"srv/d/", "srv/c/x.go"}})
		assert.Equal(t, false, denied.structured["granted"])
		assert.NotEmpty(t, denied.structured["denied"])
		rig.assertGolden("scopes/scope_extend_denied", denied)
		assert.Equal(t, []string{"srv/b/"}, pathsOf(rig.ticket(b)), "none of the paths asked for was added, srv/d/ included")
		assert.Equal(t, "in_progress", rig.ticket(b)["status"])
		assert.Equal(t, float64(b), rig.agentBody(wb.name)["lease"].(map[string]any)["ticket_id"], "a denial does not cost the agent its ticket")
	})

	t.Run("the denied path is granted once its holder lets go", func(t *testing.T) {
		wa.mustCall("ticket_scope_narrow", jsonObject{"remove": []string{"srv/c/"}})
		granted := wb.mustCall("ticket_scope_extend", jsonObject{"paths": []string{"srv/c/x.go"}})
		assert.Equal(t, true, granted.structured["granted"])
		assert.Equal(t, []string{"srv/b/", "srv/c/x.go"}, pathsOf(rig.ticket(b)))
	})

	t.Run("extending needs a ticket in hand", func(t *testing.T) {
		idle := rig.worker()
		res := idle.call("ticket_scope_extend", jsonObject{"paths": []string{"srv/e/"}})
		assert.True(t, res.isError)
	})
}

func TestContractScopes_Narrow(t *testing.T) {
	t.Parallel()
	rig := newContractRig(t)
	held := ticketID(t, rig.openTicket(jsonObject{"title": "Wide", "paths": scopePaths("srv/a/", "srv/b/", "srv/c.go")}))
	waiting := ticketID(t, rig.openTicket(jsonObject{"title": "Wants a.go", "paths": scopePaths("srv/a/x.go")}))
	w := rig.worker()
	require.Equal(t, held, grantedID(w.mustCall("ticket_next", nil)))
	other := rig.worker()
	require.Zero(t, grantedID(other.mustCall("ticket_next", nil)))

	t.Run("refuses to leave the ticket with no paths", func(t *testing.T) {
		res := w.call("ticket_scope_narrow", jsonObject{"remove": []string{"srv/a/", "srv/b/", "srv/c.go"}})
		assert.True(t, res.isError)
		assert.Contains(t, res.text, "no paths")
		assert.Equal(t, []string{"srv/a/", "srv/b/", "srv/c.go"}, pathsOf(rig.ticket(held)))
	})

	t.Run("refuses a path the ticket does not hold", func(t *testing.T) {
		res := w.call("ticket_scope_narrow", jsonObject{"remove": []string{"srv/zzz/"}})
		assert.True(t, res.isError)
		assert.Contains(t, res.text, "not in the ticket's scope")
		assert.Equal(t, []string{"srv/a/", "srv/b/", "srv/c.go"}, pathsOf(rig.ticket(held)))
	})

	t.Run("keep holds on to a narrower path inside a directory given up", func(t *testing.T) {
		got := w.mustCall("ticket_scope_narrow", jsonObject{"remove": []string{"srv/a/"}, "keep": []string{"srv/a/keep/"}})
		assert.Equal(t, []any{"srv/a/"}, got.structured["removed"])
		assert.Equal(t, []any{"srv/a/keep/"}, got.structured["kept"])
		assert.Equal(t, []any{"srv/a/keep/", "srv/b/", "srv/c.go"}, got.structured["remaining"])
		rig.assertGolden("scopes/scope_narrow_keep", got)
		assert.Equal(t, []string{"srv/a/keep/", "srv/b/", "srv/c.go"}, pathsOf(rig.ticket(held)))
	})

	t.Run("what was given up can be claimed by another worker", func(t *testing.T) {
		assert.Equal(t, waiting, grantedID(other.mustCall("ticket_next", nil)))
		assert.Nil(t, rig.queueBlockage())
	})

	t.Run("what was kept is still held", func(t *testing.T) {
		third := rig.worker()
		tk := ticketID(t, rig.openTicket(jsonObject{"title": "Wants the kept path", "paths": scopePaths("srv/a/keep/y.go")}))
		assert.Zero(t, grantedID(third.mustCall("ticket_next", jsonObject{"ticket_id": tk})))
		assert.Equal(t, float64(tk), rig.queueBlockage()["ticket_id"])
	})
}

func TestContractScopes_HeldInReview(t *testing.T) {
	t.Parallel()
	rig := newContractRig(t)
	held := ticketID(t, rig.openTicket(jsonObject{"title": "In review", "paths": scopePaths("srv/a/")}))
	waiting := ticketID(t, rig.openTicket(jsonObject{"title": "Waits", "paths": scopePaths("srv/a/x.go")}))
	author := rig.worker()
	require.Equal(t, held, grantedID(author.mustCall("ticket_next", nil)))
	author.mustCall("ticket_finish", jsonObject{"pr_url": "https://github.com/acme/repo/pull/1", "handoff": "Pushed.", "request_review": true, "reason": "a person should read it"})
	require.Equal(t, "review", rig.ticket(held)["status"])
	other := rig.worker()

	t.Run("the scope stays held, whoever asks", func(t *testing.T) {
		assert.Zero(t, grantedID(other.mustCall("ticket_next", nil)))
		assert.Zero(t, grantedID(other.mustCall("ticket_next", jsonObject{"ticket_id": waiting})))
		blockage := rig.queueBlockage()
		require.NotNil(t, blockage)
		assert.Equal(t, "review", blockage["holder_status"])
		assert.Equal(t, float64(held), blockage["holder_ticket_id"])
		assert.Equal(t, "srv/a/", blockage["path"])
		assert.Nil(t, rig.agentBody(author.name)["lease"], "the author holds nothing, the ticket does the holding")
	})

	t.Run("another ticket cannot extend onto it", func(t *testing.T) {
		third := rig.worker()
		rig.openTicket(jsonObject{"title": "Elsewhere", "paths": scopePaths("srv/z/")})
		require.Equal(t, int64(3), grantedID(third.mustCall("ticket_next", nil)))
		denied := third.mustCall("ticket_scope_extend", jsonObject{"paths": []string{"srv/a/y.go"}})
		assert.Equal(t, false, denied.structured["granted"])
	})

	t.Run("a person closing it frees the scope", func(t *testing.T) {
		rig.mustPatchStatus(held, "cancelled")
		assert.Nil(t, rig.queueBlockage())
		assert.Equal(t, waiting, grantedID(other.mustCall("ticket_next", nil)))
	})
}

func TestContractPark(t *testing.T) {
	t.Parallel()
	rig := newContractRig(t)
	id := ticketID(t, rig.openTicket(jsonObject{"title": "Not now"}))
	later := ticketID(t, rig.openTicket(jsonObject{"title": "Next"}))

	parked := rig.mustPatchStatus(id, "parked")
	assert.Equal(t, "parked", parked["status"])

	w := rig.worker()
	t.Run("a parked ticket is never handed out", func(t *testing.T) {
		assert.Equal(t, later, grantedID(w.mustCall("ticket_next", nil)), "the queue goes past it")
		w.mustComplete(jsonObject{"pr_url": "https://github.com/acme/repo/pull/1", "handoff": "Done."})
		assert.Zero(t, grantedID(w.mustCall("ticket_next", nil)))
		assert.Zero(t, grantedID(w.mustCall("ticket_next", jsonObject{"ticket_id": id})), "nor when asked for by number")
		assert.Equal(t, "parked", rig.ticket(id)["status"])
	})

	t.Run("it is unparked to preparing, never into the queue", func(t *testing.T) {
		refused := rig.patchStatus(id, "open")
		assert.Equal(t, http.StatusConflict, refused.code)
		assert.Equal(t, "parked", rig.ticket(id)["status"])
		refusedReady := rig.boardDo(http.MethodPost, fmt.Sprintf("/api/tickets/%d/ready", id), nil)
		assert.GreaterOrEqual(t, refusedReady.code, 400, "ready is for a preparing ticket")
		assert.Equal(t, "parked", rig.ticket(id)["status"])

		assert.Equal(t, "preparing", rig.mustPatchStatus(id, "preparing")["status"])
		assert.Zero(t, grantedID(w.mustCall("ticket_next", nil)), "preparing is not claimable either")
		rig.mustBoard(http.StatusOK, http.MethodPost, fmt.Sprintf("/api/tickets/%d/ready", id), nil)
		assert.Equal(t, id, grantedID(w.mustCall("ticket_next", nil)))
	})

	t.Run("a ticket someone holds is not parked from under them", func(t *testing.T) {
		held := rig.patchStatus(id, "parked")
		assert.Equal(t, http.StatusConflict, held.code)
		assert.Equal(t, "in_progress", rig.ticket(id)["status"])
		assert.Equal(t, float64(id), rig.agentBody(w.name)["lease"].(map[string]any)["ticket_id"])
	})

	t.Run("parking is not finishing: what waits on a parked ticket keeps waiting", func(t *testing.T) {
		rig := newContractRig(t)
		parkedID := ticketID(t, rig.openTicket(jsonObject{"title": "Parked"}))
		dependent := ticketID(t, rig.openTicket(jsonObject{"title": "Depends"}))
		require.Equal(t, http.StatusOK, rig.addBlocker(dependent, parkedID).code)
		rig.mustPatchStatus(parkedID, "parked")
		assert.Equal(t, "blocked", rig.ticket(dependent)["status"])
		assert.Zero(t, grantedID(rig.worker().mustCall("ticket_next", nil)))
	})
}

func TestContractCancel(t *testing.T) {
	t.Parallel()
	t.Run("a cancelled ticket is closed and never handed out", func(t *testing.T) {
		rig := newContractRig(t)
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Dropped"}))
		cancelled := rig.mustPatchStatus(id, "cancelled")
		assert.Equal(t, "cancelled", cancelled["status"])
		assert.NotNil(t, cancelled["closed_at"])

		w := rig.worker()
		assert.Zero(t, grantedID(w.mustCall("ticket_next", nil)))
		assert.Zero(t, grantedID(w.mustCall("ticket_next", jsonObject{"ticket_id": id})))
		assert.Equal(t, "cancelled", rig.ticket(id)["status"])
	})

	t.Run("a worker holding it is told on its next call", func(t *testing.T) {
		rig := newContractRig(t)
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Dropped", "paths": scopePaths("srv/a/")}))
		behind := ticketID(t, rig.openTicket(jsonObject{"title": "Behind it", "paths": scopePaths("srv/a/")}))
		w := rig.worker()
		require.Equal(t, id, grantedID(w.mustCall("ticket_next", nil)))
		other := rig.worker()
		require.Zero(t, grantedID(other.mustCall("ticket_next", nil)))

		cancelled := rig.mustPatchStatus(id, "cancelled")
		assert.Equal(t, "cancelled", cancelled["status"])
		assert.NotNil(t, cancelled["closed_at"])
		assert.Nil(t, rig.agentBody(w.name)["lease"], "the board no longer shows the agent holding it")
		assert.Nil(t, rig.queueBlockage(), "its scope is free")

		revoked := w.call("ticket_finish", jsonObject{"pr_url": "https://github.com/acme/repo/pull/1", "handoff": "Finished anyway."})
		assert.True(t, revoked.isError)
		assert.Contains(t, revoked.text, "cancelled by a human")
		assert.Contains(t, revoked.text, "stop working on it")
		rig.assertGolden("scopes/lease_revoked_by_cancel", revoked)
		assert.Equal(t, "cancelled", rig.ticket(id)["status"], "the worker's late finish did not reopen or close it")
		assert.NotContains(t, rig.commentBodies(id), "Finished anyway.")

		// Told once; after that it is an agent holding nothing, free to take
		// other work.
		// A worker that lost the notice (a subagent died before relaying it,
		// a compaction) can still learn why, read-only and without a repeat.
		wantContext := fmt.Sprintf("You hold no lease: your last ticket, #%d, was cancelled by a human at ", id)
		again := w.call("ticket_finish", jsonObject{"pr_url": "https://github.com/acme/repo/pull/1", "handoff": "Again."})
		assert.True(t, again.isError)
		assert.Contains(t, again.text, wantContext)
		assert.NotContains(t, again.text, "stop working on it")
		checkin := w.mustCall("checkin", nil)
		assert.Contains(t, checkin.text, "holding nothing. "+wantContext)
		assert.NotContains(t, checkin.text, "stop working on it")

		assert.Equal(t, behind, grantedID(w.mustCall("ticket_next", nil)))
		assert.NotContains(t, w.mustCall("checkin", nil).text, "your last ticket", "gone once it holds another ticket")
	})

	t.Run("every call that needs the ticket sees the revocation, whichever it is", func(t *testing.T) {
		rig := newContractRig(t)
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Dropped", "paths": scopePaths("srv/a/")}))
		w := rig.worker()
		require.Equal(t, id, grantedID(w.mustCall("ticket_next", nil)))
		rig.mustPatchStatus(id, "cancelled")

		res := w.call("ticket_scope_extend", jsonObject{"paths": []string{"srv/b/"}})
		assert.True(t, res.isError)
		assert.Contains(t, res.text, "cancelled by a human")
	})
}

func TestContractBlockers(t *testing.T) {
	t.Parallel()
	t.Run("a blocked ticket is not handed out until its blocker is done", func(t *testing.T) {
		rig := newContractRig(t)
		first := ticketID(t, rig.openTicket(jsonObject{"title": "First"}))
		second := ticketID(t, rig.openTicket(jsonObject{"title": "Second"}))
		added := rig.addBlocker(second, first)
		require.Equal(t, http.StatusOK, added.code, added.raw)
		body := added.obj(t)
		assert.Equal(t, "blocked", body["status"])
		assert.Equal(t, []any{float64(first)}, body["blocked_by"])
		assert.Equal(t, "blocked", rig.ticket(second)["status"])
		assert.Equal(t, []any{float64(first)}, rig.ticket(second)["depends_on"])

		w := rig.worker()
		assert.Equal(t, first, grantedID(w.mustCall("ticket_next", nil)))
		other := rig.worker()
		assert.Zero(t, grantedID(other.mustCall("ticket_next", nil)))
		assert.Zero(t, grantedID(other.mustCall("ticket_next", jsonObject{"ticket_id": second})))
		assert.Equal(t, "blocked", rig.ticket(second)["status"], "working the blocker leaves the dependent waiting")

		w.mustComplete(jsonObject{"pr_url": "https://github.com/acme/repo/pull/1", "handoff": "First is done."})
		assert.Equal(t, "open", rig.ticket(second)["status"], "the last blocker going opens it")
		assert.Equal(t, second, grantedID(other.mustCall("ticket_next", nil)))
	})

	t.Run("a cancelled blocker lets its dependents go", func(t *testing.T) {
		rig := newContractRig(t)
		first := ticketID(t, rig.openTicket(jsonObject{"title": "First"}))
		second := ticketID(t, rig.openTicket(jsonObject{"title": "Second"}))
		require.Equal(t, http.StatusOK, rig.addBlocker(second, first).code)

		rig.mustPatchStatus(first, "cancelled")
		assert.Equal(t, "open", rig.ticket(second)["status"])
		assert.Equal(t, second, grantedID(rig.worker().mustCall("ticket_next", nil)))
	})

	t.Run("it takes all of the blockers to go, and removing one by hand counts", func(t *testing.T) {
		rig := newContractRig(t)
		a := ticketID(t, rig.openTicket(jsonObject{"title": "A"}))
		b := ticketID(t, rig.openTicket(jsonObject{"title": "B"}))
		c := ticketID(t, rig.openTicket(jsonObject{"title": "C"}))
		require.Equal(t, http.StatusOK, rig.addBlocker(c, a).code)
		require.Equal(t, http.StatusOK, rig.addBlocker(c, b).code)

		assert.Equal(t, "blocked", rig.removeBlockerOK(c, a)["status"], "one blocker is left")
		assert.Equal(t, "open", rig.removeBlockerOK(c, b)["status"], "none left")
		assert.Equal(t, []any{}, rig.ticket(c)["depends_on"])
	})

	t.Run("a cycle is refused", func(t *testing.T) {
		rig := newContractRig(t)
		a := ticketID(t, rig.openTicket(jsonObject{"title": "A"}))
		b := ticketID(t, rig.openTicket(jsonObject{"title": "B"}))
		require.Equal(t, http.StatusOK, rig.addBlocker(b, a).code)

		assert.GreaterOrEqual(t, rig.addBlocker(a, b).code, 400)
		assert.GreaterOrEqual(t, rig.addBlocker(a, a).code, 400)
		assert.Equal(t, "open", rig.ticket(a)["status"])
		assert.Equal(t, []any{}, rig.ticket(a)["depends_on"])
	})

	t.Run("a worker records and lifts a blocker on the ticket it holds", func(t *testing.T) {
		rig := newContractRig(t)
		work := ticketID(t, rig.openTicket(jsonObject{"title": "Work"}))
		prereq := ticketID(t, rig.openTicket(jsonObject{"title": "Prerequisite"}))
		w := rig.worker()
		require.Equal(t, work, grantedID(w.mustCall("ticket_next", nil)))

		added := w.mustCall("ticket_blocker_add", jsonObject{"ticket_id": work, "blocker_id": prereq})
		assert.Equal(t, true, added.structured["changed"])
		assert.Equal(t, "in_progress", added.structured["status"], "a ticket in hand is not taken from its worker")
		assert.Contains(t, added.text, "#1 now waits on #2.")
		rig.assertGolden("scopes/ticket_blocker_add", added)
		assert.Equal(t, []any{float64(prereq)}, rig.ticket(work)["depends_on"])
		assert.Contains(t, rig.commentBodies(work)[0], "#2", "the change is recorded on the ticket")

		again := w.mustCall("ticket_blocker_add", jsonObject{"ticket_id": work, "blocker_id": prereq})
		assert.Equal(t, false, again.structured["changed"])
		assert.Contains(t, again.text, "nothing changed")

		removed := w.mustCall("ticket_blocker_remove", jsonObject{"ticket_id": work, "blocker_id": prereq})
		assert.Equal(t, true, removed.structured["changed"])
		assert.Equal(t, []any{}, rig.ticket(work)["depends_on"])
	})

	t.Run("releasing a ticket blocked on another holds it until that one is done", func(t *testing.T) {
		rig := newContractRig(t)
		work := ticketID(t, rig.openTicket(jsonObject{"title": "Work"}))
		prereq := ticketID(t, rig.openTicket(jsonObject{"title": "Prerequisite"}))
		w := rig.worker()
		require.Equal(t, work, grantedID(w.mustCall("ticket_next", nil)))

		released := w.mustCall("ticket_release", jsonObject{"reason": "Needs the prerequisite first.", "blocked_on": prereq})
		assert.Equal(t, "Released #1 (now blocked).", released.text)
		assert.Equal(t, "blocked", rig.ticket(work)["status"])
		assert.Nil(t, rig.agentBody(w.name)["lease"])

		assert.Equal(t, prereq, grantedID(w.mustCall("ticket_next", nil)), "the queue offers the prerequisite, not the blocked ticket")
		w.mustComplete(jsonObject{"pr_url": "https://github.com/acme/repo/pull/1", "handoff": "Prerequisite done."})
		assert.Equal(t, "open", rig.ticket(work)["status"])
		assert.Equal(t, work, grantedID(w.mustCall("ticket_next", nil)))
	})

	t.Run("a worker cannot reach into a ticket it does not hold, nor lift a person's blocker", func(t *testing.T) {
		rig := newContractRig(t)
		mine := ticketID(t, rig.openTicket(jsonObject{"title": "Mine"}))
		stranger := ticketID(t, rig.openTicket(jsonObject{"title": "Someone else's"}))
		blocker := ticketID(t, rig.openTicket(jsonObject{"title": "Blocker"}))
		w := rig.worker()
		require.Equal(t, mine, grantedID(w.mustCall("ticket_next", nil)))

		res := w.call("ticket_blocker_add", jsonObject{"ticket_id": stranger, "blocker_id": blocker})
		assert.True(t, res.isError)
		assert.Contains(t, res.text, "only of the ticket it holds")
		assert.Equal(t, "open", rig.ticket(stranger)["status"])

		require.Equal(t, http.StatusOK, rig.addBlocker(mine, blocker).code)
		lift := w.call("ticket_blocker_remove", jsonObject{"ticket_id": mine, "blocker_id": blocker})
		assert.True(t, lift.isError, "a blocker a person added is theirs to lift")
		assert.Equal(t, []any{float64(blocker)}, rig.ticket(mine)["depends_on"])
	})
}

func (r *contractRig) removeBlockerOK(id, blocker int64) jsonObject {
	r.t.Helper()
	resp := r.removeBlocker(id, blocker)
	require.Equal(r.t, http.StatusOK, resp.code, resp.raw)
	return resp.obj(r.t)
}

// The status history as the reports read it, over a lifecycle that visits most
// states: what has ended is counted as a stay, what has not as ongoing.
func TestContractHistory_Reports(t *testing.T) {
	t.Parallel()
	rig := newContractRig(t)
	work := ticketID(t, rig.openTicket(jsonObject{"title": "Work"}))
	prereq := ticketID(t, rig.openTicket(jsonObject{"title": "Prerequisite"}))
	shelved := ticketID(t, rig.openTicket(jsonObject{"title": "Shelved"}))
	dropped := ticketID(t, rig.openTicket(jsonObject{"title": "Dropped"}))

	type stay struct{ stays, ongoing float64 }
	states := func() map[string]stay {
		out := map[string]stay{}
		report := rig.mustBoard(http.StatusOK, http.MethodGet, "/api/reports?days=7&include_parked=true", nil).obj(t)
		for _, s := range report["states"].([]any) {
			s := s.(map[string]any)
			out[s["state"].(string)] = stay{s["stays"].(float64), s["ongoing"].(float64)}
		}
		return out
	}
	counts := func() map[string]float64 {
		out := map[string]float64{}
		for _, c := range rig.mustBoard(http.StatusOK, http.MethodGet, "/api/board", nil).obj(t)["columns"].([]any) {
			c := c.(map[string]any)
			out[c["status"].(string)] = c["count"].(float64)
		}
		return out
	}
	waiting := func(status string) []float64 {
		out := []float64{}
		for _, w := range rig.mustBoard(http.StatusOK, http.MethodGet, "/api/reports/waiting?include_parked=true&status="+status, nil).obj(t)["tickets"].([]any) {
			out = append(out, w.(map[string]any)["id"].(float64))
		}
		return out
	}

	rig.mustPatchStatus(shelved, "parked")
	rig.mustPatchStatus(dropped, "cancelled")
	assert.Equal(t, map[string]stay{"open": {4, 2}, "parked": {1, 1}}, onlyStays(states()))
	assert.Equal(t, []float64{float64(shelved)}, waiting("parked"))

	w := rig.worker()
	require.Equal(t, work, grantedID(w.mustCall("ticket_next", nil)))
	assert.Equal(t, map[string]stay{"open": {4, 1}, "parked": {1, 1}, "in_progress": {1, 1}}, onlyStays(states()))
	assert.Equal(t, []float64{float64(work)}, waiting("in_progress"))

	w.mustCall("ticket_release", jsonObject{"reason": "Needs the prerequisite.", "blocked_on": prereq})
	assert.Equal(t, map[string]stay{"open": {4, 1}, "parked": {1, 1}, "in_progress": {1, 0}, "blocked": {1, 1}}, onlyStays(states()))
	assert.Equal(t, []float64{float64(work)}, waiting("blocked"))
	assert.Empty(t, waiting("in_progress"))

	require.Equal(t, prereq, grantedID(w.mustCall("ticket_next", nil)))
	w.mustComplete(jsonObject{"pr_url": "https://github.com/acme/repo/pull/1", "handoff": "Prerequisite done."})
	assert.Equal(t, map[string]stay{"open": {5, 1}, "parked": {1, 1}, "in_progress": {2, 0}, "blocked": {1, 0}}, onlyStays(states()),
		"the dependent opened when its blocker was done")

	require.Equal(t, work, grantedID(w.mustCall("ticket_next", nil)))
	w.mustComplete(jsonObject{"pr_url": "https://github.com/acme/repo/pull/1", "handoff": "Work done."})
	assert.Equal(t, map[string]stay{"open": {5, 0}, "parked": {1, 1}, "in_progress": {3, 0}, "blocked": {1, 0}}, onlyStays(states()))

	// What the board counts and what the report counts of those that left.
	assert.Equal(t, map[string]float64{"preparing": 0, "parked": 1, "open": 0, "in_progress": 0, "in_merge_queue": 0, "blocked": 0, "discussion": 0, "review": 0,
		"done": 2, "cancelled": 1}, counts())
	report := rig.mustBoard(http.StatusOK, http.MethodGet, "/api/reports?days=7", nil).obj(t)
	lead := report["lead"].(map[string]any)
	assert.Equal(t, float64(2), lead["done"], "a cancelled ticket is not a finished one")
	assert.Equal(t, float64(2), lead["claimed_done"])
	var throughput float64
	for _, b := range report["throughput"].([]any) {
		throughput += b.(map[string]any)["done"].(float64)
	}
	assert.Equal(t, float64(2), throughput)
}

// onlyStays drops the states with no stays, which the report lists as empty
// rows.
func onlyStays[S comparable](in map[string]S) map[string]S {
	var zero S
	out := map[string]S{}
	for k, v := range in {
		if v != zero {
			out[k] = v
		}
	}
	return out
}

func TestContractHistory_Revisions(t *testing.T) {
	t.Parallel()
	rig := newContractRig(t)
	id := ticketID(t, rig.openTicket(jsonObject{"title": "Original", "body": "First draft."}))
	path := fmt.Sprintf("/api/tickets/%d/revisions", id)
	list := func() []any {
		return rig.mustBoard(http.StatusOK, http.MethodGet, path, nil).obj(t)["revisions"].([]any)
	}
	assert.Empty(t, list())

	rig.mustBoard(http.StatusOK, http.MethodPost, path, jsonObject{"title": "Renamed", "base_revision": 0})
	w := rig.worker()
	require.Equal(t, id, grantedID(w.mustCall("ticket_next", nil)))
	w.mustCall("ticket_edit", jsonObject{"revision": 1, "body": "Second draft."})

	revisions := list()
	require.Len(t, revisions, 2, "oldest first")
	first, second := revisions[0].(map[string]any), revisions[1].(map[string]any)
	assert.Equal(t, rig.user.ID.String(), first["editor_user_id"])
	assert.Equal(t, "Original", first["old_title"])
	assert.Equal(t, "Renamed", first["new_title"])
	assert.Equal(t, w.id, second["editor_agent_id"])
	assert.Equal(t, "First draft.", second["old_body"])
	assert.Equal(t, "Second draft.", second["new_body"])
	assert.Equal(t, float64(2), rig.ticket(id)["revision"])

	t.Run("an edit against a stale revision is refused", func(t *testing.T) {
		stale := rig.boardDo(http.MethodPost, path, jsonObject{"title": "Late", "base_revision": 0})
		assert.Equal(t, http.StatusConflict, stale.code)
		assert.Len(t, list(), 2)
	})
}
