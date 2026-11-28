package agents_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/paths"
	"github.com/lowaa/slop-factory/slop-bot/internal/sqlitex"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets/local"
	"github.com/lowaa/slop-factory/slop-bot/internal/transcript"
	"github.com/lowaa/slop-factory/slop-bot/internal/worktree"
)

const reexec = "SLOP_BOT_FAKE_REEXEC"

// The test binary doubles as the fake harness.
func TestMain(m *testing.M) {
	if os.Getenv(reexec) == "1" {
		os.Exit(fake.Main(os.Args[1:], os.Stdin, os.Stdout, os.Getenv))
	}
	os.Exit(m.Run())
}

var t0 = time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC)

// fakeHarness runs the fake, with the re-exec env, for claude-code, or for
// each of names when given.
type fakeHarness struct {
	h     *fake.Harness
	names []string
	hooks *harnessHooks
}

func (f *fakeHarness) Harness(name string) (harness.AIHarness, bool) {
	names := f.names
	if names == nil {
		names = []string{config.ClaudeCode}
	}
	if !slices.Contains(names, name) {
		return nil, false
	}
	return &envHarness{Harness: f.h, hooks: f.hooks}, true
}

// harnessHooks let a test break the harness where the fake can't: a start
// that fails or hangs, a Send that fails or blocks, a driver that can't
// steer. Unset hooks change nothing.
type harnessHooks struct {
	mu sync.Mutex
	// start runs before each start (resume is nil for a new agent); an
	// error fails it.
	start func(ctx context.Context, ws *harness.Workspace, resume *harness.ConversationRef) error
	// send runs before each Send; an error fails it, nothing sent.
	send    func(ctx context.Context, agent ident.AgentID, msg *harness.Message) error
	noSteer bool
	// stop runs before each Stop.
	stop func(agent ident.AgentID)
	// attached makes a Stop without force refuse, as a TUI driver's does
	// with a person in the session.
	attached bool
	// hold makes a Send take the message and keep it, as a codec waiting
	// for a turn does: the conversation's Undelivered.
	hold bool
}

func (h *harnessHooks) holds() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hold
}

func (h *harnessHooks) onStop(agent ident.AgentID) {
	if h == nil {
		return
	}
	h.mu.Lock()
	stop := h.stop
	h.mu.Unlock()
	if stop != nil {
		stop(agent)
	}
}

func (h *harnessHooks) isAttached() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.attached
}

func (h *harnessHooks) set(f func(h *harnessHooks)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f(h)
}

func (h *harnessHooks) get() (start func(context.Context, *harness.Workspace, *harness.ConversationRef) error,
	send func(context.Context, ident.AgentID, *harness.Message) error, noSteer bool) {
	if h == nil {
		return nil, nil, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.start, h.send, h.noSteer
}

// envHarness adds the re-exec variables to every workspace, and the hooks.
type envHarness struct {
	*fake.Harness
	hooks *harnessHooks
}

func (e *envHarness) Capabilities() harness.Capabilities {
	c := e.Harness.Capabilities()
	if _, _, noSteer := e.hooks.get(); noSteer {
		c.MidTurnSteer = false
	}
	return c
}

func (e *envHarness) Start(ctx context.Context, ws *harness.Workspace, resume *harness.ConversationRef) (harness.Conversation, error) {
	if start, _, _ := e.hooks.get(); start != nil {
		if err := start(ctx, ws, resume); err != nil {
			return nil, err
		}
	}
	ws.Env[reexec] = "1"
	ws.Env["GORACE"] = "atexit_sleep_ms=0"
	conv, err := e.Harness.Start(ctx, ws, resume)
	if err != nil {
		return nil, err
	}
	return &hookedConversation{Conversation: conv, agent: ws.Agent, hooks: e.hooks}, nil
}

type hookedConversation struct {
	harness.Conversation
	agent ident.AgentID
	hooks *harnessHooks

	mu   sync.Mutex
	held []*harness.Message
}

func (c *hookedConversation) Send(ctx context.Context, msg *harness.Message) error {
	if _, send, _ := c.hooks.get(); send != nil {
		if err := send(ctx, c.agent, msg); err != nil {
			return err
		}
	}
	if c.hooks.holds() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.held = append(c.held, msg)
		return nil
	}
	return c.Conversation.Send(ctx, msg)
}

func (c *hookedConversation) Undelivered() []*harness.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append(c.held, c.Conversation.Undelivered()...)
}

func (c *hookedConversation) Stop(ctx context.Context, force bool) error {
	c.hooks.onStop(c.agent)
	if c.hooks.isAttached() && !force {
		return harness.ErrAttached
	}
	return c.Conversation.Stop(ctx, force)
}

// recordingWorktrees stands in for git: worktree behaviour is tested
// against real git in its own package.
type recordingWorktrees struct {
	mu       sync.Mutex
	created  []ident.TicketID
	cleanups []ident.TicketID
	removed  []string
	dir      string
	// leftover, when set, is reported alongside each removal, as the real
	// manager does for branches it couldn't delete.
	leftover error
	// beforeCreate, when set, runs before each Create; an error fails it.
	beforeCreate func(ctx context.Context) error
	// beforeCleanup, when set, runs once, before the next Cleanup.
	beforeCleanup func()
}

func (w *recordingWorktrees) Create(ctx context.Context, ticket ident.TicketID, repos []string, primary string) ([]*worktree.Worktree, error) {
	w.mu.Lock()
	before := w.beforeCreate
	w.mu.Unlock()
	if before != nil {
		if err := before(ctx); err != nil {
			return nil, err
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.created = append(w.created, ticket)
	var out []*worktree.Worktree
	for _, r := range repos {
		p := filepath.Join(w.dir, "t"+string(ticket)+"-"+r)
		if err := os.MkdirAll(p, 0o700); err != nil {
			return nil, err
		}
		out = append(out, &worktree.Worktree{Ticket: ticket, Repo: r, Path: p, Branch: "slop-bot/t" + string(ticket), Primary: r == primary})
	}
	return out, nil
}

func (w *recordingWorktrees) Cleanup(_ context.Context, ticket ident.TicketID, wts []*worktree.Worktree) []*worktree.CleanupResult {
	w.mu.Lock()
	before := w.beforeCleanup
	w.beforeCleanup = nil
	w.mu.Unlock()
	if before != nil {
		before()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cleanups = append(w.cleanups, ticket)
	out := make([]*worktree.CleanupResult, 0, len(wts))
	for _, wt := range wts {
		// The real manager runs git in RepoPath: cleanup must be handed it.
		if wt.RepoPath == "" {
			out = append(out, &worktree.CleanupResult{Worktree: wt, Err: errors.New("no repo path")})
			continue
		}
		w.removed = append(w.removed, wt.Path)
		out = append(out, &worktree.CleanupResult{Worktree: wt, Removed: true, Err: w.leftover})
	}
	return out
}

// racingTickets runs a test's hook just before the next status update the
// manager makes, then reports how that update went.
type racingTickets struct {
	tickets.TicketingSystem
	mu     sync.Mutex
	before func()
	result chan error
	// getErr and updateErr, when set, fail the reads and status updates of
	// the tickets they name, with the error they return.
	getErr, updateErr map[ident.TicketID]error
	// lost are tickets whose feed events are dropped, as a remote backend
	// might lose them.
	lost       map[ident.TicketID]bool
	eventsOnce sync.Once
	events     chan tickets.TicketEvent
	done       <-chan struct{}
}

var errBackendAway = errors.New("racing: the ticketing backend is away")

// healGet ends failGet's failures of ticket id.
func (r *racingTickets) healGet(id ident.TicketID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.getErr, id)
}

// lose drops ticket's feed events from now on.
func (r *racingTickets) lose(ticket ident.TicketID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lost == nil {
		r.lost = map[ident.TicketID]bool{}
	}
	r.lost[ticket] = true
}

// Events is the backend's feed less the lost tickets' events. One forwarder
// serves every manager the test starts, as the backend's one feed does.
func (r *racingTickets) Events() <-chan tickets.TicketEvent {
	r.eventsOnce.Do(func() {
		src := r.TicketingSystem.Events()
		r.events = make(chan tickets.TicketEvent)
		go func() {
			defer close(r.events)
			for ev := range src {
				r.mu.Lock()
				lost := r.lost[ev.Ticket]
				r.mu.Unlock()
				if lost {
					continue
				}
				select {
				case r.events <- ev:
				case <-r.done:
					return
				}
			}
		}()
	})
	return r.events
}

// failGet makes reading ticket id fail with err, from now on.
func (r *racingTickets) failGet(id ident.TicketID, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr == nil {
		r.getErr = map[ident.TicketID]error{}
	}
	r.getErr[id] = err
}

// failUpdate makes a status update of ticket id fail with err, from now on.
func (r *racingTickets) failUpdate(id ident.TicketID, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updateErr == nil {
		r.updateErr = map[ident.TicketID]error{}
	}
	r.updateErr[id] = err
}

func (r *racingTickets) Get(ctx context.Context, id ident.TicketID) (*tickets.Ticket, error) {
	r.mu.Lock()
	err := r.getErr[id]
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return r.TicketingSystem.Get(ctx, id)
}

// arm makes the next status update run before first, and report on result.
func (r *racingTickets) arm(before func()) <-chan error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.before, r.result = before, make(chan error, 1)
	return r.result
}

func (r *racingTickets) Update(ctx context.Context, by ident.Principal, id ident.TicketID, change *tickets.TicketChange) (*tickets.Ticket, error) {
	r.mu.Lock()
	before, result := r.before, r.result
	if change.Status != nil {
		r.before, r.result = nil, nil
	} else {
		before, result = nil, nil
	}
	fail := r.updateErr[id]
	r.mu.Unlock()
	if fail != nil && change.Status != nil {
		return nil, fail
	}
	if before != nil {
		before()
	}
	t, err := r.TicketingSystem.Update(ctx, by, id, change)
	if result != nil {
		result <- err
	}
	return t, err
}

type fixture struct {
	t     *testing.T
	ctl   *fake.Controller
	state *state.State
	// stateDB is state's database, for a test to make a write fail (failOn).
	stateDB *sqlitex.DB
	// logs is what the manager logged.
	logs    *logBuffer
	tickets *local.System
	// racing is the ticketing system the manager sees: tickets, with a hook
	// that can land a write between the manager's read and its update.
	racing  *racingTickets
	hooks   *harnessHooks
	clock   *clock.Fake
	wts     *recordingWorktrees
	cfg     *config.Config
	paths   *paths.Paths
	mgr     *agents.Manager
	cursor  int64
	archive *transcript.Archive
	// transcripts is where the fakes keep theirs.
	transcripts string
}

func newFixture(t *testing.T, cfgYAML string) *fixture {
	t.Helper()
	return newFixtureFor(t, cfgYAML, nil)
}

// newFixtureFor runs the fake for each harness in names (claude-code when
// nil).
func newFixtureFor(t *testing.T, cfgYAML string, names []string) *fixture {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	quiet := slog.New(slog.DiscardHandler)
	ctl, err := fake.NewController()
	require.NoError(t, err)
	cfg, err := config.Parse([]byte(cfgYAML+"\nrepos: [{name: app, path: "+filepath.Join(dir, "repo")+"}]\n"), dir, dir)
	require.NoError(t, err)
	p, err := paths.Resolve(&paths.Env{
		Lookup: func(k string) (string, bool) {
			if k == "XDG_RUNTIME_DIR" {
				return "/tmp", true
			}
			return "", false
		},
		Home: dir, GOOS: "linux",
	})
	require.NoError(t, err)
	clk := clock.NewFake(t0)
	stateDB, err := sqlitex.Open(ctx, &sqlitex.Options{Path: filepath.Join(dir, "state.db"), Migrations: state.Migrations(), Logger: quiet})
	require.NoError(t, err)
	t.Cleanup(func() { _ = stateDB.Close() })
	st := state.New(stateDB, clk)
	t.Cleanup(func() { _ = st.Close() })
	ts, err := local.Open(ctx, &local.Options{Path: filepath.Join(dir, "tickets.db"), Config: tickets.Config{AgentsMaySetUrgent: true}, Logger: quiet, Clock: clk})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ts.Close() })
	require.NoError(t, ts.Start(ctx, ""))
	self, err := os.Executable()
	require.NoError(t, err)
	archive, err := transcript.NewArchive(&transcript.ArchiveOptions{
		Dir: p.TranscriptsDir(), Index: st.Transcripts, Retain: cfg.Retention.Transcripts.D(), Clock: clk,
	})
	require.NoError(t, err)
	f := &fixture{t: t, ctl: ctl, state: st, stateDB: stateDB, logs: &logBuffer{}, tickets: ts, racing: &racingTickets{TicketingSystem: ts, done: t.Context().Done()}, clock: clk, cfg: cfg, paths: p,
		wts: &recordingWorktrees{dir: filepath.Join(dir, "ws")}, hooks: &harnessHooks{}, archive: archive, transcripts: filepath.Join(dir, "fake-transcripts")}
	require.NoError(t, os.MkdirAll(f.transcripts, 0o700))
	t.Cleanup(func() {
		if t.Failed() {
			evs, _ := st.Events.ReadAfter(context.Background(), 0, 500)
			for _, e := range evs {
				t.Logf("event %d %s %s %s %s", e.Seq, e.Type, e.Agent, e.Ticket, e.Payload)
			}
		}
	})
	f.mgr = f.newManager(&fakeHarness{h: &fake.Harness{Binary: self, Control: ctl.Socket(), TranscriptDir: f.transcripts, Logger: quiet, Clock: clock.Real{}}, names: names, hooks: f.hooks})
	require.NoError(t, f.mgr.Start(ctx))
	stopInCleanup(t, func() *agents.Manager { return f.mgr })
	// Cleanups run last-first: closing the controller first makes every
	// fake exit, so the managers' drains don't wait on turns the test left
	// open.
	t.Cleanup(func() { _ = ctl.Close() })
	return f
}

// restartWith stops the manager, which drains (end any turn in flight
// first, or it waits), runs between while no manager runs, and starts a new
// one over the same state.
func (f *fixture) restartWith(between func()) *agents.Manager {
	f.t.Helper()
	require.NoError(f.t, f.mgr.Stop(f.ctx()))
	if between != nil {
		between()
	}
	return f.startAgain()
}

// startAgain starts a new manager over the state f's stopped one left.
func (f *fixture) startAgain() *agents.Manager {
	f.t.Helper()
	self, err := os.Executable()
	require.NoError(f.t, err)
	m := f.newManager(&fakeHarness{h: &fake.Harness{Binary: self, Control: f.ctl.Socket(), TranscriptDir: f.transcripts, Logger: slog.New(slog.DiscardHandler), Clock: clock.Real{}}, hooks: f.hooks})
	require.NoError(f.t, m.Start(context.Background()))
	stopInCleanup(f.t, func() *agents.Manager { return m })
	f.t.Cleanup(func() { _ = f.ctl.Close() })
	f.mgr = m
	return m
}

func (f *fixture) newManager(h agents.Harnesses) *agents.Manager {
	m, err := agents.New(&agents.Options{
		Config: f.cfg, Paths: f.paths, State: f.state, Tickets: f.racing, Harnesses: h,
		Worktrees: f.wts, User: "ada", Logger: slog.New(slog.NewTextHandler(io.MultiWriter(testWriter{f.t}, f.logs), nil)), Clock: f.clock, CLI: "/usr/bin/slop-bot",
		Transcripts: transcript.NewLibrary(h, f.archive),
	})
	require.NoError(f.t, err)
	return m
}

// ctx is a step's safety net, never a synchronisation: generous, for a
// suite under -race and -coverpkg on a loaded machine, where one step
// (thirty Tells, say) has outrun twenty seconds.
func (f *fixture) ctx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	f.t.Cleanup(cancel)
	return ctx
}

// file creates a ticket as the user.
func (f *fixture) file(title string, mut func(*tickets.NewTicket)) *tickets.Ticket {
	f.t.Helper()
	nt := &tickets.NewTicket{Title: title, Assignee: "ada", Repos: []string{"app"}}
	if mut != nil {
		mut(nt)
	}
	tk, err := f.tickets.Create(context.Background(), ident.UserPrincipal(), nt)
	require.NoError(f.t, err)
	f.mgr.Kick()
	return tk
}

// await follows the event log until an event matches, and returns it.
func (f *fixture) await(typ string, match func(*state.Event) bool) *state.Event {
	f.t.Helper()
	return f.awaitExcept(typ, match, nil)
}

// awaitExcept is await that fails at once on an event matching unexpected:
// a broken run then fails on what went wrong, rather than by waiting out
// the step's deadline for an event that can no longer come.
func (f *fixture) awaitExcept(typ string, match, unexpected func(*state.Event) bool) *state.Event {
	f.t.Helper()
	ctx := f.ctx()
	for {
		evs, err := f.state.Events.Follow(ctx, f.cursor, 100)
		require.NoError(f.t, err, "waiting for %s", typ)
		for i := range evs {
			f.cursor = evs[i].Seq
			e := &evs[i]
			if string(e.Type) == typ && (match == nil || match(e)) {
				return e
			}
			if unexpected != nil && unexpected(e) {
				require.FailNow(f.t, "unexpected event", "waiting for %s, got %s %s %s", typ, e.Type, e.Agent, e.Payload)
			}
		}
	}
}

// ofType matches an event of any of typs, for agent when it isn't "".
func ofType(agent ident.AgentID, typs ...string) func(*state.Event) bool {
	return func(e *state.Event) bool {
		return (agent == "" || e.Agent == agent) && slices.Contains(typs, string(e.Type))
	}
}

// gone matches an event that ends agent, or says its harness died: what a
// test waiting on the agent's next step rarely expects.
func gone(agent ident.AgentID) func(*state.Event) bool {
	ended := ofType(agent, agents.EventFinished, agents.EventReleased, agents.EventSpawnFailed)
	return func(e *state.Event) bool {
		return ended(e) || (e.Agent == agent && string(e.Type) == agents.EventState &&
			(toState(state.StateCrashed)(e) || toState(state.StateExited)(e)))
	}
}

func toState(to state.AgentState) func(*state.Event) bool {
	return func(e *state.Event) bool {
		var p map[string]string
		_ = json.Unmarshal(e.Payload, &p)
		return p["to"] == string(to)
	}
}

func (f *fixture) session() *fake.Session {
	f.t.Helper()
	s, err := f.ctl.Next(f.ctx())
	require.NoError(f.t, err)
	return s
}

func (f *fixture) do(s *fake.Session, cmds ...fake.Command) {
	f.t.Helper()
	for _, c := range cmds {
		_, err := s.Do(f.ctx(), c)
		require.NoError(f.t, err)
	}
}

func (f *fixture) user(s *fake.Session) *fake.Report {
	f.t.Helper()
	r, err := s.Expect(f.ctx(), fake.RepUser)
	require.NoError(f.t, err)
	return r
}

// turn plays one turn: the fake starts it, runs mid, and ends it.
func (f *fixture) turn(s *fake.Session, mid func()) {
	f.t.Helper()
	f.do(s, fake.Command{Cmd: fake.CmdTurnStart})
	if mid != nil {
		mid()
	}
	f.do(s, fake.Command{Cmd: fake.CmdTurnEnd})
}

func (f *fixture) agentFor(ticket ident.TicketID) *state.Agent {
	f.t.Helper()
	a, err := f.state.Agents.LiveForTicket(context.Background(), ticket)
	require.NoError(f.t, err)
	return a
}

const oneSlot = "concurrency: {max_active: 1, max_live: 4}\nagents: {nudges: 2}\n"

func TestAnAssignedTicketGetsAnAgentThatRefinesIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	tk := f.file("Add export", nil)
	f.await(agents.EventSpawned, nil)
	s := f.session()
	kick := f.user(s)
	assert.Contains(t, kick.Text, `assigned ticket `+string(tk.ID)+`: "Add export"`)
	assert.Contains(t, kick.Text, "refine it first")

	f.do(s, fake.Command{Cmd: fake.CmdTurnStart})
	f.await(agents.EventState, toState(state.StateRefining))
	assert.Equal(t, []ident.TicketID{tk.ID}, f.wts.created)
}

func TestUnassignedTicketsAreLeftAlone(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	f.file("nobody's", func(nt *tickets.NewTicket) { nt.Assignee = "" })
	f.file("mine", nil)
	ev := f.await(agents.EventSpawned, nil)
	mine := f.agentFor(ev.Ticket)
	tk, err := f.tickets.Get(context.Background(), mine.Ticket)
	require.NoError(t, err)
	assert.Equal(t, "mine", tk.Title)
}

func TestAnAgentThatStopsWithoutSayingWhyIsNudgedThenStalled(t *testing.T) {
	t.Parallel()
	f := newFixture(t, oneSlot)
	tk := f.file("t", nil)
	s := f.session()
	f.user(s)
	f.turn(s, nil)
	nudge := f.user(s)
	assert.Contains(t, nudge.Text, "without saying why")
	f.turn(s, nil)
	f.user(s)
	f.turn(s, nil)
	f.await(agents.EventState, toState(state.StateStalled))
	f.await(agents.EventUserTold, func(e *state.Event) bool { return e.Agent == f.agentFor(tk.ID).ID })
}

func TestAskWaitsForTheUserAndTellAnswersIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	tk := f.file("t", nil)
	s := f.session()
	f.user(s)
	a := f.agentFor(tk.ID)
	f.turn(s, func() {
		_, err := f.mgr.Ask(f.ctx(), ident.AgentPrincipal(a.ID, tk.ID), "CSV or XLSX?", []string{"CSV", "XLSX"}, "CSV")
		require.NoError(t, err)
	})
	f.await(agents.EventState, toState(state.StateWaitingUser))
	open, err := f.state.Messages.OpenAsks(context.Background())
	require.NoError(t, err)
	require.Len(t, open, 1)

	_, err = f.mgr.Tell(f.ctx(), ident.UserPrincipal(), string(a.ID), "CSV", false)
	require.NoError(t, err)
	answer := f.user(s)
	assert.Contains(t, answer.Text, "Message from the user")
	assert.Contains(t, answer.Text, "CSV")
	open, err = f.state.Messages.OpenAsks(context.Background())
	require.NoError(t, err)
	assert.Empty(t, open, "the user's tell answered the ask")
}

func TestDoneFinishesTheTicketAndCleansUp(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	tk := f.file("t", func(nt *tickets.NewTicket) { nt.Status = tickets.StatusOpen })
	s := f.session()
	f.user(s)
	a := f.agentFor(tk.ID)
	f.turn(s, func() {
		require.NoError(t, f.mgr.Done(f.ctx(), ident.AgentPrincipal(a.ID, tk.ID), "Exported as CSV."))
	})
	f.await(agents.EventFinished, nil)
	got, err := f.tickets.Get(context.Background(), tk.ID)
	require.NoError(t, err)
	assert.Equal(t, tickets.StatusDone, got.Status)
	require.NotEmpty(t, got.Comments)
	assert.Contains(t, got.Comments[len(got.Comments)-1].Body, "Exported as CSV.")
	assert.Equal(t, []ident.TicketID{tk.ID}, f.wts.cleanups)
	assert.Len(t, f.wts.removed, 1, "the worktree was removed, not kept with an error")
	ended, err := f.state.Agents.Get(context.Background(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, state.StateDone, ended.State)
}

// A worktree removed but with branches left behind is forgotten and the user
// still told why: the error isn't lost behind the removal.
func TestACleanupThatRemovedButFailedTellsTheUser(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	f.wts.mu.Lock()
	f.wts.leftover = errors.New("branch slop-bot/t1 is checked out elsewhere")
	f.wts.mu.Unlock()
	tk := f.file("t", func(nt *tickets.NewTicket) { nt.Status = tickets.StatusOpen })
	s := f.session()
	f.user(s)
	a := f.agentFor(tk.ID)
	f.turn(s, func() {
		require.NoError(t, f.mgr.Done(f.ctx(), ident.AgentPrincipal(a.ID, tk.ID), "Exported as CSV."))
	})
	f.await(agents.EventFinished, nil)

	pending, err := f.state.Messages.Pending(context.Background(), state.UserParty)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Contains(t, pending[0].Body, "couldn't fully clean up")
	assert.Contains(t, pending[0].Body, "checked out elsewhere")
	ended, err := f.state.Agents.Get(context.Background(), a.ID)
	require.NoError(t, err)
	assert.Empty(t, ended.Worktrees, "the removed worktree is no longer recorded")
}

func TestMaxActiveCountsAgentsInATurnOnly(t *testing.T) {
	t.Parallel()
	f := newFixture(t, oneSlot)
	first := f.file("first", nil)
	s1 := f.session()
	f.user(s1)
	second := f.file("second", nil)
	f.mgr.Kick()
	a1 := f.agentFor(first.ID)

	// The first is in its turn: the second must wait.
	f.do(s1, fake.Command{Cmd: fake.CmdTurnStart})
	_, err := f.state.Agents.LiveForTicket(context.Background(), second.ID)
	assert.ErrorIs(t, err, state.ErrNotFound)

	// Asking frees the slot: a waiting agent holds its process, not a slot.
	_, err = f.mgr.Ask(f.ctx(), ident.AgentPrincipal(a1.ID, first.ID), "?", nil, "")
	require.NoError(t, err)
	f.do(s1, fake.Command{Cmd: fake.CmdTurnEnd})
	ev := f.await(agents.EventSpawned, func(e *state.Event) bool { return e.Ticket == second.ID })
	assert.Equal(t, second.ID, ev.Ticket)
}

func TestAnAnsweredAgentWaitsForASlotAheadOfNewWork(t *testing.T) {
	t.Parallel()
	f := newFixture(t, oneSlot)
	first := f.file("first", nil)
	s1 := f.session()
	f.user(s1)
	a1 := f.agentFor(first.ID)
	f.turn(s1, func() {
		_, err := f.mgr.Ask(f.ctx(), ident.AgentPrincipal(a1.ID, first.ID), "?", nil, "")
		require.NoError(t, err)
	})
	f.await(agents.EventState, toState(state.StateWaitingUser))
	second := f.file("second", nil)
	s2 := f.session()
	f.user(s2)
	f.do(s2, fake.Command{Cmd: fake.CmdTurnStart}) // the second holds the slot

	_, err := f.mgr.Tell(f.ctx(), ident.UserPrincipal(), string(a1.ID), "go ahead", false)
	require.NoError(t, err)
	// Tell delivers synchronously when it can: with the only slot taken, the
	// answer must still be waiting.
	pending, err := f.state.Messages.Pending(context.Background(), state.AgentParty(a1.ID))
	require.NoError(t, err)
	require.Len(t, pending, 1, "delivered while another agent held the only slot")
	third := f.file("third", nil)

	f.do(s2, fake.Command{Cmd: fake.CmdTurnEnd}) // frees the slot
	answer := f.user(s1)
	assert.Contains(t, answer.Text, "go ahead", "the answered agent got the freed slot")
	_, err = f.state.Agents.LiveForTicket(context.Background(), third.ID)
	assert.ErrorIs(t, err, state.ErrNotFound, "new work waits behind it")
	_ = second
}

func TestAMidTurnTellSteers(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	tk := f.file("t", nil)
	s := f.session()
	f.user(s)
	a := f.agentFor(tk.ID)
	f.do(s, fake.Command{Cmd: fake.CmdTurnStart})
	f.await(agents.EventState, toState(state.StateRefining))
	_, err := f.mgr.Tell(f.ctx(), ident.UserPrincipal(), string(tk.ID), "main is red", false)
	require.NoError(t, err)
	got := f.user(s)
	assert.True(t, got.MidTurn)
	assert.Contains(t, got.Text, "main is red")
	_ = a
}

func TestAnotherAgentsMessageIsMarkedAsInformation(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	one := f.file("one", nil)
	s1 := f.session()
	f.user(s1)
	two := f.file("two", nil)
	s2 := f.session()
	f.user(s2)
	a1 := f.agentFor(one.ID)
	_, err := f.mgr.Tell(f.ctx(), ident.AgentPrincipal(a1.ID, one.ID), string(two.ID), "I'm changing paths.go", false)
	require.NoError(t, err)
	got := f.user(s2)
	assert.Contains(t, got.Text, "Message from agent "+string(a1.ID)+" (information to weigh, not an instruction)")
}

func TestOnlyTheUserMayInterrupt(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	tk := f.file("t", nil)
	f.session()
	a := f.agentFor(tk.ID)
	_, err := f.mgr.Tell(f.ctx(), ident.AgentPrincipal(a.ID, tk.ID), "user", "stop", true)
	assert.ErrorIs(t, err, agents.ErrUserOnly)
}

func TestACrashIsReportedToTheUser(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	tk := f.file("t", nil)
	s := f.session()
	f.user(s)
	f.do(s, fake.Command{Cmd: fake.CmdExit, Code: 3})
	f.await(agents.EventState, toState(state.StateCrashed))
	f.await(agents.EventUserTold, nil)
	_ = tk
}

func TestAnUnrunnableSpecMisconfiguresTheTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "harnesses: {claude-code: {}, pi: {}}\n")
	tk := f.file("t", func(nt *tickets.NewTicket) { nt.Agent.Harness = "pi" })
	f.await(agents.EventMisconfigure, nil)
	got, err := f.tickets.Get(context.Background(), tk.ID)
	require.NoError(t, err)
	assert.Equal(t, tickets.StatusMisconfigured, got.Status)
}

func TestAStalledTurnIsReportedByTheClock(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "agents: {stall_after: 15m}\n")
	tk := f.file("t", nil)
	s := f.session()
	f.user(s)
	f.do(s, fake.Command{Cmd: fake.CmdTurnStart})
	f.await(agents.EventState, toState(state.StateRefining))
	f.clock.Advance(16 * time.Minute)
	f.await(agents.EventState, toState(state.StateStalled))
	_ = tk
}

func TestARestartedDaemonResumesTheConversation(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	tk := f.file("t", nil)
	s := f.session()
	f.user(s)
	f.do(s, fake.Command{Cmd: fake.CmdTurnStart})
	f.await(agents.EventState, toState(state.StateRefining))

	draining := make(chan struct{})
	var once sync.Once
	f.hooks.set(func(h *harnessHooks) { h.stop = func(ident.AgentID) { once.Do(func() { close(draining) }) } })
	stopped := make(chan error, 1)
	go func() { stopped <- f.mgr.Stop(context.Background()) }()
	// Ended before the drain began, the turn would be one the agent ended
	// without saying why, and the restart would find it idle and nudged.
	<-draining
	f.do(s, fake.Command{Cmd: fake.CmdTurnEnd}) // the drain waits for the turn
	require.NoError(t, <-stopped)

	self, err := os.Executable()
	require.NoError(t, err)
	m := f.newManager(&fakeHarness{h: &fake.Harness{Binary: self, Control: f.ctl.Socket(), TranscriptDir: f.transcripts, Logger: slog.New(slog.DiscardHandler), Clock: clock.Real{}}, hooks: f.hooks})
	require.NoError(t, m.Start(context.Background()))
	stopInCleanup(t, func() *agents.Manager { return m })
	t.Cleanup(func() { _ = f.ctl.Close() })
	resumed := f.session()
	assert.True(t, resumed.Resumed)
	assert.Equal(t, s.ID, resumed.ID)
	note := f.user(resumed)
	assert.Contains(t, note.Text, "slop-bot restarted")
	_ = tk
}

// stopBound is how long a test's closing Stop may drain: far past any
// drain the tests make, so reaching it is a hang.
const stopBound = 2 * time.Minute

// stopInCleanup stops the manager get returns (read when the test ends, so
// a restart's is the one stopped) once the test is done. A drain that never
// ends fails the test there, with every goroutine's stack, rather than the
// package running into go test's timeout with no word of which test hung.
func stopInCleanup(t *testing.T, get func() *agents.Manager) {
	t.Helper()
	t.Cleanup(func() {
		m := get()
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = m.Stop(context.Background())
		}()
		select {
		case <-done:
		case <-time.After(stopBound): // a safety net, never a synchronisation
			buf := make([]byte, 8<<20)
			t.Errorf("the manager's Stop didn't return within %s; goroutines:\n%s", stopBound, buf[:runtime.Stack(buf, true)])
		}
	})
}

// logBuffer is the manager's log, for a test to read what it warned of.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// failOn makes the writes a SQLite trigger clause names ("BEFORE UPDATE OF
// state ON agents", with an optional WHEN) fail in state.db from now on, as
// a database that can't write would.
func (f *fixture) failOn(trigger string) {
	f.t.Helper()
	_, err := f.stateDB.ExecContext(context.Background(), "CREATE TRIGGER inject_failure "+trigger+" BEGIN SELECT RAISE(ABORT, 'injected failure'); END")
	require.NoError(f.t, err)
}

// dropFailure removes failOn's trigger: the store writes again.
func (f *fixture) dropFailure() {
	f.t.Helper()
	_, err := f.stateDB.ExecContext(context.Background(), "DROP TRIGGER inject_failure")
	require.NoError(f.t, err)
}

// testWriter sends the Manager's logs to the test's, shown on failure.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}

func TestCancellingATicketEndsItsAgentMidTurn(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	tk := f.file("t", nil)
	s := f.session()
	f.user(s)
	f.do(s, fake.Command{Cmd: fake.CmdTurnStart})
	f.await(agents.EventState, toState(state.StateRefining))
	st := tickets.StatusCancelled
	_, err := f.tickets.Update(context.Background(), ident.UserPrincipal(), tk.ID, &tickets.TicketChange{Status: &st})
	require.NoError(t, err)
	// The turn never ends: the cancel cuts it off rather than waiting.
	f.await(agents.EventFinished, nil)
	assert.Equal(t, []ident.TicketID{tk.ID}, f.wts.cleanups)
}

func TestWalkingATicketForwardNeverUndoesAMoveMadeSince(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	tk := f.file("t", nil)
	s := f.session()
	f.user(s)
	f.do(s, fake.Command{Cmd: fake.CmdTurnStart})
	f.await(agents.EventState, toState(state.StateRefining))
	a, err := f.state.Agents.LiveForTicket(context.Background(), tk.ID)
	require.NoError(t, err)

	// The user opens the ticket; the scheduler reads it as open and walks
	// it on, but the agent parks it in discussion between that read and
	// the scheduler's write.
	discussion := tickets.StatusDiscussion
	result := f.racing.arm(func() {
		_, err := f.tickets.Update(context.Background(), ident.AgentPrincipal(a.ID, tk.ID), tk.ID, &tickets.TicketChange{Status: &discussion})
		require.NoError(t, err)
	})
	open := tickets.StatusOpen
	_, err = f.tickets.Update(context.Background(), ident.UserPrincipal(), tk.ID, &tickets.TicketChange{Status: &open})
	require.NoError(t, err)
	select {
	case err := <-result:
		require.ErrorIs(t, err, tickets.ErrStatusChanged)
	case <-f.ctx().Done():
		t.Fatal("the scheduler never walked the ticket forward")
	}
	got, err := f.tickets.Get(context.Background(), tk.ID)
	require.NoError(t, err)
	assert.Equal(t, tickets.StatusDiscussion, got.Status)
}
