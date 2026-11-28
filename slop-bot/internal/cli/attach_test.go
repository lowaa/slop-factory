package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
)

// terminal is `slop-bot attach` run as a person runs it: its own process,
// in a terminal of its own (a pty).
type terminal struct {
	t   *testing.T
	cmd *exec.Cmd
	pty *os.File

	mu  sync.Mutex
	out bytes.Buffer
	// wrote is signalled whenever the terminal shows more.
	wrote chan struct{}
	// read is closed once the terminal has nothing left to show.
	read   chan struct{}
	exited chan int
}

func (e *e2e) attachTerminal(args ...string) *terminal {
	e.t.Helper()
	cmd := exec.Command(e.self, append([]string{"attach"}, args...)...) // #nosec G204 -- the test binary
	// TERM: a joined attach runs a tmux client, which needs one, and the
	// test's own environment may have none.
	cmd.Env = append(os.Environ(), cliReexec+"=1", fakeReexec+"=", control.TokenEnv+"=",
		"HOME="+e.h.home, "XDG_RUNTIME_DIR="+e.h.runtime, "GORACE=atexit_sleep_ms=0", "TERM=xterm-256color")
	f, err := pty.Start(cmd)
	require.NoError(e.t, err)
	term := &terminal{t: e.t, cmd: cmd, pty: f, read: make(chan struct{}), exited: make(chan int, 1), wrote: make(chan struct{}, 1)}
	go func() {
		defer close(term.read)
		_, _ = io.Copy(term, f)
	}()
	go func() {
		err := cmd.Wait()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		term.exited <- code
	}()
	e.t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = f.Close()
	})
	return term
}

func (term *terminal) Write(p []byte) (int, error) {
	term.mu.Lock()
	defer term.mu.Unlock()
	select {
	case term.wrote <- struct{}{}:
	default:
	}
	return term.out.Write(p)
}

// shows waits for the terminal to show text.
func (term *terminal) shows(ctx context.Context, text string) {
	term.t.Helper()
	for !strings.Contains(term.screen(), text) {
		select {
		case <-term.wrote:
		case <-ctx.Done():
			require.FailNow(term.t, "the terminal never showed "+text, term.screen())
		}
	}
}

// typeLine types a line at whatever runs in the terminal.
func (term *terminal) typeLine(line string) {
	term.t.Helper()
	_, err := term.pty.Write([]byte(line + "\r"))
	require.NoError(term.t, err)
}

// done waits for the CLI to exit, and for the terminal to show everything
// it wrote, and returns its exit code and the screen.
func (term *terminal) done(ctx context.Context) (int, string) {
	term.t.Helper()
	var code int
	select {
	case code = <-term.exited:
	case <-ctx.Done():
		require.FailNow(term.t, "attach didn't exit", term.screen())
	}
	// The pty reads EOF once nothing holds the terminal open.
	select {
	case <-term.read:
	case <-ctx.Done():
		require.FailNow(term.t, "the terminal stayed open", term.screen())
	}
	return code, term.screen()
}

func (term *terminal) screen() string {
	term.mu.Lock()
	defer term.mu.Unlock()
	return term.out.String()
}

// working has an agent on a new ticket, in its first turn.
func (e *e2e) working(title string) (*fake.Session, string) {
	e.t.Helper()
	e.h.ticket(e.t, "ticket", "create", "--title", title, "--assign")
	s, err := e.ctl.Next(e.ctx())
	require.NoError(e.t, err)
	_, err = s.Expect(e.ctx(), fake.RepUser)
	require.NoError(e.t, err)
	e.do(s, fake.CmdTurnStart)
	return s, e.await("agent.state", toState("refining")).Agent
}

// tui waits for the attach's TUI to start, and returns it and its pid.
func (e *e2e) tui(conversation string) (*fake.Session, int) {
	e.t.Helper()
	s, err := e.ctl.Next(e.ctx())
	require.NoError(e.t, err)
	require.True(e.t, s.TUI, "the TUI, not a programmatic session")
	assert.Equal(e.t, conversation, s.ID, "on the same conversation")
	var p struct{ PID int }
	require.NoError(e.t, json.Unmarshal(e.await("agent.tui_launched", nil).Payload, &p))
	return s, p.PID
}

// resumed waits for the hand-back's programmatic session and its note.
func (e *e2e) resumed(conversation string) *fake.Report {
	e.t.Helper()
	s, err := e.ctl.Next(e.ctx())
	require.NoError(e.t, err)
	assert.False(e.t, s.TUI)
	assert.True(e.t, s.Resumed)
	assert.Equal(e.t, conversation, s.ID)
	note, err := s.Expect(e.ctx(), fake.RepUser)
	require.NoError(e.t, err)
	assert.Contains(e.t, note.Text, "A person worked with you directly")
	return note
}

func TestAttachHandsTheAgentToATUIAndBack(t *testing.T) {
	t.Parallel()
	e := newE2E(t)
	s, agent := e.working("Add export")
	e.agent(s, "status", "set", "reading the code")

	term := e.attachTerminal(agent)
	e.await("agent.attaching", nil)
	// A message sent while the attach waits for the turn is held for it.
	e.h.ok(t, "tell", agent, "the API moved to v2")
	e.do(s, fake.CmdTurnEnd)
	tui, _ := e.tui(s.ID)

	// The person works in the TUI; from inside it, the agent's own CLI
	// calls still work, on the fresh token the TUI was given.
	term.typeLine("hello")
	typed, err := tui.Await(e.ctx(), fake.RepUser)
	require.NoError(t, err)
	assert.Equal(t, "hello", typed.Text)
	e.agent(tui, "status", "set", "steered by hand")
	assert.Contains(t, e.h.ok(t, "status"), "steered by hand")
	assert.Contains(t, e.h.ok(t, "status"), "attached")

	term.typeLine(fake.TUIQuit)
	code, screen := term.done(e.ctx())
	assert.Equal(t, 0, code, screen)
	note := e.resumed(s.ID)
	assert.Contains(t, note.Text, "the API moved to v2", "the held message, at the hand-back")

	assert.Contains(t, screen, `ticket 1: "Add export"`)
	assert.Contains(t, screen, "state: refining")
	assert.Contains(t, screen, "note: reading the code")
	assert.Contains(t, screen, "the API moved to v2")
	assert.Contains(t, screen, "Quitting the TUI hands the agent back")
	assert.Contains(t, screen, "fake-tui "+s.ID+" (resumed)")
	assert.Contains(t, screen, "back to slop-bot: resumed")
	// The daemon closes the stream as it hands back: that is the attach
	// ending, not the daemon going away.
	assert.NotContains(t, screen, "lost the daemon")
}

func TestASecondAttachIsRefusedAndDetachEndsTheFirst(t *testing.T) {
	t.Parallel()
	e := newE2E(t)
	s, agent := e.working("t")
	term := e.attachTerminal("--now", agent)
	_, err := s.Expect(e.ctx(), fake.RepInterrupt)
	require.NoError(t, err)
	tui, _ := e.tui(s.ID)

	assert.Equal(t, 1, e.h.run(t, "attach", agent))
	assert.Contains(t, e.h.err.String(), fmt.Sprintf("held by slop-bot attach (pid %d on /dev/pts/", term.cmd.Process.Pid))

	assert.Contains(t, e.h.ok(t, "detach", agent), "handed back: resumed")
	_, err = tui.Await(e.ctx(), fake.RepUser)
	require.Error(t, err, "the TUI was ended")
	code, screen := term.done(e.ctx())
	assert.Equal(t, 0, code, screen)
	assert.Contains(t, screen, "detached from another terminal")
	assert.NotContains(t, screen, "lost the daemon")
	e.resumed(s.ID)
}

func TestAHungUpAttachIsHandedBackOnceItsTUIIsGone(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.deps.tuiGrace = time.Millisecond
	e := newE2EFrom(t, h, "", nil)
	s, agent := e.working("t")
	term := e.attachTerminal("--now", agent)
	tui, pid := e.tui(s.ID)
	// A TUI that misses its terminal going away: the daemon must still
	// make sure it's gone before the conversation resumes elsewhere.
	e.do(tui, fake.CmdIgnoreHangup)
	require.NoError(t, term.cmd.Process.Kill())

	e.resumed(s.ID)
	assert.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH, "the TUI was gone before the resume")
	ev := e.await("agent.handed_back", nil)
	assert.Contains(t, string(ev.Payload), "hung up")
}

func TestDoneFromInsideTheTUIFinishesTheTicket(t *testing.T) {
	t.Parallel()
	e := newE2E(t)
	s, agent := e.working("t")
	term := e.attachTerminal("--now", agent)
	tui, _ := e.tui(s.ID)
	e.agent(tui, "done", "--summary", "Finished by hand.")
	term.typeLine(fake.TUIQuit)
	code, screen := term.done(e.ctx())
	assert.Equal(t, 0, code, screen)
	assert.Contains(t, screen, "back to slop-bot: finished")
	e.await("agent.finished", nil)
}

func TestNoResumeReleasesTheAgentToThePerson(t *testing.T) {
	t.Parallel()
	e := newE2E(t)
	s, agent := e.working("mine now")
	term := e.attachTerminal("--now", "--no-resume", agent)
	tui, err := e.ctl.Next(e.ctx())
	require.NoError(t, err)
	assert.True(t, tui.TUI)
	assert.Equal(t, s.ID, tui.ID)
	e.await("agent.state", toState("released"))
	term.typeLine(fake.TUIQuit)
	code, screen := term.done(e.ctx())
	assert.Equal(t, 0, code, screen)
	assert.Contains(t, screen, "Released: slop-bot no longer runs this agent")
	assert.NotContains(t, screen, "back to slop-bot")

	// Eligible tickets are started oldest first: once a newer ticket has an
	// agent, the released one was passed over.
	later := e.h.ticket(t, "ticket", "create", "--title", "later", "--assign")
	e.await("agent.spawned", func(ev *api.Event) bool { return ev.Ticket == later.ID })
	assert.Equal(t, 1, e.count("agent.spawned", "1"), "the released ticket got no new agent")
	assert.Contains(t, e.h.ok(t, "ticket", "show", "1"), "ada", "still assigned")
}

// restart starts the daemon again on the same state, after e.stop.
func (e *e2e) restart() {
	e.t.Helper()
	e.stop = startDaemon(e.t, e.h)
	// Cleanups run last-first: the fakes must exit before this daemon's
	// drain, which would wait on the turn its hand-back started.
	e.t.Cleanup(func() { _ = e.ctl.Close() })
}

// count is how many events of typ the log holds for ticket.
func (e *e2e) count(typ, ticket string) int {
	e.t.Helper()
	body, err := control.NewClient(e.h.runtime+"/slop-bot/slop-bot.sock", "").Stream(e.ctx(), api.PathEvents+"?after=0")
	require.NoError(e.t, err)
	defer func() { _ = body.Close() }()
	n := 0
	sc := bufio.NewScanner(body)
	for sc.Scan() {
		var ev api.Event
		require.NoError(e.t, json.Unmarshal(sc.Bytes(), &ev))
		if ev.Type == typ && ev.Ticket == ticket {
			n++
		}
	}
	return n
}

func TestAnAttachSurvivesADaemonRestart(t *testing.T) {
	t.Parallel()
	e := newE2E(t)
	s, agent := e.working("t")
	term := e.attachTerminal("--now", agent)
	tui, _ := e.tui(s.ID)
	e.stop()
	e.restart()

	// The CLI reconnects to the new daemon; the TUI never noticed.
	e.await("agent.reattached", nil)
	// The new daemon learns the TUI's pid, for a hang-up from here on.
	e.await("agent.tui_launched", nil)
	term.typeLine("still here")
	_, err := tui.Await(e.ctx(), fake.RepUser)
	require.NoError(t, err)
	term.typeLine(fake.TUIQuit)
	code, screen := term.done(e.ctx())
	assert.Equal(t, 0, code, screen)
	assert.Contains(t, screen, "reconnected")
	e.resumed(s.ID)
}

func TestAnAttachWhoseCLIDidntComeBackIsHandedBackWhenItsLeaseExpires(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC))
	e := newE2EOn(t, "user: ada\nagents: {attach_lease: 30s}\n", clk)
	s, agent := e.working("t")
	term := e.attachTerminal("--now", agent)
	tui, _ := e.tui(s.ID)
	e.stop()
	// The terminal is closed while the daemon is down: nothing can tell it.
	require.NoError(t, term.cmd.Process.Kill())
	require.NoError(t, term.pty.Close())
	_, err := tui.Await(e.ctx(), fake.RepUser)
	require.Error(t, err, "the TUI went with its terminal")

	e.restart()
	status := e.h.ok(t, "status")
	assert.Contains(t, status, "attached", "the lease survived the restart")
	clk.Advance(31 * time.Second)
	ev := e.await("agent.handed_back", nil)
	assert.Contains(t, string(ev.Payload), "lease expired")
	e.resumed(s.ID)
	_ = agent
}

// The daemon closes the stream right after telling the CLI the attach is
// over: the CLI ends the TUI rather than reconnecting to a daemon that never
// went away.
func TestAnAttachEndedByTheDaemonEndsTheTUIQuietly(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC))
	e := newE2EOn(t, "user: ada\nagents: {attach_lease: 30s}\n", clk)
	s, agent := e.working("t")
	term := e.attachTerminal("--now", agent)
	e.tui(s.ID)
	e.h.ok(t, "ticket", "cancel", "1")
	e.await("agent.finished", nil)
	// The next lease renewal finds the agent ended.
	clk.Advance(11 * time.Second)
	code, screen := term.done(e.ctx())
	assert.Equal(t, 0, code, screen)
	assert.Contains(t, screen, "handed back without this terminal")
	assert.NotContains(t, screen, "lost the daemon")
}
