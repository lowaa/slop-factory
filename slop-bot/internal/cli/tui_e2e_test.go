package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/daemon"
	sbharness "github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost/tmuxtest"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/tuidriver"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
)

// tuiE2E is an e2e on fake/tui configured as agy: the daemon's own tmux
// server (one per test) and hook hub, with the fake's hooks relayed by
// `slop-bot hook tui`, the test binary run as the CLI.
type tuiE2E struct {
	*e2e
	host *sessionhost.Tmux
}

// e2eTUIHarness gives the fake's session the test's home and runtime dir,
// as e2eHarness does, so its slop-bot calls and hooks reach this daemon.
type e2eTUIHarness struct {
	*fake.TUIHarness
	h *harness
}

func (e *e2eTUIHarness) Start(ctx context.Context, ws *sbharness.Workspace, resume *sbharness.ConversationRef) (sbharness.Conversation, error) {
	ws.Env[fakeReexec] = "1"
	ws.Env["GORACE"] = "atexit_sleep_ms=0"
	ws.Env["HOME"] = e.h.home
	ws.Env["XDG_RUNTIME_DIR"] = e.h.runtime
	return e.TUIHarness.Start(ctx, ws, resume)
}

// cliHub runs the daemon's hook command as the CLI: the session's
// environment makes the test binary the fake, and this makes it slop-bot.
type cliHub struct{ tuidriver.HookHub }

func (c cliHub) Command(session string) []string {
	return append([]string{"/usr/bin/env", fakeReexec + "=", cliReexec + "=1"}, c.HookHub.Command(session)...)
}

func newTUIE2E(t *testing.T) *tuiE2E {
	t.Helper()
	return newTUIE2EWith(t, "")
}

// newTUIE2EWith is newTUIE2E with more config.
func newTUIE2EWith(t *testing.T, cfg string) *tuiE2E {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	h := newHarness(t)
	require.NoError(t, os.WriteFile(h.config, []byte("user: ada\ndefault_harness: agy\nharnesses: {agy: {}}\n"+cfg), 0o600))
	b := make([]byte, 6)
	_, err := rand.Read(b)
	require.NoError(t, err)
	h.deps.tmuxSocket = "sbt-" + hex.EncodeToString(b)
	// The test's view of the daemon's server, which it kills at the end.
	host := tmuxtest.Host(t, &sessionhost.TmuxOptions{Socket: h.deps.tmuxSocket})
	ctl, err := fake.NewController()
	require.NoError(t, err)
	self, err := os.Executable()
	require.NoError(t, err)
	h.deps.tuiHarnesses = func(kit *tuidriver.Sessions) daemon.Harnesses {
		s := *kit
		s.Hub = cliHub{kit.Hub}
		s.Logger = slog.New(slog.DiscardHandler)
		// No screen fallback: the fake's hooks say everything, and a
		// screen read under load mustn't end a turn the test holds open.
		s.PollInterval = time.Hour
		return daemon.Harnesses{"agy": &e2eTUIHarness{
			TUIHarness: &fake.TUIHarness{Sessions: s, Binary: self, Control: ctl.Socket()}, h: h,
		}}
	}
	stop := startDaemon(t, h)
	t.Cleanup(func() { _ = ctl.Close() })
	return &tuiE2E{e2e: &e2e{t: t, h: h, ctl: ctl, self: self, stop: stop}, host: host}
}

// spawned files an assigned ticket and returns its agent's session, which
// has been pointed at its inbox for the kick-off, and the agent.
func (e *tuiE2E) spawned(title string) (*fake.Session, string) {
	e.t.Helper()
	e.h.ticket(e.t, "ticket", "create", "--title", title, "--assign")
	s, err := e.ctl.Next(e.ctx())
	require.NoError(e.t, err)
	require.True(e.t, s.TUI)
	agent := e.await("agent.spawned", nil).Agent
	pointer, err := s.Await(e.ctx(), fake.RepUser)
	require.NoError(e.t, err)
	assert.Equal(e.t, fake.ViaKeys, pointer.Via)
	assert.Contains(e.t, pointer.Text, "You have a new message: run `"+e.self+" inbox`")
	assert.NotContains(e.t, pointer.Text, "assigned ticket", "a pointer, not the text")
	return s, agent
}

func (e *tuiE2E) session(agent string) string { return fake.SessionName(ident.AgentID(agent)) }

// joined attaches a terminal to agent, and returns it once the person sees
// the agent's session in it: tmux has counted them attached by the time it
// draws the session for them.
func (e *tuiE2E) joined(agent string) *terminal {
	e.t.Helper()
	term := e.attachTerminal(agent)
	e.await("agent.attached", nil)
	term.shows(e.ctx(), "[fake] ")
	st, err := e.host.Status(e.ctx(), e.session(agent))
	require.NoError(e.t, err)
	require.Equal(e.t, 1, st.Attached)
	return term
}

func TestATUIAgentWorksATicketThroughTheDaemon(t *testing.T) {
	t.Parallel()
	e := newTUIE2E(t)
	s, agent := e.spawned("Add export")

	// Following its pointer, the agent finds its kick-off in the inbox,
	// once.
	inbox := e.agent(s, "inbox").Stdout
	assert.Contains(t, inbox, `assigned ticket 1: "Add export"`)
	assert.NotContains(t, e.agent(s, "inbox").Stdout, "Add export", "read")

	// Its hooks reach the daemon through `slop-bot hook tui`.
	e.do(s, fake.CmdTurnStart)
	e.await("agent.state", toState("refining"))
	e.do(s, fake.CmdTurnEnd)
	e.await("agent.nudged", nil)
	_, err := s.Await(e.ctx(), fake.RepUser)
	require.NoError(t, err, "the nudge's pointer")
	e.do(s, fake.CmdTurnStart)
	e.agent(s, "done", "--summary", "Exported.")
	e.do(s, fake.CmdTurnEnd)
	e.await("agent.finished", nil)
	_, err = s.Await(e.ctx(), fake.RepQuit)
	require.NoError(t, err, "the session is quit, not killed")
	names, err := e.host.List(e.ctx())
	require.NoError(t, err)
	assert.NotContains(t, names, e.session(agent))
}

func TestARestartedDaemonAdoptsTheSessionMidTurn(t *testing.T) {
	t.Parallel()
	e := newTUIE2E(t)
	s, agent := e.spawned("t")
	e.do(s, fake.CmdTurnStart)
	e.await("agent.state", toState("refining"))
	// Taken for the turn, waiting for its next step's hook, when the daemon
	// stops.
	e.h.ok(t, "tell", agent, "use the v2 API")
	e.await("message.delivered", nil)

	e.stop()
	names, err := e.host.List(e.ctx())
	require.NoError(t, err)
	assert.Contains(t, names, e.session(agent), "the session outlives the daemon")
	e.restart()
	resumed := e.await("agent.resumed", nil)
	assert.JSONEq(t, `{"adopted":true}`, string(resumed.Payload))

	// The same session, its turn running on: the new daemon hears its
	// hooks, and steers the restart's note into the turn: handed to the kit
	// mid-turn, it goes out on the next step's hook.
	e.await("message.delivered", nil)
	e.do(s, fake.CmdStep)
	note, err := s.Await(e.ctx(), fake.RepUser)
	require.NoError(t, err)
	assert.Equal(t, fake.ViaHook, note.Via)
	assert.Contains(t, note.Text, "slop-bot restarted while you worked")
	assert.Contains(t, note.Text, "use the v2 API", "what the old daemon held isn't lost")
	e.agent(s, "done", "--summary", "ok")
	e.do(s, fake.CmdTurnEnd)
	e.await("agent.finished", nil)
}

func TestAJoinedAttachHoldsMessagesAndDetachHandsBack(t *testing.T) {
	t.Parallel()
	e := newTUIE2E(t)
	s, agent := e.spawned("Add export")
	e.agent(s, "inbox")
	e.do(s, fake.CmdTurnStart)
	e.agent(s, "ask", "CSV or XLSX?")
	e.do(s, fake.CmdTurnEnd)
	e.await("agent.state", toState("waiting_user"))

	term := e.joined(agent)
	assert.Contains(t, term.screen(), "Joining the agent's running session")
	// The person's turn, typed in the joined session, leaves it attached
	// (no state change, no nudge at its end); a message sent meanwhile is
	// held, not typed into their session.
	term.typeLine("hello from a person")
	typed, err := s.Await(e.ctx(), fake.RepUser)
	require.NoError(t, err)
	assert.Equal(t, "hello from a person", typed.Text)
	e.do(s, fake.CmdTurnStart)
	e.do(s, fake.CmdTurnEnd)
	e.h.ok(t, "tell", agent, "CSV")
	assert.Contains(t, e.h.ok(t, "status"), "attached")

	assert.Contains(t, e.h.ok(t, "detach", agent), "handed back: resumed")
	code, screen := term.done(e.ctx())
	assert.Equal(t, 0, code, screen)

	// The held answer and the hand-back's note go to the same session, as
	// one pointer, once the person has gone.
	pointer, err := s.Await(e.ctx(), fake.RepUser)
	require.NoError(t, err)
	assert.Contains(t, pointer.Text, "You have 2 new messages")
	inbox := e.agent(s, "inbox").Stdout
	assert.Contains(t, inbox, "CSV")
	assert.Contains(t, inbox, "A person worked with you directly")
	assert.Zero(t, e.count("agent.nudged", "1"), "the person's turn wasn't the agent stopping")
}

func TestDetachingMidTurnGivesTheSlotBackAtTheTurnsEnd(t *testing.T) {
	t.Parallel()
	e := newTUIE2EWith(t, "concurrency: {max_active: 1, max_live: 4}\n")
	s, agent := e.spawned("first")
	e.agent(s, "inbox")
	e.do(s, fake.CmdTurnStart)
	term := e.joined(agent)
	assert.Contains(t, e.h.ok(t, "detach", agent), "handed back: resumed")
	_, _ = term.done(e.ctx())

	// The turn the person watched runs on, and ends waiting: the only slot
	// is free again, and new work gets it. The hand-back's note goes into
	// it at its end, which keeps it going.
	e.do(s, fake.CmdTurnEnd)
	note, err := s.Await(e.ctx(), fake.RepUser)
	require.NoError(t, err)
	assert.Equal(t, fake.ViaHook, note.Via)
	e.agent(s, "ask", "CSV or XLSX?")
	e.do(s, fake.CmdTurnEnd)
	e.await("agent.state", toState("waiting_user"))
	second := e.h.ticket(t, "ticket", "create", "--title", "second", "--assign")
	e.await("agent.spawned", func(ev *api.Event) bool { return ev.Ticket == second.ID })
}

func TestARestartLeavesSessionsItDoesntKnow(t *testing.T) {
	t.Parallel()
	e := newTUIE2E(t)
	// Another slop-bot's agent, on the same server: none of this one's.
	require.NoError(t, e.host.Start(e.ctx(), &sessionhost.StartSpec{Name: "agy-t9-abcdef", Argv: []string{"sleep", "600"}}))
	e.stop()
	e.restart()
	e.h.ok(t, "health")
	names, err := e.host.List(e.ctx())
	require.NoError(t, err)
	assert.Contains(t, names, "agy-t9-abcdef")
}

// toolReply runs a shell tool call in the fake, mid-turn, and returns what
// its pre-tool hook answered: a refusal's reason, or "" when it may run.
func (e *tuiE2E) toolReply(s *fake.Session, command string) string {
	e.t.Helper()
	e.do2(s, fake.Command{Cmd: fake.CmdToolStart, Tool: "bash", Text: command})
	for {
		r, err := s.Await(e.ctx(), fake.RepHook)
		require.NoError(e.t, err)
		if r.Event != fake.HookToolStart {
			continue
		}
		var reply fake.HookReply
		require.NoError(e.t, json.Unmarshal([]byte(r.Reply), &reply), r.Reply)
		e.do2(s, fake.Command{Cmd: fake.CmdToolEnd, Tool: "bash"})
		return reply.Deny
	}
}

func (e *tuiE2E) do2(s *fake.Session, c fake.Command) {
	e.t.Helper()
	_, err := s.Do(e.ctx(), c)
	require.NoError(e.t, err)
}

func TestTheMergeGuardRefusesATUIAgentsMergeUntilAuthorised(t *testing.T) {
	t.Parallel()
	e := newTUIE2E(t)
	s, agent := e.spawned("Add export")
	e.do(s, fake.CmdTurnStart)

	assert.Empty(t, e.toolReply(s, "go test ./..."), "ordinary work runs")
	reason := e.toolReply(s, "gh pr merge 12 --squash")
	assert.Contains(t, reason, "merge guard refused this")
	assert.Contains(t, reason, "has no AutoMerge")
	refused := e.await(daemon.EventMergeRefused, nil)
	assert.Equal(t, agent, refused.Agent)

	e.h.ok(t, "tell", agent, "--merge")
	assert.Empty(t, e.toolReply(s, "gh pr merge 12 --squash"), "authorised, the merge runs")
}

func TestATUIHookAnswersForTheDaemonWhenItsAway(t *testing.T) {
	t.Parallel()
	e := newTUIE2E(t)
	s, agent := e.spawned("t")
	e.stop()
	// What agy would pass, for agy's session: the hook answers as agy's
	// profile does, refusing a merge, which nothing can authorise now.
	session := "agy-" + agent
	pre := func(command string) *fake.Report {
		payload, err := json.Marshal(map[string]any{"cwd": "/", "toolCall": map[string]any{
			"name": "run_command", "args": map[string]string{"CommandLine": command},
		}})
		require.NoError(t, err)
		r, err := s.Do(e.ctx(), fake.Command{Cmd: fake.CmdRun, Argv: []string{
			"/usr/bin/env", fakeReexec + "=", cliReexec + "=1",
			"/bin/sh", "-c", `printf '%s' "$1" | "$0" hook tui "$2" PreToolUse`, e.self, string(payload), session,
		}})
		require.NoError(t, err)
		require.Equal(t, 0, r.Code, "it answers rather than fails: %s", r.Stderr)
		return r
	}
	assert.JSONEq(t, `{"decision":"allow"}`, pre("go test ./...").Stdout)
	var out struct{ Decision, Reason string }
	require.NoError(t, json.Unmarshal([]byte(pre("gh pr merge 12").Stdout), &out))
	assert.Equal(t, "deny", out.Decision)
	assert.Contains(t, out.Reason, "daemon couldn't be asked")
}

func TestStoppingTheDaemonWithAPersonJoinedLeavesTheSession(t *testing.T) {
	t.Parallel()
	e := newTUIE2E(t)
	_, agent := e.spawned("t")
	e.joined(agent)

	// It returns (startDaemon's stop asserts a clean exit): the session
	// isn't quit under the person, nor waited for.
	e.stop()
	st, err := e.host.Status(e.ctx(), e.session(agent))
	require.NoError(t, err)
	assert.False(t, st.Dead)
	assert.Equal(t, 1, st.Attached, "the person is still in it")
}

func TestAHookForAnotherAgentsSessionIsRefused(t *testing.T) {
	t.Parallel()
	e := newTUIE2E(t)
	s, agent := e.spawned("t")
	other := e.agentRun(s, "hook", "tui", "fake-t9-abcdef", fake.HookStep)
	assert.Equal(t, 1, other.Code)
	assert.Contains(t, other.Stderr, "isn't the calling agent's")
	assert.Empty(t, other.Stdout, "nothing printed, so the hook's fallback answers")

	// The user's CLI (no token) isn't a session's hook either.
	var out api.HookTUIReply
	err := control.NewClient(e.h.runtime+"/slop-bot/slop-bot.sock", "").Do(e.ctx(), "POST", api.PathHookTUI,
		&api.HookTUI{Session: e.session(agent), Event: fake.HookStep, Payload: []byte("{}")}, &out)
	require.Error(t, err)
}
