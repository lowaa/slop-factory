package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/codex/protocol"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/mergeguard"
)

// fakeGuard answers each Check with the next of verdicts (an allow once
// they run out), and records what it was asked.
type fakeGuard struct {
	mu       sync.Mutex
	verdicts []*mergeguard.Verdict
	calls    []*mergeguard.Call
	by       []ident.Principal
	// during, if set, runs inside the nth Check (from 1), as a race would.
	during func(n int)
}

func (g *fakeGuard) Check(_ context.Context, by ident.Principal, call *mergeguard.Call) *mergeguard.Verdict {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, call)
	g.by = append(g.by, by)
	if g.during != nil {
		g.during(len(g.calls))
	}
	if len(g.verdicts) == 0 {
		return &mergeguard.Verdict{Allow: true}
	}
	v := g.verdicts[0]
	g.verdicts = g.verdicts[1:]
	return v
}

const refusal = "slop-bot's merge guard refused this: it looks like a merge (gh pr merge)"

// guardedFixture is a permissive agent's codec with a guard, opened on the
// recorded start.
func guardedFixture(t *testing.T, g *fakeGuard) *fixture {
	t.Helper()
	ws := &harness.Workspace{Agent: "a1", Ticket: "7", Dir: "/ws", Permissive: true, Briefing: "brief"}
	f := newFixture(t, (&Harness{Guard: g}).codecOptions(ws, nil))
	f.open()
	return f
}

// answers are the codec's replies to Codex's server requests, by request
// id.
func (f *fixture) answers() map[string]string {
	out := map[string]string{}
	for _, m := range f.p.sent {
		if m.Method == "" && m.ID != nil {
			out[string(m.ID)] = string(m.Result)
		}
	}
	return out
}

// TestAGuardedTurn plays a recorded guarded turn (testdata/turn_guarded):
// Codex asks about `ls`, which the guard allows, then about `gh pr merge`,
// which it refuses.
func TestAGuardedTurn(t *testing.T) {
	t.Parallel()
	g := &fakeGuard{verdicts: []*mergeguard.Verdict{{Allow: true}, {Reason: refusal}}}
	f := guardedFixture(t, g)
	var start protocol.ThreadStartParams
	f.p.last(protocol.MethodThreadStart, &start)
	assert.Equal(t, protocol.AskForApprovalUntrusted, start.ApprovalPolicy)
	assert.Equal(t, protocol.ApprovalsReviewerUser, start.ApprovalsReviewer)

	f.send("go")
	evs := newPlayer(t, f.p, "turn_guarded").decode(f.c, f.w, nil)

	require.Len(t, g.calls, 2, "each command was put to the guard")
	assert.Equal(t, &mergeguard.Call{Tool: "exec_command", Command: "/bin/bash -lc ls", Cwd: "/ws"}, g.calls[0])
	assert.Equal(t, &mergeguard.Call{Tool: "exec_command", Command: "/bin/bash -lc 'gh pr merge 12 --squash'", Cwd: "/ws"}, g.calls[1])
	assert.Equal(t, []ident.Principal{ident.AgentPrincipal("a1", "7"), ident.AgentPrincipal("a1", "7")}, g.by, "as the agent")

	assert.Equal(t, map[string]string{
		"0": `{"decision":"accept"}`,
		"1": `{"decision":"decline"}`,
	}, f.answers(), "ls approved, the merge declined, each by its own request id")

	var steer protocol.TurnSteerParams
	f.p.last(protocol.MethodTurnSteer, &steer)
	assert.Equal(t, refusal, steer.Input[0].Text, "the guard's reason, which a decline can't carry, is steered into the turn")
	assert.Equal(t, thread, steer.ThreadID)

	assert.Len(t, of(evs, harness.ToolStarted), 2)
	assert.Len(t, of(evs, harness.ToolEnded), 2)
	ended := of(evs, harness.TurnEnded)
	require.Len(t, ended, 1)
	assert.Equal(t, harness.TurnCompleted, ended[0].Outcome)
}

func TestTheGuardIsAskedAboutCommandsOnly(t *testing.T) {
	t.Parallel()
	g := &fakeGuard{verdicts: []*mergeguard.Verdict{{Reason: refusal}}}
	f := guardedFixture(t, g)
	for _, m := range []string{protocol.MethodItemFileChangeRequestApproval, protocol.MethodApplyPatchApproval, protocol.MethodItemPermissionsRequestApproval, protocol.MethodMcpServerElicitationRequest} {
		_, err := f.c.Decode([]byte(`{"id":5,"method":"`+m+`","params":{"permissions":{}}}`), f.w)
		require.NoError(t, err)
	}
	_, err := f.c.Decode([]byte(`{"method":"`+protocol.MethodItemCommandExecutionRequestApproval+`","params":{"command":"gh pr merge"}}`), f.w)
	require.NoError(t, err)
	assert.Empty(t, g.calls, "a patch, a permission or a notification isn't a command to run")
}

// TestAnUnreadableApprovalIsDeclined: the guard can't classify a command
// it can't read, and approving it would run that command unscreened, so a
// guarded agent fails closed.
func TestAnUnreadableApprovalIsDeclined(t *testing.T) {
	t.Parallel()
	declined := `{"decision":"decline"}`
	denied := `{"decision":{"denied":{"rejection":"` + unreadable + `"}}}`
	for name, tc := range map[string]struct{ line, want string }{
		"a command that isn't a string": {`{"id":9,"method":"` + protocol.MethodItemCommandExecutionRequestApproval + `","params":{"command":7}}`, declined},
		"no params":                     {`{"id":9,"method":"` + protocol.MethodItemCommandExecutionRequestApproval + `"}`, declined},
		"a legacy argv that isn't one":  {`{"id":9,"method":"` + protocol.MethodExecCommandApproval + `","params":{"command":"gh pr merge"}}`, denied},
		"a legacy request, no params":   {`{"id":9,"method":"` + protocol.MethodExecCommandApproval + `","params":null}`, denied},
		"a legacy request, no command":  {`{"id":9,"method":"` + protocol.MethodExecCommandApproval + `","params":{"command":[]}}`, denied},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			g := &fakeGuard{}
			f := guardedFixture(t, g)
			_, err := f.c.Decode([]byte(tc.line), f.w)
			require.NoError(t, err)
			assert.Empty(t, g.calls, "nothing to classify")
			assert.JSONEq(t, tc.want, f.answers()["9"])
		})
	}
}

// TestALegacyCommandApprovalIsGuarded: execCommandApproval is the protocol's
// other command approval (the legacy turn APIs'). It carries argv, which is
// put to the guard as the shell line it would be.
func TestALegacyCommandApprovalIsGuarded(t *testing.T) {
	t.Parallel()
	g := &fakeGuard{verdicts: []*mergeguard.Verdict{{Reason: refusal}, {Allow: true}}}
	f := guardedFixture(t, g)
	_, err := f.c.Decode([]byte(`{"id":3,"method":"execCommandApproval","params":{"callId":"c","conversationId":"`+thread+
		`","cwd":"/ws/sub","parsedCmd":[],"command":["/bin/bash","-lc","gh pr merge 12 --squash"]}}`), f.w)
	require.NoError(t, err)
	_, err = f.c.Decode([]byte(`{"id":4,"method":"execCommandApproval","params":{"callId":"d","conversationId":"`+thread+
		`","cwd":"/ws","parsedCmd":[],"command":["ls"]}}`), f.w)
	require.NoError(t, err)
	require.Len(t, g.calls, 2)
	assert.Equal(t, &mergeguard.Call{Tool: shellTool, Command: "/bin/bash -lc 'gh pr merge 12 --squash'", Cwd: "/ws/sub"}, g.calls[0])
	assert.JSONEq(t, `{"decision":{"denied":{"rejection":"`+refusal+`"}}}`, f.answers()["3"], "the legacy denial carries the reason")
	assert.Equal(t, `{"decision":"approved"}`, f.answers()["4"])
}

// approvalLine is a command approval Codex would send for thread and turn.
func approvalLine(t *testing.T, id int, threadID, turnID string) []byte {
	t.Helper()
	params, err := json.Marshal(&protocol.CommandExecutionRequestApprovalParams{Command: "gh pr merge", ItemID: "i", ThreadID: threadID, TurnID: turnID})
	require.NoError(t, err)
	return []byte(fmt.Sprintf(`{"id":%d,"method":"%s","params":%s}`, id, protocol.MethodItemCommandExecutionRequestApproval, params))
}

// TestARefusalOutsideTheRunningTurnIsOnlyDeclined: steered anywhere but the
// turn that asked, the reason would start a turn of its own, or reach the
// wrong agent.
func TestARefusalOutsideTheRunningTurnIsOnlyDeclined(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		running        bool
		thread, turnID string
	}{
		"no turn running":       {false, thread, "t"},
		"another thread's turn": {true, "00000000-0000-7000-8000-0000000000ff", "t"},
		"an earlier turn":       {true, thread, "t-old"},
		"no turn named":         {true, thread, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := guardedFixture(t, &fakeGuard{verdicts: []*mergeguard.Verdict{{Reason: refusal}}})
			if tc.running {
				_, err := f.c.Decode([]byte(`{"method":"turn/started","params":{"threadId":"`+thread+`","turn":{"id":"t","items":[],"status":"inProgress"}}}`), f.w)
				require.NoError(t, err)
			}
			sent := len(f.p.sent)
			_, err := f.c.Decode(approvalLine(t, 4, tc.thread, tc.turnID), f.w)
			require.NoError(t, err)
			assert.Equal(t, `{"decision":"decline"}`, f.answers()["4"])
			assert.Len(t, f.p.sent, sent+1, "the decline, and no turn/steer or turn/start: %v", f.p.methods())
		})
	}
}

// TestARefusalRacingAnInterruptDoesntOutliveIt: the interrupt lands while
// the guard decides (screen runs outside mu), so the turn is being
// interrupted when the refusal is answered. The reason must not be queued
// into a new turn, which would also hold back the interrupted turn's end.
func TestARefusalRacingAnInterruptDoesntOutliveIt(t *testing.T) {
	t.Parallel()
	g := &fakeGuard{verdicts: []*mergeguard.Verdict{{Allow: true}, {Reason: refusal}}}
	f := guardedFixture(t, g)
	g.during = func(n int) {
		if n == 2 {
			require.NoError(t, f.c.Interrupt(context.Background(), f.w))
		}
	}
	f.send("go")
	pl := newPlayer(t, f.p, "turn_guarded")
	var turn string
	pl.decode(f.c, f.w, func(line []byte) bool {
		var m struct {
			ID     json.RawMessage                                `json:"id"`
			Method string                                         `json:"method"`
			Params protocol.CommandExecutionRequestApprovalParams `json:"params"`
		}
		require.NoError(t, json.Unmarshal(line, &m))
		turn = m.Params.TurnID
		return m.Method == protocol.MethodItemCommandExecutionRequestApproval && string(m.ID) == "1"
	})
	assert.Equal(t, `{"decision":"decline"}`, f.answers()["1"])
	assert.NotContains(t, f.p.methods(), protocol.MethodTurnSteer)

	evs, err := f.c.Decode([]byte(`{"method":"turn/completed","params":{"threadId":"`+thread+`","turn":{"id":"`+turn+`","items":[],"status":"interrupted"}}}`), f.w)
	require.NoError(t, err)
	ended := of(evs, harness.TurnEnded)
	require.Len(t, ended, 1, "the interrupted turn ends")
	assert.Equal(t, harness.TurnInterrupted, ended[0].Outcome)
	starts := 0
	for _, m := range f.p.methods() {
		if m == protocol.MethodTurnStart {
			starts++
		}
	}
	assert.Equal(t, 1, starts, "no turn started for the reason")
}
