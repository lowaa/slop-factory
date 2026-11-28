package scripted

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/scriptedmodel"
)

// postMessages sends one Anthropic request with the given messages and
// returns the response body, so a test can see what the model answered.
func postMessages(t *testing.T, p *Puppet, messages []any) string {
	t.Helper()
	body := map[string]any{"model": "m", "messages": messages, "tools": []any{map[string]any{"name": "Bash"}}}
	data, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, p.server.URL()+"/v1/messages", bytes.NewReader(data))
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(out)
}

func userText(text string) []any {
	return []any{map[string]any{"role": "user", "content": text}}
}

// toolResultThen is the request a harness sends after a tool ends: the
// result, and any steered text folded in beside it.
func toolResultThen(result, text string) []any {
	return []any{map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": result},
		map[string]any{"type": "text", "text": text},
	}}}
}

func (p *Puppet) startSession(from int, resumed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cur = &session{started: started{id: "c1", resumed: resumed}, from: from}
}

// readFifo reads what is written to path, in the background, and delivers it
// on the returned channel.
func readFifo(path string) <-chan string {
	got := make(chan string, 1)
	go func() {
		data, err := os.ReadFile(path)
		if err != nil {
			got <- "read error: " + err.Error()
			return
		}
		got <- string(data)
	}()
	return got
}

func TestLongToolScriptsAShellCommandThatBlocksOnAFifo(t *testing.T) {
	t.Parallel()
	p := newBarePuppet(t)
	release, err := p.LongTool(t.Context())
	require.NoError(t, err)
	_, long := p.current()
	require.NotNil(t, long)

	info, err := os.Stat(long.fifo)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeNamedPipe, "the gate is a named pipe")
	assert.Equal(t, 1, p.server.Remaining(), "the turn after the tool is scripted only at release")

	answer := postMessages(t, p, userText("go"))
	assert.Contains(t, answer, "cat '"+long.fifo+"'")

	got := readFifo(long.fifo)
	require.NoError(t, release(t.Context()))
	assert.Equal(t, long.marker+"\n", <-got)
	assert.Equal(t, 1, p.server.Remaining(), "release scripted the closing reply")
}

func TestEachLongToolHasItsOwnFifoAndMarker(t *testing.T) {
	t.Parallel()
	p := newBarePuppet(t)
	_, err := p.LongTool(t.Context())
	require.NoError(t, err)
	_, first := p.current()
	_, err = p.LongTool(t.Context())
	require.NoError(t, err)
	_, second := p.current()

	assert.NotEqual(t, first.fifo, second.fifo)
	assert.NotEqual(t, first.marker, second.marker)
}

func TestLongToolFailsWhenTheFifoCannotBeMade(t *testing.T) {
	t.Parallel()
	p := newBarePuppet(t)
	require.NoError(t, os.RemoveAll(p.dir))
	_, err := p.LongTool(t.Context())
	assert.ErrorContains(t, err, "scripted:")
}

func TestReleaseGivesUpWhenNothingReadsTheFifo(t *testing.T) {
	t.Parallel()
	p := newBarePuppet(t)
	_, err := p.LongTool(t.Context())
	require.NoError(t, err)
	_, long := p.current()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assert.ErrorContains(t, long.Release(ctx), "never read its FIFO")
}

func TestUnblockFreesACommandStillReadingTheFifo(t *testing.T) {
	t.Parallel()
	p := newBarePuppet(t)
	_, err := p.LongTool(t.Context())
	require.NoError(t, err)
	_, long := p.current()

	// A reader of the test's own stands in for the stuck command, so the
	// write cannot be missed by an open that has not happened yet.
	reader, err := os.OpenFile(long.fifo, os.O_RDONLY|unix.O_NONBLOCK, 0)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	long.Unblock()
	buf := make([]byte, 64)
	n, err := reader.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "cleanup\n", string(buf[:n]))
}

func TestUnblockWithNoReaderReturns(t *testing.T) {
	t.Parallel()
	p := newBarePuppet(t)
	_, err := p.LongTool(t.Context())
	require.NoError(t, err)
	_, long := p.current()
	long.Unblock()
}

func TestReceivedTellsMidTurnFromNewTurn(t *testing.T) {
	t.Parallel()
	p := newBarePuppet(t)
	_, err := p.LongTool(t.Context())
	require.NoError(t, err)
	_, long := p.current()
	require.NoError(t, p.Reply(t.Context(), "after the tool"))
	require.NoError(t, p.Reply(t.Context(), "after the new turn"))
	p.startSession(0, false)

	postMessages(t, p, userText("start"))
	postMessages(t, p, toolResultThen(long.marker, "STEER"))
	postMessages(t, p, userText("NEXT"))

	require.NoError(t, p.Received(t.Context(), "STEER", true))
	assert.ErrorContains(t, p.Received(t.Context(), "STEER", false), "mid-turn=true, want false")
	require.NoError(t, p.Received(t.Context(), "NEXT", false))
	assert.ErrorContains(t, p.Received(t.Context(), "NEXT", true), "mid-turn=false, want true")
	assert.ErrorContains(t, p.Received(t.Context(), "LOST", false), "never sent")
}

func TestReceivedWithoutALongToolIsNeverMidTurn(t *testing.T) {
	t.Parallel()
	p := newBarePuppet(t)
	require.NoError(t, p.Reply(t.Context(), "ok"))
	p.startSession(0, false)
	postMessages(t, p, userText("hello"))

	require.NoError(t, p.Received(t.Context(), "hello", false))
	assert.Error(t, p.Received(t.Context(), "hello", true))
}

func TestReceivedIgnoresRequestsOfAnEarlierSession(t *testing.T) {
	t.Parallel()
	p := newBarePuppet(t)
	require.NoError(t, p.Reply(t.Context(), "one"))
	require.NoError(t, p.Reply(t.Context(), "two"))
	postMessages(t, p, userText("OLD"))
	p.startSession(1, false)
	postMessages(t, p, userText("NEW"))

	require.NoError(t, p.Received(t.Context(), "NEW", false))
	assert.ErrorContains(t, p.Received(t.Context(), "OLD", false), "never sent")
}

func TestPuppetChecksNeedASession(t *testing.T) {
	t.Parallel()
	p := newBarePuppet(t)
	assert.ErrorContains(t, p.Received(t.Context(), "x", false), "no session has started")
	assert.ErrorContains(t, p.Remembers(t.Context(), "x"), "not a resume")
	assert.ErrorContains(t, p.Crash(t.Context()), "no session has started")
	assert.ErrorContains(t, p.Interrupted(t.Context()), "no long tool")
	p.startSession(0, false)
	assert.ErrorContains(t, p.Remembers(t.Context(), "x"), "not a resume")
	assert.ErrorContains(t, p.Interrupted(t.Context()), "no long tool")
}

func TestInterruptedWantsTheToolCalledAndNothingElseAsked(t *testing.T) {
	t.Parallel()
	p := newBarePuppet(t)
	_, err := p.LongTool(t.Context())
	require.NoError(t, err)
	p.startSession(0, false)

	assert.ErrorContains(t, p.Interrupted(t.Context()), "unplayed")
	postMessages(t, p, userText("go"))
	require.NoError(t, p.Interrupted(t.Context()))

	// The harness carried on: the model is asked for a turn the script lacks.
	postMessages(t, p, toolResultThen("x", "y"))
	assert.ErrorContains(t, p.Interrupted(t.Context()), "carried on")
	// The unexpected request is the point of the test, not a failure of it.
	p.server.Reset()
}

func TestCrashKillsTheSessionsProcess(t *testing.T) {
	t.Parallel()
	p := newBarePuppet(t)
	cmd := exec.CommandContext(t.Context(), "sleep", "60")
	require.NoError(t, cmd.Start())
	p.mu.Lock()
	p.cur = &session{pid: cmd.Process.Pid}
	p.mu.Unlock()

	require.NoError(t, p.Crash(t.Context()))
	var exit *exec.ExitError
	require.ErrorAs(t, cmd.Wait(), &exit)
	assert.Equal(t, "signal: killed", exit.Error())
}

func TestAcceptedHasNothingToWaitOn(t *testing.T) {
	t.Parallel()
	assert.NoError(t, newBarePuppet(t).Accepted(t.Context(), "x"))
}

func TestBriefingIsCheckedOnEveryConversationRequest(t *testing.T) {
	t.Parallel()
	server, err := scriptedmodel.New(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	p, err := New(t, &Options{Server: server, Shell: &Shell{Tool: "Bash"}, Briefing: "BRIEF"})
	require.NoError(t, err)
	require.NoError(t, p.Reply(t.Context(), "ok"))
	postMessages(t, p, userText("BRIEF me"))
}

type fakeHarness struct {
	harness.AIHarness
	err error
	// boot runs in Start, before it returns: a harness's own first turn.
	boot func()
	// last is the conversation Start made last.
	last *fakeConversation
}

func (f *fakeHarness) Start(_ context.Context, _ *harness.Workspace, resume *harness.ConversationRef) (harness.Conversation, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.boot != nil {
		f.boot()
	}
	id := "fresh"
	if resume != nil {
		id = resume.ID
	}
	f.last = &fakeConversation{ref: harness.ConversationRef{ID: id}}
	return f.last, nil
}

type fakeConversation struct {
	harness.Conversation
	ref     harness.ConversationRef
	stopped bool
}

func (c *fakeConversation) Ref() harness.ConversationRef { return c.ref }

func (c *fakeConversation) Stop(context.Context, bool) error {
	c.stopped = true
	return nil
}

// A boot reply is the answer to the harness's own first turn, and a start
// that never asked for it (agy without a sign-in it can use, whose boot
// turn fails and still ends) is not a start on the scripted model.
func TestABootReplyMustBePlayedByTheStart(t *testing.T) {
	t.Parallel()
	server, err := scriptedmodel.New(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	p, err := New(t, &Options{Server: server, Shell: &Shell{Tool: "Bash"}, BootReply: "ready"})
	require.NoError(t, err)

	booted := p.Harness(&fakeHarness{boot: func() {
		assert.Contains(t, postMessages(t, p, userText("start up")), "ready")
	}})
	_, err = booted.Start(t.Context(), &harness.Workspace{}, nil)
	require.NoError(t, err)
	assert.Zero(t, p.server.Remaining())

	plain := &fakeHarness{}
	unbooted := p.Harness(plain)
	_, err = unbooted.Start(t.Context(), &harness.Workspace{}, &harness.ConversationRef{ID: "c1"})
	require.NoError(t, err, "a resume runs no boot turn, and none is scripted")
	assert.Zero(t, p.server.Remaining())
	_, err = unbooted.Start(t.Context(), &harness.Workspace{}, nil)
	require.ErrorContains(t, err, "boot turn")
	assert.True(t, plain.last.stopped, "the harness isn't left running")
	p.server.Reset() // the unplayed boot reply is the point, not a leftover
}

func TestStartedReturnsTheLaunchedConversation(t *testing.T) {
	t.Parallel()
	p := newBarePuppet(t)
	_, err := p.LongTool(t.Context())
	require.NoError(t, err)
	_, before := p.current()
	require.NotNil(t, before)
	p.server.Reset() // nothing scripted is left unplayed for the cleanup to flag

	wrapped := p.Harness(&fakeHarness{})
	p.pids <- 4242
	_, err = wrapped.Start(t.Context(), &harness.Workspace{}, &harness.ConversationRef{ID: "old"})
	require.NoError(t, err)

	id, resumed, err := p.Started(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "old", id)
	assert.True(t, resumed)
	cur, long := p.current()
	assert.Equal(t, 4242, cur.pid)
	assert.Nil(t, long, "a new session has no long tool yet")
}

func TestStartedReportsAFreshConversation(t *testing.T) {
	t.Parallel()
	p := newBarePuppet(t)
	wrapped := p.Harness(&fakeHarness{})
	p.pids <- 1
	_, err := wrapped.Start(t.Context(), &harness.Workspace{}, nil)
	require.NoError(t, err)
	id, resumed, err := p.Started(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "fresh", id)
	assert.False(t, resumed)
}

func TestFailedStartIsNotReportedAsStarted(t *testing.T) {
	t.Parallel()
	p := newBarePuppet(t)
	wrapped := p.Harness(&fakeHarness{err: assert.AnError})
	_, err := wrapped.Start(t.Context(), &harness.Workspace{}, nil)
	require.ErrorIs(t, err, assert.AnError)
	assert.Empty(t, p.starts)
}

func TestStartedGivesUpWhenNothingLaunchedOrStarted(t *testing.T) {
	t.Parallel()
	p := newBarePuppet(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := p.Started(ctx)
	assert.ErrorContains(t, err, "no harness was launched")

	p.pids <- 7
	_, _, err = p.Started(ctx)
	assert.ErrorContains(t, err, "no conversation started")
}

func TestLauncherNamesTheTestBinaryAndThePuppetsSocket(t *testing.T) {
	t.Parallel()
	p := newBarePuppet(t)
	self, err := os.Executable()
	require.NoError(t, err)

	bin, env, err := p.Launcher("/bin/harness")
	require.NoError(t, err)
	assert.Equal(t, self, bin)
	assert.Equal(t, map[string]string{
		envLaunchTarget: "/bin/harness",
		envLaunchSocket: filepath.Join(p.dir, "launch.sock"),
	}, env)
}

func TestLaunchesReachStartedWhateverElseIsWrittenToTheSocket(t *testing.T) {
	t.Parallel()
	p := newBarePuppet(t)
	_, env, err := p.Launcher("x")
	require.NoError(t, err)
	socket := env[envLaunchSocket]

	// Noise first: a launcher that wrote junk must not wedge the accept loop
	// or reach Started as a pid.
	conn, err := net.Dial("unix", socket)
	require.NoError(t, err)
	_, err = conn.Write([]byte("not a pid\n"))
	require.NoError(t, err)
	require.NoError(t, conn.Close())

	require.NoError(t, reportPid(socket, 31337))
	assert.Equal(t, 31337, <-p.pids)
}

func TestReportPidFailsWithoutAListener(t *testing.T) {
	t.Parallel()
	assert.Error(t, reportPid(filepath.Join(t.TempDir(), "none.sock"), 1))
}

func TestLaunchEnvDropsOnlyTheLauncherVariables(t *testing.T) {
	t.Parallel()
	got := launchEnv([]string{"A=1", envLaunchTarget + "=/x", "B=2", envLaunchSocket + "=/s", envLaunchTarget + "X=keep"})
	assert.Equal(t, []string{"A=1", "B=2", envLaunchTarget + "X=keep"}, got)
}

func TestLaunchFailsBeforeExecWhenThePuppetIsGone(t *testing.T) {
	t.Parallel()
	assert.Error(t, launch("/bin/true", filepath.Join(t.TempDir(), "none.sock")))
}

func TestSmallHelpers(t *testing.T) {
	t.Parallel()
	assert.Equal(t, `'a'\''b'`, shellQuote("a'b"))
	assert.Equal(t, "short", tail("short", 10))
	assert.Equal(t, "...cdef", tail("abcdef", 4))
	long := summarise([]*scriptedmodel.Request{{MessageText: strings.Repeat("x", 300), ToolResults: []string{"r"}}})
	assert.Contains(t, long, "1 requests")
	assert.Contains(t, long, `tool results ["r"]`)
	assert.Contains(t, long, "...")
	assert.Equal(t, -1, indexOf(nil, func(*scriptedmodel.Request) bool { return true }))
}
