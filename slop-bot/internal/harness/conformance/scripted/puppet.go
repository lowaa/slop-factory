// Package scripted is the conformance Puppet for the real harness CLIs: it
// makes Claude Code, Codex, pi or agy do what the suite asks by scripting the
// model they talk to (scriptedmodel), and checks what they did by what that
// model was sent. One puppet serves every wire API; only the harness's shell
// tool differs (Shell).
//
// Two things the model cannot do are done from outside: a long tool call is
// a shell command the model runs that blocks on a FIFO until the test writes
// to it, and a crash is a SIGKILL to the harness's pid, which a small
// launcher (Launcher, ExecIfLauncher) reports before it execs the harness.
package scripted

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/sys/unix"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/scriptedmodel"
)

// Shell is a harness's shell tool, as its model calls it.
type Shell struct {
	// Tool is its name, e.g. "Bash".
	Tool string
	// Input is its input for running command.
	Input func(command string) map[string]any
}

// Options configure a Puppet.
type Options struct {
	Server *scriptedmodel.Server
	Shell  *Shell
	// Briefing, when set, is text every request of the conversation must
	// carry, in its system prompt or its messages: the check that the
	// harness delivered the briefing.
	Briefing string
	// BootReply, when set, is scripted as the model's answer to a turn the
	// harness runs by itself at each new conversation, before slop-bot sends
	// anything (agy's boot prompt, which it needs to name a conversation).
	// Without it that turn would take the test's first scripted turn.
	BootReply string
}

// Puppet implements conformance.Puppet for one Subject: one conversation at
// a time, the latest started being the current one.
type Puppet struct {
	t         *testing.T
	server    *scriptedmodel.Server
	shell     *Shell
	briefing  string
	bootReply string
	// dir holds the launcher's socket and the long tools' FIFOs. It is not a
	// t.TempDir: a socket path has a length limit those can exceed.
	dir      string
	listener net.Listener
	pids     chan int
	starts   chan *started

	mu    sync.Mutex
	cur   *session
	long  *longTool
	longs []*longTool
	// procs are pidfds of the harnesses launched (proc_linux.go).
	procs []int
}

type started struct {
	id      string
	resumed bool
}

type session struct {
	started
	pid int
	// from is where the session's requests start in the recording: those
	// before it are an earlier session's.
	from int
}

type longTool struct {
	fifo   string
	marker string
}

// New makes a puppet for one test and loads an empty scenario, which the
// puppet's methods extend turn by turn. The test's cleanup checks that the
// model saw no unexpected request and, with a Briefing, that every request
// carried it.
func New(t *testing.T, opts *Options) (*Puppet, error) {
	dir, err := os.MkdirTemp("", "slop-t2-")
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", filepath.Join(dir, "launch.sock"))
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := opts.Server.Load(&scriptedmodel.Scenario{Name: "conformance", Turns: []*scriptedmodel.Turn{}}); err != nil {
		_ = listener.Close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	p := &Puppet{
		t: t, server: opts.Server, shell: opts.Shell, briefing: opts.Briefing, bootReply: opts.BootReply,
		dir: dir, listener: listener,
		// Buffered: a launch is reported whether or not Started is waiting.
		pids:   make(chan int, 16),
		starts: make(chan *started, 16),
	}
	go p.acceptLaunches()
	t.Cleanup(p.close)
	return p, nil
}

// Harness wraps h so the puppet knows each conversation it starts: its id,
// and whether it was a resume.
func (p *Puppet) Harness(h harness.AIHarness) harness.AIHarness {
	return &puppeted{AIHarness: h, p: p}
}

type puppeted struct {
	harness.AIHarness
	p *Puppet
}

func (h *puppeted) Start(ctx context.Context, ws *harness.Workspace, resume *harness.ConversationRef) (harness.Conversation, error) {
	// Scripted before the start, which waits for the boot turn to end: agy
	// is ready only once it has.
	boot := resume == nil && h.p.bootReply != ""
	unplayed := h.p.server.Remaining()
	if boot {
		if err := h.p.Reply(ctx, h.p.bootReply); err != nil {
			return nil, err
		}
	}
	conv, err := h.AIHarness.Start(ctx, ws, resume)
	if err != nil {
		return nil, err
	}
	// A boot turn that failed without reaching the model (agy with no
	// sign-in it can use) still ends, and so still makes the harness ready.
	if boot && h.p.server.Remaining() != unplayed {
		_ = conv.Stop(ctx, true)
		return nil, errors.New("scripted: the harness was ready without asking the model for its boot turn")
	}
	h.p.starts <- &started{id: conv.Ref().ID, resumed: resume != nil}
	return conv, nil
}

// Started implements conformance.Puppet: it returns once the harness's
// process has been launched (the launcher reported its pid) and its
// conversation started.
func (p *Puppet) Started(ctx context.Context) (string, bool, error) {
	pid, err := receive(ctx, p.pids, "no harness was launched")
	if err != nil {
		return "", false, err
	}
	st, err := receive(ctx, p.starts, "no conversation started")
	if err != nil {
		return "", false, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cur = &session{started: *st, pid: pid, from: len(p.server.Recording().Requests)}
	p.long = nil
	return st.id, st.resumed, nil
}

// receive takes what is already waiting on ch before it looks at ctx: with
// both ready, select would pick at random, and a report that did arrive must
// not be lost to a context that has since ended.
func receive[T any](ctx context.Context, ch <-chan T, what string) (T, error) {
	select {
	case v := <-ch:
		return v, nil
	default:
	}
	select {
	case v := <-ch:
		return v, nil
	case <-ctx.Done():
		var zero T
		return zero, fmt.Errorf("scripted: %s: %w", what, ctx.Err())
	}
}

// Reply implements conformance.Puppet: the model's next turn is text.
func (p *Puppet) Reply(_ context.Context, text string) error {
	return p.server.Append(&scriptedmodel.Turn{Text: text, Usage: scriptedmodel.Usage{InputTokens: 10, OutputTokens: 2}})
}

// LongTool implements conformance.Puppet: the model's next turn runs a shell
// command that reads a FIFO, so the tool call lasts until release writes to
// it. Only then is the turn after it scripted: the model is not called again
// until the command ends, and an interrupted tool must not leave a reply in
// the script for the next turn to receive.
func (p *Puppet) LongTool(context.Context) (func(context.Context) error, error) {
	p.mu.Lock()
	n := len(p.longs) + 1
	l := &longTool{fifo: filepath.Join(p.dir, fmt.Sprintf("long-%d.fifo", n)), marker: fmt.Sprintf("long-tool-%d-released", n)}
	p.longs = append(p.longs, l)
	p.long = l
	p.mu.Unlock()
	if err := unix.Mkfifo(l.fifo, 0o600); err != nil {
		return nil, fmt.Errorf("scripted: %w", err)
	}
	call := &scriptedmodel.ToolCall{Name: p.shell.Tool, Input: p.shell.Input("cat " + shellQuote(l.fifo))}
	if err := p.server.Append(&scriptedmodel.Turn{ToolCalls: []*scriptedmodel.ToolCall{call}, Usage: scriptedmodel.Usage{InputTokens: 10, OutputTokens: 4}}); err != nil {
		return nil, err
	}
	return func(ctx context.Context) error {
		if err := p.Reply(ctx, "the long tool is done"); err != nil {
			return err
		}
		return l.release(ctx)
	}, nil
}

// release writes the marker to the FIFO. Opening it for writing waits for the
// command to open it for reading, so it is also the point at which the tool
// is known to be running.
func (l *longTool) release(ctx context.Context) error {
	done := make(chan error, 1)
	go func() {
		f, err := os.OpenFile(l.fifo, os.O_WRONLY, 0)
		if err != nil {
			done <- err
			return
		}
		_, err = f.WriteString(l.marker + "\n")
		done <- errors.Join(err, f.Close())
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// A reader of our own lets the blocked open return, so the
		// goroutine does not outlive the test.
		var writeErr error
		if f, err := os.OpenFile(l.fifo, os.O_RDONLY|unix.O_NONBLOCK, 0); err == nil {
			writeErr = <-done
			_ = f.Close()
		}
		return fmt.Errorf("scripted: the long tool never read its FIFO: %w", errors.Join(ctx.Err(), writeErr))
	}
}

// unblock ends a command still waiting on the FIFO, so a test that never
// released it (an interrupt, a forced stop) leaves no process behind, should
// the harness have left it running. With no reader there is nothing to do.
func (l *longTool) unblock() {
	f, err := os.OpenFile(l.fifo, os.O_WRONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return
	}
	_, _ = f.WriteString("cleanup\n")
	_ = f.Close()
}

// Accepted implements conformance.Puppet. A real harness shows a steer to
// nobody before its next model call, so there is nothing to wait on: the
// steer is in its stdin before release, and each harness reads stdin while a
// tool runs, so it has it before the tool's end moves the turn on.
func (p *Puppet) Accepted(context.Context, string) error { return nil }

// Received implements conformance.Puppet. The harness received text when the
// model was sent it. It came mid-turn when the first request carrying it is
// the one that delivered the long tool's result: the harness folded it into
// the running turn rather than starting a turn for it.
func (p *Puppet) Received(_ context.Context, text string, midTurn bool) error {
	cur, long := p.current()
	if cur == nil {
		return errors.New("scripted: no session has started")
	}
	reqs := p.requests(cur.from)
	first := indexOf(reqs, func(r *scriptedmodel.Request) bool { return strings.Contains(r.MessageText, text) })
	if first < 0 {
		return fmt.Errorf("scripted: the model was never sent %q in this session (%s)", text, summarise(reqs))
	}
	delivered := -1
	if long != nil {
		delivered = indexOf(reqs, func(r *scriptedmodel.Request) bool {
			return strings.Contains(strings.Join(r.ToolResults, "\n"), long.marker)
		})
	}
	if got := first == delivered; got != midTurn {
		return fmt.Errorf("scripted: %q reached the model mid-turn=%v, want %v (%s)", text, got, midTurn, summarise(reqs))
	}
	return nil
}

// Remembers implements conformance.Puppet: an earlier session's model was
// sent text, and so was this resumed session's, as the history it replays.
func (p *Puppet) Remembers(_ context.Context, text string) error {
	cur, _ := p.current()
	if cur == nil || !cur.resumed {
		return errors.New("scripted: the current session is not a resume")
	}
	has := func(r *scriptedmodel.Request) bool { return strings.Contains(r.MessageText, text) }
	earlier, resumed := p.split(cur.from)
	if indexOf(earlier, has) < 0 {
		return fmt.Errorf("scripted: no earlier session sent the model %q", text)
	}
	if indexOf(resumed, has) < 0 {
		return fmt.Errorf("scripted: the resumed session never sent the model %q (%s)", text, summarise(resumed))
	}
	return nil
}

// Interrupted implements conformance.Puppet. The harness reported the turn
// interrupted (the suite asserts that); what the model shows is that the
// turn stopped there. The long tool's call was played, and the turn after it
// is scripted only at release, so a harness that carried on with the turn
// anyway would have asked for a turn the script lacks: a recorded error.
func (p *Puppet) Interrupted(context.Context) error {
	cur, long := p.current()
	if cur == nil || long == nil {
		return errors.New("scripted: no long tool to interrupt")
	}
	if rec := p.server.Recording(); len(rec.Errors) > 0 {
		return fmt.Errorf("scripted: the harness carried on, or asked something unexpected: %v", rec.Errors)
	}
	if n := p.server.Remaining(); n != 0 {
		return fmt.Errorf("scripted: %d scripted turn(s) unplayed: the long tool was never called", n)
	}
	return nil
}

// Crash implements conformance.Puppet: SIGKILL, so the harness gets no say.
func (p *Puppet) Crash(context.Context) error {
	cur, _ := p.current()
	if cur == nil {
		return errors.New("scripted: no session has started")
	}
	return syscall.Kill(cur.pid, syscall.SIGKILL)
}

func (p *Puppet) current() (*session, *longTool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cur, p.long
}

// requests are the conversation's requests (not the harness's housekeeping)
// from index from of the recording on.
func (p *Puppet) requests(from int) []*scriptedmodel.Request {
	_, after := p.split(from)
	return after
}

// split is the conversation's requests before and after index at of the
// recording. at counts the housekeeping requests too, so each side is
// filtered on its own.
func (p *Puppet) split(at int) (before, after []*scriptedmodel.Request) {
	rec := p.server.Recording()
	before = (&scriptedmodel.Recording{Requests: rec.Requests[:at]}).Conversation()
	after = (&scriptedmodel.Recording{Requests: rec.Requests[at:]}).Conversation()
	return before, after
}

func (p *Puppet) close() {
	p.endLaunched()
	p.mu.Lock()
	longs := p.longs
	p.mu.Unlock()
	for _, l := range longs {
		l.unblock()
	}
	_ = p.listener.Close()
	_ = os.RemoveAll(p.dir)

	rec := p.server.Recording()
	assert.Empty(p.t, rec.Errors, "the scripted model saw unexpected requests")
	if p.briefing == "" {
		return
	}
	for i, r := range rec.Conversation() {
		assert.Contains(p.t, r.System+"\n"+r.MessageText, p.briefing, "request %d did not carry the briefing", i)
	}
}

func indexOf(reqs []*scriptedmodel.Request, match func(*scriptedmodel.Request) bool) int {
	for i, r := range reqs {
		if match(r) {
			return i
		}
	}
	return -1
}

// summarise is a failure's context: what each request carried.
func summarise(reqs []*scriptedmodel.Request) string {
	parts := make([]string, len(reqs))
	for i, r := range reqs {
		parts[i] = fmt.Sprintf("request %d: tool results %q, text %q", i, r.ToolResults, tail(r.MessageText, 200))
	}
	return fmt.Sprintf("%d requests: %s", len(reqs), strings.Join(parts, "; "))
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

// shellQuote is s as one POSIX shell word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
