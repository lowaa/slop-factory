package main

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

const (
	flowRepo = "github.com/acme/repo"
	flowLink = "https://github.com/acme/repo/pull/12"
	headA    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headB    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	mergeSHA = "cccccccccccccccccccccccccccccccccccccccc"
)

var flowRef = codehost.ChangeRef{Host: "github.com", Owner: "acme", Repo: "repo", Number: 12}

// changeFlow is one ticket held by one agent in a repository the workspace
// manifest declares, with a code host ready to open its change.
type changeFlow struct {
	rig    *contractRig
	agent  *contractAgent
	id     int64
	branch string
}

// startFlow files a ticket, has an agent claim it and readies the fake to
// answer OpenChange with the change at flowLink.
func (r *contractRig) startFlow(requiresApproval bool) *changeFlow {
	r.t.Helper()
	r.declareRepo(flowRepo)
	tk := r.openTicket(jsonObject{"title": "Change flow", "requires_approval": requiresApproval,
		"paths": []jsonObject{{"path": "server/flow/"}}})
	f := &changeFlow{rig: r, agent: r.worker(), id: ticketID(r.t, tk), branch: "ticket-1-a1"}
	require.Equal(r.t, f.id, grantedID(f.agent.mustCall("ticket_next", nil)))
	r.host.opened(flowRef)
	return f
}

// recordUnderOldName makes the attempt's current change look like one opened
// before branch names were enforced.
func (f *changeFlow) recordUnderOldName() {
	f.rig.t.Helper()
	_, err := f.rig.boot.appDB.Exec(db.WithTenant(f.rig.t.Context(), f.rig.user.TenantID), `UPDATE ticket_changes SET head_branch = 'ticket-1-old-name' WHERE superseded_at IS NULL`)
	require.NoError(f.rig.t, err)
}

func (f *changeFlow) open(draft bool) *toolResult {
	f.rig.t.Helper()
	return f.agent.call("change_open", jsonObject{"branch": f.branch, "title": "Change flow", "body": "Does the thing.", "draft": draft})
}

// opened is a draft change, taken out of draft.
func (f *changeFlow) opened() {
	f.rig.t.Helper()
	require.False(f.rig.t, f.open(true).isError)
	require.False(f.rig.t, f.open(false).isError)
}

// pushed is the host's answer once the head is sha and the base tip is in it:
// every check on it in state, and the changes against the base the ones the
// diff names.
func (f *changeFlow) pushed(sha string, state codehost.CheckState) {
	f.rig.host.checks(flowRef, codehost.NewChecks(sha, "main", []codehost.Check{{Name: "ci", State: state}}))
	f.rig.host.configure(func(h *codehosttest.Fake) {
		h.CompareOf = map[codehost.ChangeRef]*codehost.BaseComparison{flowRef: {Base: "main", ContainsTip: true}}
	})
}

func (f *changeFlow) ticket() jsonObject { return f.rig.ticket(f.id) }

// readied is every MarkReady the fake was asked for; the harness's host leaves
// them out.
func readied(h *contractHost) []codehosttest.OperationCall {
	var calls []codehosttest.OperationCall
	h.configure(func(f *codehosttest.Fake) { calls = f.Readied() })
	return calls
}

// lockState is the board's view of the integration lock.
func (r *contractRig) lockState() jsonObject {
	r.t.Helper()
	return r.mustBoard(http.StatusOK, http.MethodGet, "/api/integration_lock", nil).obj(r.t)
}

// lockHeldBy is the name of the agent the board shows holding the integration
// lock, or "" when it is free.
func (r *contractRig) lockHeldBy() string {
	r.t.Helper()
	holder, ok := r.lockState()["holder"].(map[string]any)
	if !ok {
		return ""
	}
	return holder["agent_name"].(string)
}

func commentBodies(comments []jsonObject) []string {
	bodies := make([]string, 0, len(comments))
	for _, c := range comments {
		bodies = append(bodies, c["body"].(string))
	}
	return bodies
}

// a diff against the base, as the host reports it: the same patch under every
// head that "takes main in".
func diffOf(patch string) *codehost.Diff {
	return &codehost.Diff{Base: "main", BaseSHA: "base-tip", MergeBaseSHA: "base-tip",
		Files: []codehost.FileDiff{{Path: "server/flow/a.go", Status: codehost.DiffModified, Patch: patch}}}
}

const patchOne = "@@ -1,1 +1,2 @@\n line\n+added\n"
const patchTwo = "@@ -1,1 +1,2 @@\n line\n+something else\n"

// -- the change flow --

func TestContractChange_Open(t *testing.T) {
	t.Parallel()
	t.Run("opens a draft, takes it out of draft, and says so on every repeat", func(t *testing.T) {
		rig := newContractRig(t)
		f := rig.startFlow(false)

		draft := f.open(true)
		require.False(t, draft.isError, draft.text)
		rig.assertGolden("change/change_open_draft", draft)
		assert.Equal(t, flowLink, f.ticket()["pr_url"], "the board shows the ticket's change")
		opens := rig.host.opens()
		require.Len(t, opens, 1)
		req := opens[0].Request
		assert.Equal(t, "acme", req.Repo.Owner)
		assert.Equal(t, "repo", req.Repo.Name)
		assert.Equal(t, f.branch, req.Head)
		assert.Equal(t, "main", req.Base, "the base is the server's, not the agent's")
		assert.True(t, req.Draft)
		assert.Contains(t, req.Body, "Does the thing.")
		assert.Contains(t, req.Body, "slop-haus #1", "the change links back to the ticket")

		again := f.open(true)
		assert.Equal(t, true, again.structured["existing"])
		assert.Equal(t, true, again.structured["draft"])
		assert.Empty(t, readied(rig.host))

		ready := f.open(false)
		require.False(t, ready.isError, ready.text)
		rig.assertGolden("change/change_open_ready", ready)
		assert.Equal(t, false, ready.structured["draft"])
		assert.Len(t, readied(rig.host), 1)

		f.open(false)
		assert.Len(t, readied(rig.host), 1, "a change that is ready is not marked ready again")
		assert.Len(t, rig.host.opens(), 1, "nor opened again")
	})

	t.Run("a change recorded under another name is replaced by one from the attempt's own branch", func(t *testing.T) {
		rig := newContractRig(t)
		f := rig.startFlow(false)
		f.opened()

		rig.host.opened(codehost.ChangeRef{Host: "github.com", Owner: "acme", Repo: "repo", Number: 13})
		f.recordUnderOldName()
		second := f.open(false)
		require.False(t, second.isError, second.text)
		assert.Equal(t, "https://github.com/acme/repo/pull/13", second.structured["url"])
		assert.Equal(t, "https://github.com/acme/repo/pull/13", f.ticket()["pr_url"])
		assert.Len(t, rig.host.opens(), 2)
	})

	t.Run("refuses what cannot be a change before asking the host", func(t *testing.T) {
		rig := newContractRig(t)
		f := rig.startFlow(false)
		for name, c := range map[string]struct {
			args jsonObject
			text string
		}{
			"the base branch itself": {jsonObject{"branch": "main", "title": "T"}, "from the branch it merges into"},
			"a flag for a branch":    {jsonObject{"branch": "--force", "title": "T"}, "not a branch name"},
			"no title":               {jsonObject{"branch": "ticket-1-x", "title": " "}, "title must be"},
			"a title of two lines":   {jsonObject{"branch": "ticket-1-x", "title": "a\nb"}, "one line"},
		} {
			res := f.agent.call("change_open", c.args)
			assert.True(t, res.isError, name)
			assert.Contains(t, res.text, c.text, name)
		}
		assert.Empty(t, rig.host.opens())
		assert.Nil(t, f.ticket()["pr_url"])
	})

	t.Run("a repository the manifest does not declare has no change to open", func(t *testing.T) {
		rig := newContractRig(t)
		rig.openTicket(jsonObject{"title": "Unplaced", "paths": []jsonObject{{"path": "server/flow/"}}})
		w := rig.worker()
		w.mustCall("ticket_next", nil)
		res := w.call("change_open", jsonObject{"branch": "ticket-1-x", "title": "T"})
		assert.True(t, res.isError)
		assert.Contains(t, res.text, "workspace manifest does not place this ticket")
		assert.Empty(t, rig.host.opens())
	})

	t.Run("a host that fails opens nothing on the ticket", func(t *testing.T) {
		rig := newContractRig(t)
		f := rig.startFlow(false)
		rig.host.configure(func(h *codehosttest.Fake) { h.OpenErr = errors.New("the host is down") })
		res := f.open(true)
		assert.True(t, res.isError)
		assert.Nil(t, f.ticket()["pr_url"])
		assert.Equal(t, "in_progress", f.ticket()["status"])
	})

	t.Run("an agent holding nothing has no change", func(t *testing.T) {
		rig := newContractRig(t)
		rig.declareRepo(flowRepo)
		res := rig.worker().call("change_open", jsonObject{"branch": "ticket-1-x", "title": "T"})
		assert.True(t, res.isError)
		assert.Empty(t, rig.host.opens())
	})
}

func TestContractChange_Checks(t *testing.T) {
	t.Parallel()
	t.Run("reads the checks on the head, in each state", func(t *testing.T) {
		rig := newContractRig(t)
		f := rig.startFlow(false)
		f.opened()
		for _, c := range []struct {
			name   string
			checks []codehost.Check
			state  string
			passed bool
		}{
			{"none", nil, "none", false},
			{"pending", []codehost.Check{{Name: "lint", State: codehost.CheckSuccess}, {Name: "test", State: codehost.CheckPending}}, "pending", false},
			{"failure", []codehost.Check{{Name: "lint", State: codehost.CheckSuccess}, {Name: "test", State: codehost.CheckFailure}}, "failure", false},
			{"success", []codehost.Check{{Name: "lint", State: codehost.CheckSuccess}, {Name: "test", State: codehost.CheckSuccess}}, "success", true},
		} {
			rig.host.checks(flowRef, codehost.NewChecks(headA, "main", c.checks))
			res := f.agent.call("change_checks", nil)
			require.False(t, res.isError, c.name+": "+res.text)
			assert.Equal(t, c.state, res.structured["state"], c.name)
			assert.Equal(t, c.passed, res.structured["passed"], c.name)
			assert.Equal(t, headA, res.structured["head_sha"], c.name)
			assert.NotContains(t, res.structured, "for_your_head", "it is only said when a head_sha is named")
			rig.assertGolden("change/change_checks_"+c.name, res)
		}
	})

	t.Run("says whether the checks are for the commit the agent pushed", func(t *testing.T) {
		rig := newContractRig(t)
		f := rig.startFlow(false)
		f.opened()
		f.pushed(headA, codehost.CheckSuccess)

		mine := f.agent.mustCall("change_checks", jsonObject{"head_sha": headA})
		assert.Equal(t, true, mine.structured["for_your_head"])
		upper := f.agent.mustCall("change_checks", jsonObject{"head_sha": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"})
		assert.Equal(t, true, upper.structured["for_your_head"], "a commit id is compared without regard to case")

		other := f.agent.mustCall("change_checks", jsonObject{"head_sha": headB})
		assert.Equal(t, false, other.structured["for_your_head"])
		assert.Equal(t, headA, other.structured["head_sha"], "it still says which head the checks are for")
		rig.assertGolden("change/change_checks_other_head", other)
	})

	t.Run("is a read: it changes nothing", func(t *testing.T) {
		rig := newContractRig(t)
		f := rig.startFlow(false)
		f.opened()
		f.pushed(headA, codehost.CheckSuccess)
		before := f.ticket()
		f.agent.mustCall("change_checks", nil)
		assert.Equal(t, before, f.ticket())
		assert.Empty(t, rig.host.merges())
	})

	t.Run("fails for a ticket with no change, and when the host does", func(t *testing.T) {
		rig := newContractRig(t)
		f := rig.startFlow(false)
		none := f.agent.call("change_checks", nil)
		assert.True(t, none.isError)
		assert.Contains(t, none.text, "has not opened a change for this ticket")

		f.opened()
		rig.host.configure(func(h *codehosttest.Fake) { h.ChecksErr = errors.New("the host is down") })
		down := f.agent.call("change_checks", nil)
		assert.True(t, down.isError)
	})
}

func TestContractChange_IntegrationLock(t *testing.T) {
	t.Parallel()
	rig := newContractRig(t)
	rig.declareRepo(flowRepo)
	rig.openTicket(jsonObject{"title": "First", "paths": []jsonObject{{"path": "server/one/"}}})
	rig.openTicket(jsonObject{"title": "Second", "paths": []jsonObject{{"path": "server/two/"}}})
	first, second := rig.worker(), rig.worker()
	require.Equal(t, int64(1), grantedID(first.mustCall("ticket_next", nil)))
	require.Equal(t, int64(2), grantedID(second.mustCall("ticket_next", nil)))

	t.Run("an agent takes the lock, and the board shows who has it", func(t *testing.T) {
		got := first.mustCall("integration_lock", nil)
		rig.assertGolden("change/integration_lock_held", got)
		assert.Equal(t, first.name, rig.lockHeldBy())
		holder := rig.lockState()["holder"].(map[string]any)
		assert.Equal(t, float64(1), holder["ticket_id"])
		assert.Equal(t, "First", holder["ticket_title"])
	})

	t.Run("another agent is refused, told where it stands, and queued", func(t *testing.T) {
		refused := second.call("integration_lock", nil)
		assert.True(t, refused.isError)
		assert.Contains(t, refused.text, "lock is held by another agent")
		assert.Contains(t, refused.text, fmt.Sprintf("held by agent %q on ticket #1", first.name))
		assert.Contains(t, refused.text, "You are number 1 in the queue, next in line.")
		assert.Equal(t, first.name, rig.lockHeldBy())
		queue := rig.lockState()["queue"].([]any)
		require.Len(t, queue, 1)
		assert.Equal(t, second.name, queue[0].(map[string]any)["agent_name"])

		again := second.call("integration_lock", nil)
		assert.True(t, again.isError)
		assert.Len(t, rig.lockState()["queue"], 1, "asking again does not queue it twice")
	})

	t.Run("the holder asking again keeps it", func(t *testing.T) {
		assert.False(t, first.call("integration_lock", nil).isError)
		assert.Equal(t, first.name, rig.lockHeldBy())
	})

	t.Run("on release the lock is reserved for the head of the queue, which takes it", func(t *testing.T) {
		released := first.mustCall("integration_unlock", nil)
		rig.assertGolden("change/integration_unlock", released)
		assert.Equal(t, "", rig.lockHeldBy())

		third := rig.worker()
		rig.openTicket(jsonObject{"title": "Third", "paths": []jsonObject{{"path": "server/three/"}}})
		third.mustCall("ticket_next", nil)
		reserved := third.call("integration_lock", nil)
		assert.True(t, reserved.isError, "the reserved lock is not up for grabs")
		assert.Contains(t, reserved.text, "reserved for the agent at the head of the queue")

		second.mustCall("integration_lock", nil)
		assert.Equal(t, second.name, rig.lockHeldBy())
	})

	t.Run("releasing a lock not held is an error", func(t *testing.T) {
		res := first.call("integration_unlock", nil)
		assert.True(t, res.isError)
		assert.Equal(t, second.name, rig.lockHeldBy())
	})

	t.Run("finishing the ticket frees the lock", func(t *testing.T) {
		second.mustComplete(jsonObject{"pr_url": "https://github.com/acme/repo/pull/1", "handoff": "done"})
		assert.Equal(t, "", rig.lockHeldBy())
	})
}

func TestContractChange_HappyPath(t *testing.T) {
	t.Parallel()
	rig := newContractRig(t)
	f := rig.startFlow(false)

	f.opened()
	f.pushed(headA, codehost.CheckSuccess)
	rig.host.mergedWith(mergeSHA)
	f.agent.mustCall("integration_lock", nil)

	merged := f.agent.mustCall("change_merge", jsonObject{"head_sha": headA})
	rig.assertGolden("change/change_merge_merged", merged)
	assert.Equal(t, mergeSHA, merged.structured["merge_sha"])

	calls := rig.host.merges()
	require.Len(t, calls, 1)
	assert.Equal(t, rig.user.TenantID, calls[0].Tenant)
	assert.Equal(t, flowRef, calls[0].Change)
	assert.Equal(t, headA, calls[0].Merge.HeadSHA, "the merge is for the head whose checks were read")
	assert.Equal(t, "Merge "+f.branch+": Change flow", calls[0].Merge.Subject)
	assert.Contains(t, calls[0].Merge.Body, "slop-haus #1")

	closed := f.ticket()
	assert.Equal(t, "done", closed["status"], "the merge closes the ticket")
	assert.Equal(t, flowLink, closed["pr_url"])
	assert.Equal(t, []string{fmt.Sprintf("Merged %s (merge commit %s) on head %s.", flowLink, mergeSHA, headA)}, commentBodies(rig.comments(f.id)))
	assert.Equal(t, "", rig.lockHeldBy(), "and frees the lock")
	assert.Nil(t, rig.agentBody(f.agent.name)["lease"])

	f.agent.mustCall("integration_unlock", nil)
	assert.Equal(t, []string{fmt.Sprintf("Merged %s (merge commit %s) on head %s.", flowLink, mergeSHA, headA)}, commentBodies(rig.comments(f.id)), "the late calls changed nothing")
	assert.Len(t, rig.host.merges(), 1)
}

// -- the merge gate --

// gate is a change that would merge: open, ready, green on headA, and the lock
// held, so each test takes one thing away.
func (r *contractRig) gate(requiresApproval bool) *changeFlow {
	r.t.Helper()
	f := r.startFlow(requiresApproval)
	f.opened()
	f.pushed(headA, codehost.CheckSuccess)
	r.host.mergedWith(mergeSHA)
	f.agent.mustCall("integration_lock", nil)
	return f
}

// refusedMerge is a merge that is refused by a message containing text: the
// host was never asked to merge, and the ticket does not say it was.
func (f *changeFlow) refusedMerge(args jsonObject, text, golden string) {
	f.rig.t.Helper()
	merged := len(f.rig.host.merges())
	res := f.agent.call("change_merge", args)
	assert.True(f.rig.t, res.isError, "the merge was not refused: %s", res.text)
	assert.Contains(f.rig.t, res.text, text)
	assert.Len(f.rig.t, f.rig.host.merges(), merged, "the host was asked to merge")
	for _, c := range commentBodies(f.rig.comments(f.id)) {
		assert.NotContains(f.rig.t, c, "Merged ")
	}
	if golden != "" {
		f.rig.assertGolden("change/"+golden, res)
	}
}

// merges is a merge that goes through, for a refusal to be shown to have been
// the only thing in the way.
func (f *changeFlow) merges(args jsonObject) {
	f.rig.t.Helper()
	res := f.agent.call("change_merge", args)
	require.False(f.rig.t, res.isError, res.text)
	assert.Equal(f.rig.t, mergeSHA, res.structured["merge_sha"])
}

func TestContractChange_MergeGate(t *testing.T) {
	t.Parallel()
	t.Run("no lock", func(t *testing.T) {
		rig := newContractRig(t)
		f := rig.gate(false)
		f.agent.mustCall("integration_unlock", nil)
		f.refusedMerge(jsonObject{"head_sha": headA}, "you do not hold the integration lock", "merge_refused_no_lock")
		f.agent.mustCall("integration_lock", nil)
		f.merges(jsonObject{"head_sha": headA})
	})

	t.Run("the lock of another agent is no lock", func(t *testing.T) {
		rig := newContractRig(t)
		f := rig.gate(false)
		f.agent.mustCall("integration_unlock", nil)
		rig.openTicket(jsonObject{"title": "Other", "paths": []jsonObject{{"path": "server/other/"}}})
		other := rig.worker()
		other.mustCall("ticket_next", nil)
		other.mustCall("integration_lock", nil)
		f.refusedMerge(nil, "you do not hold the integration lock", "")
		other.mustCall("integration_unlock", nil)
		f.agent.mustCall("integration_lock", nil)
		f.merges(nil)
	})

	t.Run("checks that have not passed", func(t *testing.T) {
		for _, c := range []struct {
			state  codehost.CheckState
			text   string
			golden string
		}{
			{codehost.CheckPending, "pending on " + headA + " (ci pending)", "merge_refused_checks_pending"},
			{codehost.CheckFailure, "failure on " + headA + " (ci failure)", "merge_refused_checks_failed"},
			{codehost.CheckNone, "no checks have reported on " + headA, "merge_refused_checks_none"},
		} {
			rig := newContractRig(t)
			f := rig.gate(false)
			rig.host.checks(flowRef, codehost.NewChecks(headA, "main", checksIn(c.state)))
			f.refusedMerge(jsonObject{"head_sha": headA}, c.text, c.golden)
			f.pushed(headA, codehost.CheckSuccess)
			f.merges(jsonObject{"head_sha": headA})
		}
	})

	t.Run("a head other than the one the agent pushed", func(t *testing.T) {
		rig := newContractRig(t)
		f := rig.gate(false)
		f.refusedMerge(jsonObject{"head_sha": headB}, "the change's head is "+headA+", not "+headB, "merge_refused_head_mismatch")
		f.merges(jsonObject{"head_sha": headA})
	})

	t.Run("a head that moved after the checks were read", func(t *testing.T) {
		rig := newContractRig(t)
		f := rig.gate(false)
		rig.host.configure(func(h *codehosttest.Fake) { h.MergeErr = codehost.ErrHeadMoved })
		res := f.agent.call("change_merge", jsonObject{"head_sha": headA})
		assert.True(t, res.isError)
		assert.Contains(t, res.text, "head moved after its checks were read, so nothing was merged")
		rig.assertGolden("change/merge_refused_head_moved", res)
		calls := rig.host.merges()
		require.Len(t, calls, 1)
		assert.Equal(t, headA, calls[0].Merge.HeadSHA, "the host was asked for exactly the head that was checked")
		for _, c := range commentBodies(rig.comments(f.id)) {
			assert.NotContains(t, c, "Merged ", "nothing is recorded as merged")
		}

		rig.host.configure(func(h *codehosttest.Fake) { h.MergeErr = nil })
		f.merges(jsonObject{"head_sha": headA})
		assert.Len(t, rig.host.merges(), 2, "the refusal left the change free to merge")
	})

	t.Run("a host that fails to answer merges nothing", func(t *testing.T) {
		for name, c := range map[string]struct {
			fail func(h *codehosttest.Fake, err error)
			heal func(h *codehosttest.Fake)
		}{
			"the base comparison": {
				func(h *codehosttest.Fake, err error) { h.CompareErr = err },
				func(h *codehosttest.Fake) { h.CompareErr = nil },
			},
			"the merge itself": {
				func(h *codehosttest.Fake, err error) { h.MergeErr = err },
				func(h *codehosttest.Fake) { h.MergeErr = nil },
			},
		} {
			rig := newContractRig(t)
			f := rig.gate(false)
			rig.host.configure(func(h *codehosttest.Fake) { c.fail(h, errors.New("the host is down")) })
			res := f.agent.call("change_merge", jsonObject{"head_sha": headA})
			assert.True(t, res.isError, name)
			for _, body := range commentBodies(rig.comments(f.id)) {
				assert.NotContains(t, body, "Merged ", name)
			}
			rig.host.configure(c.heal)
			f.merges(jsonObject{"head_sha": headA})
		}
	})

	t.Run("a diff the host cannot give refuses a head the approval does not name", func(t *testing.T) {
		rig := newContractRig(t)
		f := rig.gate(true)
		rig.host.configure(func(h *codehosttest.Fake) { h.DiffOf = map[string]*codehost.Diff{headA: diffOf(patchOne)} })
		f.review()
		f.approve()
		f.agent.mustCall("integration_lock", nil)
		f.pushed(headB, codehost.CheckSuccess)
		rig.host.configure(func(h *codehosttest.Fake) { h.DiffErr = errors.New("the host is down") })
		f.refusedMerge(jsonObject{"head_sha": headB}, "could not be compared with the reviewed ones", "")
		rig.host.configure(func(h *codehosttest.Fake) { h.DiffErr = nil; h.DiffOf[headB] = diffOf(patchOne) })
		f.merges(jsonObject{"head_sha": headB})
	})

	t.Run("a host that fails to mark the change ready leaves it a draft", func(t *testing.T) {
		rig := newContractRig(t)
		f := rig.startFlow(false)
		require.False(t, f.open(true).isError)
		f.pushed(headA, codehost.CheckSuccess)
		rig.host.mergedWith(mergeSHA)
		rig.host.configure(func(h *codehosttest.Fake) { h.ReadyErr = errors.New("the host is down") })
		assert.True(t, f.open(false).isError)
		f.agent.mustCall("integration_lock", nil)
		f.refusedMerge(nil, "the change is still a draft", "")
		rig.host.configure(func(h *codehosttest.Fake) { h.ReadyErr = nil })
		require.False(t, f.open(false).isError)
		f.merges(nil)
	})

	t.Run("a head that does not contain the base's tip", func(t *testing.T) {
		rig := newContractRig(t)
		f := rig.gate(false)
		rig.host.configure(func(h *codehosttest.Fake) {
			h.CompareOf = map[codehost.ChangeRef]*codehost.BaseComparison{flowRef: {Base: "main", ContainsTip: false}}
		})
		f.refusedMerge(jsonObject{"head_sha": headA}, "does not contain the base branch's latest commit", "merge_refused_behind_base")
		f.pushed(headA, codehost.CheckSuccess)
		f.merges(jsonObject{"head_sha": headA})
	})

	t.Run("a change retargeted to another branch", func(t *testing.T) {
		rig := newContractRig(t)
		f := rig.gate(false)
		rig.host.checks(flowRef, codehost.NewChecks(headA, "release", checksIn(codehost.CheckSuccess)))
		f.refusedMerge(nil, `it targets "release" on the host, not "main"`, "")
		f.pushed(headA, codehost.CheckSuccess)
		f.merges(nil)
	})

	t.Run("a change still in draft", func(t *testing.T) {
		rig := newContractRig(t)
		f := rig.startFlow(false)
		require.False(t, f.open(true).isError)
		f.pushed(headA, codehost.CheckSuccess)
		rig.host.mergedWith(mergeSHA)
		f.agent.mustCall("integration_lock", nil)
		f.refusedMerge(nil, "the change is still a draft", "merge_refused_draft")
		require.False(t, f.open(false).isError)
		f.merges(nil)
	})

	t.Run("a ticket with no change", func(t *testing.T) {
		rig := newContractRig(t)
		f := rig.startFlow(false)
		f.agent.mustCall("integration_lock", nil)
		f.refusedMerge(nil, "has not opened a change for this ticket", "")
		assert.Nil(t, f.ticket()["pr_url"])
	})

	t.Run("a ticket that needs approval and has none", func(t *testing.T) {
		rig := newContractRig(t)
		f := rig.gate(true)
		f.refusedMerge(jsonObject{"head_sha": headA}, "requires a human to approve its change", "merge_refused_not_approved")
		f.review()
		f.approve()
		f.agent.mustCall("integration_lock", nil)
		f.merges(jsonObject{"head_sha": headA})
	})

	t.Run("an approval of an earlier head", func(t *testing.T) {
		rig := newContractRig(t)
		f := rig.gate(true)
		f.review()
		f.approve()
		f.agent.mustCall("integration_lock", nil)
		f.pushed(headB, codehost.CheckSuccess)

		f.refusedMerge(jsonObject{"head_sha": headB}, "the approval does not cover it", "merge_refused_head_not_reviewed")
		f.agent.mustCall("integration_unlock", nil)
		f.review()
		f.approve()
		f.agent.mustCall("integration_lock", nil)
		f.merges(jsonObject{"head_sha": headB})
	})

	t.Run("a later head with the same changes against the base is covered", func(t *testing.T) {
		rig := newContractRig(t)
		f := rig.gate(true)
		rig.host.configure(func(h *codehosttest.Fake) {
			h.DiffOf = map[string]*codehost.Diff{headA: diffOf(patchOne), headB: diffOf(patchOne)}
		})
		f.review()
		f.approve()
		f.agent.mustCall("integration_lock", nil)
		f.pushed(headB, codehost.CheckSuccess)
		f.merges(jsonObject{"head_sha": headB})
		assert.Contains(t, commentBodies(rig.comments(f.id)), fmt.Sprintf(
			"Merged %s (merge commit %s) on head %s (approved at %s; same changes against the base).", flowLink, mergeSHA, headB, headA))
	})

	t.Run("a later head with different changes is not", func(t *testing.T) {
		rig := newContractRig(t)
		f := rig.gate(true)
		rig.host.configure(func(h *codehosttest.Fake) {
			h.DiffOf = map[string]*codehost.Diff{headA: diffOf(patchOne), headB: diffOf(patchTwo)}
		})
		f.review()
		f.approve()
		f.agent.mustCall("integration_lock", nil)
		f.pushed(headB, codehost.CheckSuccess)
		f.refusedMerge(jsonObject{"head_sha": headB}, "differ from the reviewed ones", "")
	})

	t.Run("an approval of another pull request covers nothing here", func(t *testing.T) {
		rig := newContractRig(t)
		f := rig.gate(true)
		res := f.agent.call("ticket_finish", jsonObject{"pr_url": "https://github.com/acme/repo/pull/99", "handoff": "Wrong link.", "request_review": true, "reason": "a person should read it"})
		require.False(t, res.isError, res.text)
		f.approve()
		f.agent.mustCall("integration_lock", nil)
		f.refusedMerge(nil, "the approval does not cover it", "")
		f.agent.mustCall("integration_unlock", nil)
		f.review()
		f.approve()
		f.agent.mustCall("integration_lock", nil)
		f.merges(nil)
	})

	t.Run("a new change drops the approval", func(t *testing.T) {
		rig := newContractRig(t)
		f := rig.gate(true)
		f.review()
		f.approve()
		require.NotNil(t, f.ticket()["approved_at"])

		rig.host.opened(codehost.ChangeRef{Host: "github.com", Owner: "acme", Repo: "repo", Number: 13})
		f.recordUnderOldName()
		require.False(t, f.open(false).isError)
		assert.Nil(t, f.ticket()["approved_at"], "an approval was of some other change")
		f.agent.mustCall("integration_lock", nil)
		f.refusedMerge(nil, "requires a human to approve its change", "")
	})
}

func checksIn(state codehost.CheckState) []codehost.Check {
	if state == codehost.CheckNone {
		return nil
	}
	return []codehost.Check{{Name: "ci", State: state}}
}

// -- review --

// review hands the change over for review: the agent loses the ticket, and
// the lock with it.
func (f *changeFlow) review() {
	f.rig.t.Helper()
	res := f.agent.call("ticket_finish", jsonObject{"pr_url": flowLink, "handoff": "Branch " + f.branch + " is pushed and ready.", "request_review": true, "reason": "a person should read it"})
	require.False(f.rig.t, res.isError, res.text)
}

// approve is the signed-in person approving on the board, after which the
// author claims the ticket again to merge it.
func (f *changeFlow) approve() {
	f.rig.t.Helper()
	f.rig.mustBoard(http.StatusOK, http.MethodPost, fmt.Sprintf("/api/tickets/%d/approve", f.id), nil)
	require.Equal(f.rig.t, f.id, grantedID(f.agent.mustCall("ticket_next", nil)))
}

func TestContractReview_HandOver(t *testing.T) {
	t.Parallel()
	rig := newContractRig(t)
	f := rig.startFlow(true)

	t.Run("a ticket that requires approval says so, and cannot be closed without it", func(t *testing.T) {
		rig.assertGolden("change/ticket_get_requires_approval", f.agent.mustCall("ticket_get", jsonObject{"ticket_id": f.id}))
		assert.Equal(t, true, f.ticket()["requires_approval"])

		done := f.agent.complete(jsonObject{"pr_url": "https://github.com/acme/repo/pull/1", "handoff": "Skipping review."})
		assert.True(t, done.isError)
		assert.Contains(t, done.text, "requires a human to approve its PR")
		assert.Equal(t, "in_progress", f.ticket()["status"])
	})

	f.opened()
	f.pushed(headA, codehost.CheckSuccess)
	f.agent.mustCall("integration_lock", nil)

	t.Run("a hand-over needs a link and a handoff", func(t *testing.T) {
		for name, args := range map[string]jsonObject{
			"no handoff": {"pr_url": flowLink, "handoff": " "},
			"not a link": {"pr_url": "not a url", "handoff": "ok"},
			"not https":  {"pr_url": "http://github.com/acme/repo/pull/12", "handoff": "ok"},
		} {
			args["request_review"], args["reason"] = true, "a person should read it"
			res := f.agent.call("ticket_finish", args)
			assert.True(t, res.isError, name)
		}
		assert.Equal(t, "in_progress", f.ticket()["status"], "the ticket stays held for a retry")
		assert.Equal(t, f.agent.name, rig.lockHeldBy())
	})

	t.Run("hands the ticket to a person and lets go of the agent, the lock and the ticket's place", func(t *testing.T) {
		res := f.agent.mustCall("ticket_finish", jsonObject{"pr_url": flowLink, "handoff": "Branch is pushed; CI is green.", "request_review": true, "reason": "a person should read it"})
		rig.assertGolden("change/ticket_finish", res)

		in := f.ticket()
		assert.Equal(t, "review", in["status"])
		assert.Equal(t, flowLink, in["pr_url"])
		assert.Nil(t, in["approved_at"])
		assert.Nil(t, rig.agentBody(f.agent.name)["lease"])
		assert.Equal(t, "", rig.lockHeldBy(), "a ticket in review does not hold the integration lock")

		bodies := commentBodies(rig.comments(f.id))
		require.Len(t, bodies, 1)
		assert.Contains(t, bodies[0], "Handed over for review at commit "+headA)

		other := rig.worker()
		assert.Zero(t, grantedID(other.mustCall("ticket_next", nil)), "a ticket in review is not in the queue")
		assert.Zero(t, grantedID(other.mustCall("ticket_next", jsonObject{"ticket_id": f.id})))
	})
}

func TestContractReview_Approved(t *testing.T) {
	t.Parallel()
	rig := newContractRig(t)
	f := rig.gate(true)
	f.review()

	approved := rig.mustBoard(http.StatusOK, http.MethodPost, fmt.Sprintf("/api/tickets/%d/approve", f.id), nil).obj(t)
	assert.Equal(t, "open", approved["status"], "an approved ticket goes back to the queue")
	assert.NotNil(t, approved["approved_at"])
	assert.Equal(t, rig.user.ID.String(), approved["approved_by_user_id"])
	assert.Nil(t, approved["changes_requested_at"])

	t.Run("the author has first claim", func(t *testing.T) {
		other := rig.worker()
		assert.Zero(t, grantedID(other.mustCall("ticket_next", nil)))
		assert.Zero(t, grantedID(other.mustCall("ticket_next", jsonObject{"ticket_id": f.id})))
		got := f.agent.mustCall("ticket_next", nil)
		assert.Equal(t, f.id, grantedID(got))
		assert.Equal(t, "approved", got.structured["ticket"].(map[string]any)["review"])
		rig.assertGolden("change/ticket_next_approved", got)
		assert.Equal(t, "in_progress", f.ticket()["status"])
	})

	t.Run("it merges and closes", func(t *testing.T) {
		f.agent.mustCall("integration_lock", nil)
		f.merges(jsonObject{"head_sha": headA})
		f.agent.mustComplete(jsonObject{"handoff": "Merged.", "pr_url": flowLink})
		assert.Equal(t, "done", f.ticket()["status"])
	})

	t.Run("approving what is not in review is refused", func(t *testing.T) {
		resp := rig.boardDo(http.MethodPost, fmt.Sprintf("/api/tickets/%d/approve", f.id), nil)
		assert.Equal(t, http.StatusConflict, resp.code, resp.raw)
		assert.Contains(t, resp.raw, "not in review")
	})
}

func TestContractReview_ChangesRequested(t *testing.T) {
	t.Parallel()
	rig := newContractRig(t)
	f := rig.gate(true)
	f.review()
	path := fmt.Sprintf("/api/tickets/%d/request_changes", f.id)

	t.Run("a person has to say what to change", func(t *testing.T) {
		resp := rig.boardDo(http.MethodPost, path, jsonObject{"comment": "  "})
		assert.Equal(t, http.StatusUnprocessableEntity, resp.code, resp.raw)
		assert.Equal(t, "review", f.ticket()["status"])
	})

	t.Run("sends it back to the queue with the comment", func(t *testing.T) {
		sent := rig.mustBoard(http.StatusOK, http.MethodPost, path, jsonObject{"comment": "Please add a test."}).obj(t)
		assert.Equal(t, "open", sent["status"])
		assert.NotNil(t, sent["changes_requested_at"])
		assert.Nil(t, sent["approved_at"])
		bodies := commentBodies(rig.comments(f.id))
		assert.Equal(t, "Please add a test.", bodies[len(bodies)-1])
	})

	t.Run("the author has first claim and is told to address it, not to merge", func(t *testing.T) {
		other := rig.worker()
		assert.Zero(t, grantedID(other.mustCall("ticket_next", nil)))
		got := f.agent.mustCall("ticket_next", nil)
		assert.Equal(t, f.id, grantedID(got))
		assert.Equal(t, "changes_requested", got.structured["ticket"].(map[string]any)["review"])
		rig.assertGolden("change/ticket_next_changes_requested", got)
	})

	t.Run("it cannot merge until a person approves a new hand-over", func(t *testing.T) {
		f.agent.mustCall("integration_lock", nil)
		f.refusedMerge(nil, "requires a human to approve its change", "")
		f.review()
		f.approve()
		assert.Nil(t, f.ticket()["changes_requested_at"], "an approval replaces the request for changes")
		f.agent.mustCall("integration_lock", nil)
		f.merges(nil)
	})

	t.Run("asking for changes to what is not in review is refused", func(t *testing.T) {
		resp := rig.boardDo(http.MethodPost, path, jsonObject{"comment": "Again."})
		assert.Equal(t, http.StatusConflict, resp.code, resp.raw)
	})
}
