package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/daemon"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/codex"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/conformance/scripted"
	"github.com/lowaa/slop-factory/slop-bot/internal/scriptedmodel"
)

// The scenarios each harness runs, end to end through the daemon. Each
// asserts on slop-bot's events and ticket store, and on what the model was
// sent: never on a harness's own output.
var tier2Scenarios = map[string]func(*t2){
	"ask, then the user's answer, then done": scenarioAskAnswerDone,
	"status set shows in status":             scenarioStatusSet,
	"a tell mid-turn reaches the same turn":  scenarioTellMidTurn,
	"ready-to-merge, tell --merge, done":     scenarioMerge,
	"attach: the TUI and back":               scenarioAttach,
	"a restart resumes the conversation":     scenarioRestart,
}

// TestTier2Scenarios runs every scenario on every harness, each with a
// daemon, model and harness of its own, so they run in parallel. Filter
// with e.g. -run 'TestTier2Scenarios/pi/attach'.
func TestTier2Scenarios(t *testing.T) {
	t.Parallel()
	scripted.OptIn(t)
	for _, tt := range tier2Targets {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bin := scripted.Binary(t, tt.bin)
			for name, scenario := range tier2Scenarios {
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					scenario(newT2(t, tt, bin))
				})
			}
		})
	}
}

// asking is the start most scenarios share: the agent's first turn asks
// the user a question, so it ends waiting for them (not idle, which would
// draw a nudge the script has no turn for).
func (b *t2) asking(title, question string, attach bool) (api.Ticket, *tier2Start, string) {
	b.t.Helper()
	b.script(b.slopBot("ask", question, "--option", "CSV", "--option", "XLSX"), reply("TIER2-ASKED: waiting for the user"))
	create := b.create
	if attach {
		create = b.createForAttach
	}
	tk, start := create(title)
	agent := b.settled("waiting_user").Agent
	return tk, start, agent
}

func scenarioAskAnswerDone(b *t2) {
	t := b.t
	tk, start, agent := b.asking("Add export", "CSV or XLSX? TIER2-QUESTION", false)
	assert.DirExists(t, start.dir, "the agent works in its ticket's worktree")
	assert.Contains(t, b.h.ok(t, "asks"), "TIER2-QUESTION")

	b.script(b.slopBot("done", "--summary", "Exported as CSV."), reply("finished"))
	b.h.ok(t, "tell", agent, "TIER2-ANSWER-CSV")
	b.finished(tk, start.dir)

	// The answer came as the agent's next turn: the first request carrying
	// it is a user message, after the ask's turn ended.
	answered, reqs := b.firstWith("TIER2-ANSWER-CSV")
	require.GreaterOrEqual(t, answered, 0, b.requests())
	// The ask's own output reached the model the request before.
	asked, _ := b.firstWith("asked. Carry on")
	assert.Greater(t, answered, asked, b.requests())
	assert.Empty(t, newResults(reqs, answered), "the answer opened a turn of its own, not a tool's result")
	assert.NotContains(t, b.h.ok(t, "asks"), "TIER2-QUESTION", "answered")
	comments := strings.Join(commentBodies(b.h.ticket(t, "ticket", "show", tk.ID)), "\n")
	assert.Contains(t, comments, "Exported as CSV.")
	// The harness's own session file, read by its transcript reader and
	// retained at the ticket's end.
	assert.Equal(t, 1, b.count("transcript.retained", tk.ID))
	assert.Contains(t, b.h.ok(t, "transcript", agent, "--full"), "TIER2-ASKED")
}

func scenarioStatusSet(b *t2) {
	t := b.t
	hold := b.long()
	b.script(b.slopBot("status", "set", "TIER2-NOTE reading the code"), b.shell(hold.Command()))
	tk, start := b.create("Status")
	// The note's call ended (the next tool started after it), and the turn
	// is held in the long command: status is read mid-turn.
	b.await("harness.tool_ended", nil)
	var st api.Status
	require.NoError(t, jsonDecode(b.h.ok(t, "status", "--json"), &st))
	require.Len(t, st.Agents, 1)
	assert.Equal(t, "TIER2-NOTE reading the code", st.Agents[0].Note)
	assert.Equal(t, b.target.Name, st.Agents[0].Harness)
	assert.Contains(t, b.h.ok(t, "status"), "TIER2-NOTE reading the code")

	b.script(b.slopBot("done", "--summary", "noted"), reply("finished"))
	require.NoError(t, hold.Release(b.ctx()))
	b.finished(tk, start.dir)
}

func scenarioTellMidTurn(b *t2) {
	t := b.t
	hold := b.long()
	b.script(b.shell(hold.Command()))
	tk, start := b.create("Steer")
	ev := b.await("harness.tool_started", nil)
	b.h.ok(t, "tell", ev.Agent, "TIER2-STEER use v2")
	// Delivered as a steer, into the running turn: only then is the tool
	// let go, so the steer is in the harness before its next model call.
	b.await("message.delivered", func(ev *api.Event) bool { return strings.Contains(string(ev.Payload), `"path":"steer"`) })
	b.script(b.slopBot("done", "--summary", "steered"), reply("finished"))
	require.NoError(t, hold.Release(b.ctx()))
	b.finished(tk, start.dir)

	// Every harness here steers (MidTurnSteer): the steer reached the model
	// in the request that delivered the long tool's result, the same turn.
	steered, reqs := b.firstWith("TIER2-STEER")
	require.GreaterOrEqual(t, steered, 0, b.requests())
	assert.Contains(t, strings.Join(newResults(reqs, steered), "\n"), hold.Marker(), "mid-turn: %s", b.requests())
}

func scenarioMerge(b *t2) {
	t := b.t
	b.script(
		b.shell(shellWords(b.gh, "pr", "merge", "7", "--squash")),
		b.slopBot("ready-to-merge", "--pr", "https://example.test/pr/7"),
		reply("ready for the user"),
	)
	tk, start := b.create("Merge")
	agent := b.settled("waiting_merge").Agent
	assert.Contains(t, b.h.ok(t, "inbox"), "ready to merge (https://example.test/pr/7)")
	results := b.lastResults()
	require.NotEmpty(t, results, b.requests())
	require.True(t, b.target.Guarded, "every harness here is guarded")
	assert.NotContains(t, results[0], "GH-STUB-RAN", "unauthorised, the merge never ran")
	assert.Equal(t, 1, b.count(daemon.EventMergeRefused, tk.ID), "the refusal is in the event log")
	if b.target.Name == codex.Name {
		// Codex has no hook to answer with under app-server: the guard
		// declines the command's approval and steers its reason in, so the
		// model reads it in the same turn, beside the declined result.
		refused, reqs := b.firstWith("merge guard refused this")
		require.GreaterOrEqual(t, refused, 0, b.requests())
		assert.NotContains(t, strings.Join(reqs[refused].ToolResults, "\n"), "merge guard refused this", "a steer, not a tool result")
		assert.NotEmpty(t, newResults(reqs, refused), "in the turn the declined command's result came back in: %s", b.requests())
	} else {
		assert.Contains(t, results[0], "merge guard refused this", "the hook's reason is the tool's result")
	}

	b.script(b.shell(shellWords(b.gh, "pr", "merge", "7", "--squash")), b.slopBot("done", "--summary", "Merged."), reply("finished"))
	assert.Contains(t, b.h.ok(t, "tell", agent, "--merge"), "told 1")
	b.finished(tk, start.dir)
	authorised, _ := b.firstWith("authorised to merge")
	require.GreaterOrEqual(t, authorised, 0, b.requests())
	results = b.lastResults()
	var merged bool
	for _, r := range results[1:] {
		merged = merged || strings.Contains(r, "GH-STUB-RAN: pr merge 7 --squash")
	}
	assert.True(t, merged, "authorised, the merge ran: %q", results)
}

func scenarioAttach(b *t2) {
	t := b.t
	tk, start, agent := b.asking("Attach", "CSV or XLSX?", true)
	term := b.attachTerminal(agent)
	b.await("agent.tui_launched", nil)
	// Held for the hand-back: the person is in the TUI.
	b.h.ok(t, "tell", agent, "TIER2-HELD while you were out")
	// The TUI resumed the agent's own conversation: it shows its history.
	at := b.tui.opened(b.ctx(), term, "TIER2-ASKED")
	// Scripted before the quit: the hand-back's turn follows it at once.
	b.script(b.slopBot("done", "--summary", "after the attach"), reply("finished"))
	b.tui.quitAt(b.ctx(), term, at)
	code, screen := term.done(b.ctx())
	assert.Equal(t, 0, code, screen)
	assert.Contains(t, screen, "back to slop-bot: resumed")
	resumed := b.started(true)
	assert.Equal(t, start.id, resumed.id, "programmatic again, on the same conversation")
	b.finished(tk, start.dir)

	handBack, reqs := b.firstWith("A person worked with you directly")
	require.GreaterOrEqual(t, handBack, 0, b.requests())
	assert.Contains(t, reqs[handBack].MessageText, "TIER2-HELD", "the held message, at the hand-back")
	assert.Contains(t, reqs[handBack].MessageText, "TIER2-ASKED", "with the conversation's history")
}

func scenarioRestart(b *t2) {
	t := b.t
	tk, start, agent := b.asking("Restart", "CSV or XLSX? TIER2-BEFORE-RESTART", false)
	b.stop()
	b.stop = startDaemon(t, b.h)
	resumed := b.started(true)
	assert.Equal(t, start.id, resumed.id, "resumed by id")
	b.await("agent.resumed", nil)
	assert.Contains(t, b.h.ok(t, "status"), "waiting_user", "it goes on waiting for its answer")

	// The resumed agent's own calls work on the token it was given anew.
	b.script(b.slopBot("done", "--summary", "after the restart"), reply("finished"))
	b.h.ok(t, "tell", agent, "TIER2-ANSWER-AFTER")
	b.finished(tk, start.dir)
	answered, reqs := b.firstWith("TIER2-ANSWER-AFTER")
	require.GreaterOrEqual(t, answered, 0, b.requests())
	assert.Contains(t, reqs[answered].MessageText, "TIER2-ASKED", "the resumed conversation has its history")
}

// newResults are the tool results request i of reqs adds to the one before.
func newResults(reqs []*scriptedmodel.Request, i int) []string {
	if i == 0 {
		return reqs[0].ToolResults
	}
	return reqs[i].ToolResults[min(len(reqs[i-1].ToolResults), len(reqs[i].ToolResults)):]
}

func commentBodies(tk api.Ticket) []string {
	out := make([]string, len(tk.Comments))
	for i, c := range tk.Comments {
		out[i] = c.Body
	}
	return out
}
