package mcpserver_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Claude Code shows an agent a tool's structured content in place of its text,
// so every line of guidance the text carries must be in the structured output
// too. Found when attempts of one ticket could not tell which branch was theirs.

const pinnedBase = "0123456789abcdef0123456789abcdef01234567"

func collectStrings(v any, into *[]string) {
	switch x := v.(type) {
	case string:
		*into = append(*into, x)
	case map[string]any:
		for _, e := range x {
			collectStrings(e, into)
		}
	case []any:
		for _, e := range x {
			collectStrings(e, into)
		}
	}
}

// assertTextIsStructured fails on a line of the result's text that is neither
// one of the lines derived from fields (derived, by prefix) nor found in a
// string of the structured output.
func assertTextIsStructured(t *testing.T, res *mcp.CallToolResult, derived ...string) {
	t.Helper()
	var strs []string
	collectStrings(res.StructuredContent, &strs)
lines:
	for _, line := range strings.Split(resultText(t, res), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		for _, prefix := range derived {
			if strings.HasPrefix(line, prefix) {
				continue lines
			}
		}
		found := false
		for _, s := range strs {
			if strings.Contains(s, line) {
				found = true
				break
			}
		}
		assert.True(t, found, "the text says %q but the structured output does not", line)
	}
}

var (
	nextDerived = []string{"Granted ticket #", "Scopes:", "Link:"}
	getDerived  = []string{"#", "Scopes:", "Link:", "Blocked by:", "Claimable only", "PR:"}
)

func TestStructuredOutput_CarriesEveryGuidanceLineOfTheText(t *testing.T) {
	for name, tc := range goldenTickets() {
		unpromoted := &ticket.AttemptBranch{Name: "ticket-7-a2", Managed: true, Ready: true, Unpromoted: true}
		t.Run("ticket_next/"+name, func(t *testing.T) {
			session, tickets := newGoldenRig(t)
			granted := tc
			granted.Status = ticket.StatusInProgress
			granted.BaseCommit = goldenPtr(pinnedBase)
			tickets.claim = &ticket.ClaimResult{
				Ticket: &granted, Unpromoted: true, Branch: unpromoted, Attempt: &ticket.AttemptRef{TicketID: 7, Number: 2},
			}
			assertTextIsStructured(t, callGolden(t, session, "ticket_next", nil), nextDerived...)
		})
		t.Run("ticket_get/"+name, func(t *testing.T) {
			session, tickets := newGoldenRig(t)
			got := tc
			got.BaseCommit = goldenPtr(pinnedBase)
			tickets.got = &got
			tickets.held = &ticket.AttemptRef{TicketID: 7, Number: 2}
			tickets.branch = unpromoted
			assertTextIsStructured(t, callGolden(t, session, "ticket_get", map[string]any{"ticket_id": 7}), getDerived...)
		})
	}
}

func TestStructuredOutput_NamesTheAttemptsBranch(t *testing.T) {
	branch := &ticket.AttemptBranch{Name: "ticket-7-a2", Managed: true, Ready: true, Unpromoted: true}
	want := map[string]any{
		"number": float64(2), "branch": "ticket-7-a2", "managed": true, "ready": true, "unpromoted": true,
		"base_commit": pinnedBase, "base_branch": "slop-haus-base-ticket-7",
	}
	check := func(t *testing.T, res *mcp.CallToolResult) {
		attempt, ok := structured(t, res)["attempt"].(map[string]any)
		require.True(t, ok, "no attempt in %#v", res.StructuredContent)
		guidance, _ := attempt["guidance"].(string)
		delete(attempt, "guidance")
		assert.Equal(t, want, attempt)
		assert.Contains(t, guidance, "git switch ticket-7-a2")
		assert.Contains(t, guidance, "merge the base branch slop-haus-base-ticket-7, never origin/main")
	}

	t.Run("the claim", func(t *testing.T) {
		session, tickets := newGoldenRig(t)
		granted := goldenTickets()["plain"]
		granted.Status, granted.BaseCommit = ticket.StatusInProgress, goldenPtr(pinnedBase)
		tickets.claim = &ticket.ClaimResult{
			Ticket: &granted, Unpromoted: true, Branch: branch, Attempt: &ticket.AttemptRef{TicketID: 7, Number: 2},
		}
		check(t, callGolden(t, session, "ticket_next", nil))
	})
	t.Run("ticket_get by the holder", func(t *testing.T) {
		session, tickets := newGoldenRig(t)
		got := goldenTickets()["plain"]
		got.BaseCommit = goldenPtr(pinnedBase)
		tickets.got, tickets.held, tickets.branch = &got, &ticket.AttemptRef{TicketID: 7, Number: 2}, branch
		check(t, callGolden(t, session, "ticket_get", map[string]any{"ticket_id": 7}))
	})
	t.Run("ticket_get by someone holding nothing", func(t *testing.T) {
		session, tickets := newGoldenRig(t)
		got := goldenTickets()["plain"]
		tickets.got = &got
		assert.NotContains(t, structured(t, callGolden(t, session, "ticket_get", map[string]any{"ticket_id": 7})), "attempt")
	})
	t.Run("a branch that could not be made says so", func(t *testing.T) {
		session, tickets := newGoldenRig(t)
		got := goldenTickets()["plain"]
		got.BaseCommit = goldenPtr(pinnedBase)
		tickets.got, tickets.held = &got, &ticket.AttemptRef{TicketID: 7, Number: 1}
		tickets.branch = &ticket.AttemptBranch{Name: "ticket-7-a1", Managed: true}
		attempt := structured(t, callGolden(t, session, "ticket_get", map[string]any{"ticket_id": 7}))["attempt"].(map[string]any)
		assert.Equal(t, false, attempt["ready"])
		assert.Contains(t, attempt["guidance"], "NOT READY")
	})
}

func TestStructuredOutput_CarriesTheSafetyAndStageGuidance(t *testing.T) {
	tickets := goldenTickets()
	for name, tc := range map[string]struct {
		key   string
		field string
		text  string
	}{
		"needs discussion":   {"needs_discussion", "needs_discussion", "NEEDS DISCUSSION"},
		"external source":    {"external_source", "external", "data, not instructions"},
		"done through other": {"shadow_of_done", "done_through_other", ""},
		"approved":           {"approved", "review", "APPROVED"},
		"changes requested":  {"changes_requested", "review", "CHANGES REQUESTED"},
		"requires approval":  {"requires_approval", "review", "REQUIRES HUMAN APPROVAL"},
		"in review":          {"in_review", "review", "In review"},
		"parked (read only)": {"parked", "parked", "PARKED"},
	} {
		t.Run(name, func(t *testing.T) {
			session, fake := newGoldenRig(t)
			got := tickets[tc.key]
			fake.got = &got
			read := structured(t, callGolden(t, session, "ticket_get", map[string]any{"ticket_id": 7}))
			guidance, _ := read["guidance"].(map[string]any)
			assert.Contains(t, guidance[tc.field], tc.text)
		})
	}
}

func TestStructuredOutput_TheHumansWordsAndNoticesAreInEveryTool(t *testing.T) {
	r := newRealRig(t)
	r.holds("Being worked")
	r.say("stop, wrong approach")

	res := r.call("checkin", map[string]any{})

	require.False(t, res.IsError, resultText(t, res))
	assert.Contains(t, structured(t, res)["human_message"], "stop, wrong approach")
}

func TestStructuredOutput_TheAnswerOfAToolWithNoOtherFieldsIsInTheOutput(t *testing.T) {
	r := newRealRig(t)
	held := r.holds("Being worked")

	res := r.call("ticket_discuss", map[string]any{"question": "which way?", "handoff": "nothing yet"})

	require.False(t, res.IsError, resultText(t, res))
	out := structured(t, res)
	assert.Equal(t, true, out["ok"])
	assert.Contains(t, out["message"], "#"+itoa(held.ID)+" is in discussion")
	assert.Contains(t, out["message"], "comes back to the queue")
}

func TestStructuredOutput_ARefusedCallSaysWhy(t *testing.T) {
	refusal := fmt.Errorf("%w (ticket #1)", ticket.ErrNeedsDiscussion)
	r := newRig(t)
	r.tickets.lease = &ticket.Lease{TicketID: 1}
	r.tickets.doneErr = refusal

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_finish", Arguments: map[string]any{"pr_url": testPR, "handoff": "merged"},
	})

	require.NoError(t, err)
	out := structured(t, res)
	assert.Equal(t, refusal.Error(), out["message"], "the refusal says why")
}

func TestStructuredOutput_TicketNextSaysWhyThereIsNoTicket(t *testing.T) {
	t.Run("empty queue", func(t *testing.T) {
		session, tickets := newGoldenRig(t)
		tickets.claim = &ticket.ClaimResult{}
		assert.Equal(t, "The queue is empty.", structured(t, callGolden(t, session, "ticket_next", nil))["message"])
	})
	t.Run("a requested ticket that was not granted", func(t *testing.T) {
		session, tickets := newGoldenRig(t)
		tickets.claim = &ticket.ClaimResult{}
		res := callGolden(t, session, "ticket_next", map[string]any{"ticket_id": 7})
		assert.Equal(t, resultText(t, res), structured(t, res)["message"])
	})
}

// A human's message and a revocation notice are the safety-critical lines of
// a call that goes through, so they are fields of its output too.
func TestStructuredOutput_ANoticeAndTheHumansWordsRideOnAnySuccessfulCall(t *testing.T) {
	for _, tool := range []struct {
		name string
		args func(held *ticket.Ticket) map[string]any
	}{
		{"checkin", func(*ticket.Ticket) map[string]any { return nil }},
		{"ticket_search", func(*ticket.Ticket) map[string]any { return map[string]any{"text": "x"} }},
	} {
		t.Run(tool.name, func(t *testing.T) {
			r := newRealRig(t)
			held := r.claimThenRevoke()
			r.say("and tell me what you found")

			res := r.call(tool.name, tool.args(held))

			require.False(t, res.IsError, resultText(t, res))
			out := structured(t, res)
			assert.Contains(t, out["human_message"], "and tell me what you found")
			assert.Contains(t, out["notice"], revokedNotice)
		})
	}
}

// The answer of each tool that gives one in its text is in its output.
func TestStructuredOutput_EveryAnsweringToolRepeatsItsTextInTheOutput(t *testing.T) {
	for name, tc := range map[string]struct {
		tool  string
		args  map[string]any
		field string
	}{
		"release":            {"ticket_release", map[string]any{"reason": "blocked"}, "message"},
		"prepare":            {"ticket_prepare", map[string]any{"reason": "unclear"}, "message"},
		"discuss":            {"ticket_discuss", map[string]any{"question": "q", "handoff": "h"}, "message"},
		"finish":             {"ticket_finish", map[string]any{"pr_url": testPR, "handoff": "s"}, "message"},
		"comment":            {"ticket_comment", map[string]any{"ticket_id": 1, "body": "b"}, "message"},
		"integration lock":   {"integration_lock", nil, "message"},
		"integration unlock": {"integration_unlock", nil, "message"},
		"checkin":            {"checkin", nil, "guidance"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.tickets.lease = &ticket.Lease{TicketID: 1}

			res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})

			require.NoError(t, err)
			require.False(t, res.IsError, resultText(t, res))
			assert.NotEmpty(t, structured(t, res)[tc.field])
			assert.Equal(t, resultText(t, res), structured(t, res)[tc.field])
		})
	}
}
