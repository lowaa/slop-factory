package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
)

func jsonDecode(s string, v any) error { return json.Unmarshal([]byte(s), v) }

// agentRun is e.agent without the exit-code requirement, for calls that
// should fail.
func (e *e2e) agentRun(s *fake.Session, args ...string) *fake.Report {
	e.t.Helper()
	argv := append([]string{"/usr/bin/env", fakeReexec + "=", cliReexec + "=1", e.self}, args...)
	r, err := s.Do(e.ctx(), fake.Command{Cmd: fake.CmdRun, Argv: argv})
	require.NoError(e.t, err)
	return r
}

// started files an assigned ticket and returns its agent's session, mid-turn.
func (e *e2e) started(args ...string) (*fake.Session, api.Ticket) {
	e.t.Helper()
	tk := e.h.ticket(e.t, append([]string{"ticket", "create", "--title", "t", "--assign"}, args...)...)
	s, err := e.ctl.Next(e.ctx())
	require.NoError(e.t, err)
	_, err = s.Expect(e.ctx(), fake.RepUser)
	require.NoError(e.t, err)
	e.do(s, fake.CmdTurnStart)
	return s, tk
}

// signalWriter closes seen once what was written contains want, so a test
// can wait for a long-running command's output without polling.
type signalWriter struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	want string
	seen chan struct{}
	once sync.Once
}

func newSignalWriter(want string) *signalWriter {
	return &signalWriter{want: want, seen: make(chan struct{})}
}

func (w *signalWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if strings.Contains(w.buf.String(), w.want) {
		w.once.Do(func() { close(w.seen) })
	}
	return n, err
}

func (w *signalWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// runUntilSeen runs args until its output contains want, then cancels it
// and returns its exit code and output.
func (e *e2e) runUntilSeen(want string, args ...string) (int, string) {
	e.t.Helper()
	out := newSignalWriter(want)
	deps := *e.h.deps
	deps.Stdout, deps.Stderr = out, &bytes.Buffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- Execute(ctx, &deps, args) }()
	select {
	case <-out.seen:
		cancel()
		return <-done, out.String()
	case code := <-done:
		require.FailNow(e.t, "exited before printing", "%q: code %d: %s", want, code, out.String())
	case <-e.ctx().Done():
		require.FailNow(e.t, "timed out waiting for output", "%q", want)
	}
	return 0, ""
}

func TestDiscussionsThroughTheCLI(t *testing.T) {
	t.Parallel()
	e := newE2E(t)
	assert.Contains(t, e.h.ok(t, "discussions"), "nothing waiting for review")
	s, tk := e.started()

	e.agent(s, "discuss", "Drop the column?", "--option", "drop", "--option", "keep", "--recommend", "keep")
	e.do(s, fake.CmdTurnEnd)
	e.await("agent.state", toState("discussion"))

	listed := e.h.ok(t, "discussions")
	assert.Contains(t, listed, "# "+tk.ID+": t")
	assert.Contains(t, listed, "Drop the column?")
	assert.Contains(t, listed, "Recommendation: keep")
	assert.Contains(t, listed, "slop-bot discussions resolve "+tk.ID)

	assert.Contains(t, e.h.ok(t, "discussions", "resolve", tk.ID, "keep it"), "resolved "+tk.ID)
	answer, err := s.Expect(e.ctx(), fake.RepUser)
	require.NoError(t, err)
	assert.Contains(t, answer.Text, "Your discussion is resolved: keep it")
	assert.Contains(t, e.h.ok(t, "discussions"), "nothing waiting for review")

	assert.Equal(t, 1, e.h.run(t, "discussions", "resolve", tk.ID, "again"), "it isn't in discussion any more")
	assert.Contains(t, e.h.err.String(), "isn't in discussion")
	assert.Equal(t, 1, e.h.run(t, "discussions", "resolve", "9999", "x"))

	r := e.agentRun(s, "discussions", "resolve", tk.ID, "x")
	assert.Equal(t, 1, r.Code, "an agent can't resolve")
}

func TestReadyToMergeReachesTheUsersInbox(t *testing.T) {
	t.Parallel()
	e := newE2E(t)
	s, _ := e.started()

	e.agent(s, "ready-to-merge", "--pr", "https://example.test/pr/7")
	e.await("user.told", func(ev *api.Event) bool { return strings.Contains(string(ev.Payload), "ready to merge") })

	inbox := e.h.ok(t, "inbox")
	assert.Contains(t, inbox, "is ready to merge (https://example.test/pr/7)")
	assert.Contains(t, e.h.ok(t, "inbox"), "no new messages", "reading marks the messages read")

	var msgs []api.Message
	require.NoError(t, jsonDecode(e.h.ok(t, "inbox", "--log", "--json"), &msgs))
	require.NotEmpty(t, msgs)
	agentID := msgs[len(msgs)-1].From
	assert.Contains(t, msgs[len(msgs)-1].Body, "ready to merge")

	assert.Contains(t, e.h.ok(t, "inbox", "--log", "--target", agentID), "ready to merge")
	assert.NotContains(t, e.h.ok(t, "inbox", "--log", "--target", "t0-nobody"), "ready to merge")

	r := e.agentRun(s, "inbox", "--log")
	assert.Equal(t, 1, r.Code)
	assert.Contains(t, r.Stderr, "only the user")
	assert.Contains(t, e.agent(s, "inbox").Stdout, "no new messages")
}

func TestReadyToMergeIsRefusedForAnAutoMergeTicket(t *testing.T) {
	t.Parallel()
	e := newE2E(t)
	s, _ := e.started("--auto-merge")
	r := e.agentRun(s, "ready-to-merge")
	assert.Equal(t, 1, r.Code)
	assert.Contains(t, r.Stderr, "AutoMerge")
}

func TestTellAllAndTellingTheUser(t *testing.T) {
	t.Parallel()
	e := newE2E(t)
	s, _ := e.started()

	assert.Contains(t, e.h.ok(t, "tell", "--all", "main is red"), "told 1")
	got, err := s.Expect(e.ctx(), fake.RepUser)
	require.NoError(t, err)
	assert.Contains(t, got.Text, "main is red")

	e.agent(s, "tell", "user", "FYI: halfway")
	assert.Contains(t, e.h.ok(t, "inbox"), "FYI: halfway")

	assert.Equal(t, 1, e.h.run(t, "tell", "nobody", "hi"))
	assert.Contains(t, e.h.err.String(), "no such agent")
	r := e.agentRun(s, "tell", "user", "stop", "--interrupt")
	assert.Equal(t, 1, r.Code, "only the user interrupts")
}

func TestAsksShowOptionsAndTheRecommendation(t *testing.T) {
	t.Parallel()
	e := newE2E(t)
	s, _ := e.started()
	e.agent(s, "ask", "CSV or XLSX?", "--option", "CSV", "--option", "XLSX", "--recommend", "CSV")
	e.do(s, fake.CmdTurnEnd)
	e.await("agent.state", toState("waiting_user"))
	asks := e.h.ok(t, "asks")
	assert.Contains(t, asks, "asks (")
	assert.Contains(t, asks, "1. CSV")
	assert.Contains(t, asks, "2. XLSX")
	assert.Contains(t, asks, "recommends: CSV")
	assert.Contains(t, asks, "answer: slop-bot tell ")
}

func TestAutonomyStatusAndBadArguments(t *testing.T) {
	t.Parallel()
	e := newE2E(t)
	assert.Contains(t, e.h.ok(t, "autonomy"), "autonomy off")
	assert.Contains(t, e.h.ok(t, "autonomy", "on"), "autonomy on")
	assert.Contains(t, e.h.ok(t, "autonomy", "status"), "autonomy on")
	assert.Contains(t, e.h.ok(t, "autonomy", "off"), "autonomy off")
	assert.Equal(t, 1, e.h.run(t, "autonomy", "maybe"))
	assert.Contains(t, e.h.err.String(), `"maybe" isn't on, off or status`)
}

func TestStatusListsAgentsAndTheirNotes(t *testing.T) {
	t.Parallel()
	e := newE2E(t)
	s, tk := e.started()
	e.agent(s, "status", "set", "reading")

	var st api.Status
	require.NoError(t, jsonDecode(e.h.ok(t, "status", "--json"), &st))
	require.Len(t, st.Agents, 1)
	assert.Equal(t, tk.ID, st.Agents[0].Ticket)
	assert.Equal(t, "reading", st.Agents[0].Note)
	assert.Contains(t, e.h.ok(t, "status"), "autonomy off, agent-filed tickets 0 of")
}

func TestEventsPrintsTheLogAndFollowsIt(t *testing.T) {
	t.Parallel()
	e := newE2E(t)
	e.started()

	events := e.h.ok(t, "events")
	assert.Contains(t, events, `"type":"agent.spawned"`)
	assert.NotContains(t, e.h.ok(t, "events", "--after", "1000000"), "agent.spawned")

	// Following from the start replays the log, so the output to wait for
	// already exists.
	code, out := e.runUntilSeen(`"type":"agent.spawned"`, "events", "--follow", "--after", "0")
	assert.Equal(t, 0, code, "cancelling a follow is not an error")
	assert.Contains(t, out, "agent.spawned")
}

func TestInboxFollowPrintsWhatIsWaitingThenStopsWhenCancelled(t *testing.T) {
	t.Parallel()
	e := newE2E(t)
	s, _ := e.started()
	e.agent(s, "tell", "user", "waiting for you")
	code, out := e.runUntilSeen("waiting for you", "inbox", "--follow")
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "waiting for you")
}
