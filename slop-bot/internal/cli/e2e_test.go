package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/daemon"
	sbharness "github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
)

// The test binary is also the fake harness and, run from it as an agent's
// tool call would, the slop-bot CLI: the whole path from an agent's shell
// to the daemon is real.
const (
	fakeReexec = "SLOP_BOT_FAKE_REEXEC"
	cliReexec  = "SLOP_BOT_CLI_REEXEC"
)

func TestMain(m *testing.M) {
	switch {
	case os.Getenv(cliReexec) == "1":
		deps, err := FromOS()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(Execute(context.Background(), deps, os.Args[1:]))
	case os.Getenv(fakeReexec) == "1":
		os.Exit(fake.Main(os.Args[1:], os.Stdin, os.Stdout, os.Getenv))
	}
	os.Exit(m.Run())
}

// e2eHarness runs the fake for claude-code, giving the agent's process the
// test's home and runtime dir, so its slop-bot calls reach this daemon.
type e2eHarness struct {
	*fake.Harness
	h *harness
}

func (e *e2eHarness) Start(ctx context.Context, ws *sbharness.Workspace, resume *sbharness.ConversationRef) (sbharness.Conversation, error) {
	ws.Env[fakeReexec] = "1"
	ws.Env["GORACE"] = "atexit_sleep_ms=0"
	ws.Env["HOME"] = e.h.home
	ws.Env["XDG_RUNTIME_DIR"] = e.h.runtime
	return e.Harness.Start(ctx, ws, resume)
}

type e2e struct {
	t    *testing.T
	h    *harness
	ctl  *fake.Controller
	self string
	seq  int64
	// stop stops the daemon, waiting for it to exit.
	stop func()
}

func newE2E(t *testing.T) *e2e {
	t.Helper()
	return newE2EWithConfig(t, "")
}

// newE2EWithConfig is newE2E with the config file replaced, when given.
func newE2EWithConfig(t *testing.T, cfg string) *e2e {
	t.Helper()
	return newE2EOn(t, cfg, nil)
}

// newE2EOn is newE2EWithConfig with the daemon on clk, when given.
func newE2EOn(t *testing.T, cfg string, clk clock.Clock) *e2e {
	t.Helper()
	return newE2EFrom(t, newHarness(t), cfg, clk)
}

// newE2EFrom is newE2EOn with h's other deps as the test set them.
func newE2EFrom(t *testing.T, h *harness, cfg string, clk clock.Clock) *e2e {
	t.Helper()
	h.deps.clock = clk
	if cfg != "" {
		require.NoError(t, os.WriteFile(h.config, []byte(cfg), 0o600))
	}
	ctl, err := fake.NewController()
	require.NoError(t, err)
	self, err := os.Executable()
	require.NoError(t, err)
	h.deps.harnesses = daemon.Harnesses{"claude-code": &e2eHarness{
		Harness: &fake.Harness{Binary: self, Control: ctl.Socket(), Logger: slog.New(slog.DiscardHandler), Clock: clock.Real{}},
		h:       h,
	}}
	stop := startDaemon(t, h)
	// Closing the controller first makes the fakes exit, so the daemon's
	// drain doesn't wait on turns the test left open.
	t.Cleanup(func() { _ = ctl.Close() })
	return &e2e{t: t, h: h, ctl: ctl, self: self, stop: stop}
}

func (e *e2e) ctx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	e.t.Cleanup(cancel)
	return ctx
}

// agent runs slop-bot as the agent would: from the fake, as a tool call,
// with the agent's own environment (its token).
func (e *e2e) agent(s *fake.Session, args ...string) *fake.Report {
	e.t.Helper()
	argv := append([]string{"/usr/bin/env", fakeReexec + "=", cliReexec + "=1", e.self}, args...)
	r, err := s.Do(e.ctx(), fake.Command{Cmd: fake.CmdRun, Argv: argv})
	require.NoError(e.t, err)
	require.Equal(e.t, 0, r.Code, "slop-bot %v: %s%s", args, r.Stdout, r.Stderr)
	return r
}

func (e *e2e) do(s *fake.Session, cmd string) {
	e.t.Helper()
	_, err := s.Do(e.ctx(), fake.Command{Cmd: cmd})
	require.NoError(e.t, err)
}

// await reads the daemon's event log until an event of typ arrives.
func (e *e2e) await(typ string, match func(*api.Event) bool) *api.Event {
	e.t.Helper()
	ctx := e.ctx()
	body, err := control.NewClient(e.h.runtime+"/slop-bot/slop-bot.sock", "").Stream(ctx, fmt.Sprintf("%s?follow&after=%d", api.PathEvents, e.seq))
	require.NoError(e.t, err)
	defer func() { _ = body.Close() }()
	sc := bufio.NewScanner(body)
	for sc.Scan() {
		var ev api.Event
		require.NoError(e.t, json.Unmarshal(sc.Bytes(), &ev))
		e.seq = ev.Seq
		if ev.Type == typ && (match == nil || match(&ev)) {
			return &ev
		}
	}
	require.FailNow(e.t, "event stream ended", "waiting for %s: %v", typ, sc.Err())
	return nil
}

func toState(s string) func(*api.Event) bool {
	return func(e *api.Event) bool { return strings.Contains(string(e.Payload), `"to":"`+s+`"`) }
}

func TestAnAgentWorksATicketThroughTheCLI(t *testing.T) {
	t.Parallel()
	e := newE2E(t)
	tk := e.h.ticket(t, "ticket", "create", "--title", "Add export", "--assign")
	s, err := e.ctl.Next(e.ctx())
	require.NoError(t, err)
	kick, err := s.Expect(e.ctx(), fake.RepUser)
	require.NoError(t, err)
	assert.Contains(t, kick.Text, "Add export")

	// Refining: the agent sets its note and asks the user.
	e.do(s, fake.CmdTurnStart)
	e.agent(s, "status", "set", "reading the code")
	asked := e.agent(s, "ask", "CSV or XLSX?", "--option", "CSV", "--option", "XLSX", "--recommend", "CSV")
	assert.Contains(t, asked.Stdout, "asked")
	e.do(s, fake.CmdTurnEnd)
	ev := e.await("agent.state", toState("waiting_user"))
	agent := ev.Agent

	assert.Contains(t, e.h.ok(t, "asks"), "CSV or XLSX?")
	status := e.h.ok(t, "status")
	assert.Contains(t, status, "waiting_user")
	assert.Contains(t, status, "reading the code")

	// The user answers with tell.
	e.h.ok(t, "tell", agent, "CSV")
	answer, err := s.Expect(e.ctx(), fake.RepUser)
	require.NoError(t, err)
	assert.Contains(t, answer.Text, "CSV")
	assert.NotContains(t, e.h.ok(t, "asks"), "CSV or XLSX?", "answered")

	// Working, then done: the agent's tool calls carry its token.
	e.do(s, fake.CmdTurnStart)
	e.agent(s, "ticket", "comment", tk.ID, "Decided: CSV only")
	e.agent(s, "done", "--summary", "Exported as CSV.")
	e.do(s, fake.CmdTurnEnd)
	e.await("agent.finished", nil)
	shown := e.h.ticket(t, "ticket", "show", tk.ID)
	assert.Equal(t, "done", shown.Status)
	var comments []string
	for _, c := range shown.Comments {
		comments = append(comments, c.Author.Kind+": "+c.Body)
	}
	assert.Contains(t, comments, "agent: Decided: CSV only")
	assert.Contains(t, strings.Join(comments, "\n"), "Exported as CSV.")
}

func TestAnAgentCantActAsTheUser(t *testing.T) {
	t.Parallel()
	e := newE2E(t)
	tk := e.h.ticket(t, "ticket", "create", "--title", "t", "--assign")
	s, err := e.ctl.Next(e.ctx())
	require.NoError(t, err)
	_, err = s.Expect(e.ctx(), fake.RepUser)
	require.NoError(t, err)
	e.do(s, fake.CmdTurnStart)
	argv := []string{"/usr/bin/env", fakeReexec + "=", cliReexec + "=1", e.self, "ticket", "set", tk.ID, "--auto-merge"}
	r, err := s.Do(e.ctx(), fake.Command{Cmd: fake.CmdRun, Argv: argv})
	require.NoError(t, err)
	assert.Equal(t, 1, r.Code)
	assert.Contains(t, r.Stderr, "only the user may set AutoMerge")

	argv = []string{"/usr/bin/env", fakeReexec + "=", cliReexec + "=1", e.self, "autonomy", "on"}
	r, err = s.Do(e.ctx(), fake.Command{Cmd: fake.CmdRun, Argv: argv})
	require.NoError(t, err)
	assert.Equal(t, 1, r.Code)
}

func TestTheBriefingListsTheAgentCommands(t *testing.T) {
	t.Parallel()
	ref := AgentReference(New(&Deps{Stdout: os.Stdout, Stderr: os.Stderr}))
	for _, want := range []string{"slop-bot ask <question>", "slop-bot tell <agent|ticket|user> <text>", "slop-bot done --summary <text>",
		"slop-bot ticket create --title <title>", "slop-bot status set <note>", "slop-bot ready-to-merge", "slop-bot discuss <decision>", "slop-bot inbox"} {
		assert.Contains(t, ref, "`"+want+"`")
	}
	for _, userOnly := range []string{"slop-bot daemon", "slop-bot autonomy", "slop-bot asks", "slop-bot discussions", "slop-bot service"} {
		assert.NotContains(t, ref, "`"+userOnly, "agents don't run %s", userOnly)
	}
}

// fileAsAgent has the agent working s file a ticket, returning the result.
func (e *e2e) fileAsAgent(s *fake.Session, title string) *fake.Report {
	e.t.Helper()
	argv := []string{"/usr/bin/env", fakeReexec + "=", cliReexec + "=1", e.self, "ticket", "create", "--title", title, "--json"}
	r, err := s.Do(e.ctx(), fake.Command{Cmd: fake.CmdRun, Argv: argv})
	require.NoError(e.t, err)
	return r
}

func TestAutonomyDecidesWhetherAgentFiledTicketsAreWorked(t *testing.T) {
	t.Parallel()
	e := newE2E(t)
	e.h.ticket(t, "ticket", "create", "--title", "parent", "--assign")
	s, err := e.ctl.Next(e.ctx())
	require.NoError(t, err)
	_, err = s.Expect(e.ctx(), fake.RepUser)
	require.NoError(t, err)
	e.do(s, fake.CmdTurnStart)

	// Off (the default): the follow-up waits, unassigned, for the user.
	r := e.fileAsAgent(s, "follow-up while off")
	require.Equal(t, 0, r.Code, r.Stderr)
	var off api.Ticket
	require.NoError(t, json.Unmarshal([]byte(r.Stdout), &off))
	assert.Empty(t, off.Assignee)
	assert.Equal(t, "agent", off.CreatedBy.Kind)

	// On: it's assigned, so an agent picks it up.
	e.h.ok(t, "autonomy", "on")
	r = e.fileAsAgent(s, "follow-up while on")
	require.Equal(t, 0, r.Code, r.Stderr)
	var on api.Ticket
	require.NoError(t, json.Unmarshal([]byte(r.Stdout), &on))
	assert.Equal(t, "ada", on.Assignee)
	e.await("agent.spawned", func(ev *api.Event) bool { return ev.Ticket == on.ID })
}

func TestTheAutonomyBudgetScalesWithTheSlots(t *testing.T) {
	t.Parallel()
	e := newE2EWithConfig(t, "user: ada\nconcurrency: {max_active: 1, max_live: 4, urgent: {max_active: 1}}\nautonomy: {tickets_per_agent: 1}\n")
	e.h.ticket(t, "ticket", "create", "--title", "parent", "--assign")
	s, err := e.ctl.Next(e.ctx())
	require.NoError(t, err)
	_, err = s.Expect(e.ctx(), fake.RepUser)
	require.NoError(t, err)
	e.do(s, fake.CmdTurnStart)
	// 1 per slot × (1 + 1) slots = 2 outstanding agent-filed tickets.
	for _, title := range []string{"one", "two"} {
		r := e.fileAsAgent(s, title)
		require.Equal(t, 0, r.Code, r.Stderr)
	}
	r := e.fileAsAgent(s, "three")
	assert.Equal(t, 1, r.Code)
	assert.Contains(t, r.Stderr, "budget")
	e.await("user.told", func(ev *api.Event) bool { return strings.Contains(string(ev.Payload), "budget") })
	// The user's own tickets never count.
	e.h.ticket(t, "ticket", "create", "--title", "mine")
}
