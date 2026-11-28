package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
)

// Reconnecting and handing back keep trying through a daemon restart.
// The daemon's lease is the real bound: past it the agent is handed back
// without this CLI, and a reconnect is refused.
const (
	retryEvery   = 500 * time.Millisecond
	retryMax     = 5 * time.Second
	handBackFor  = 2 * time.Minute
	tuiTermGrace = 5 * time.Second
)

func (a *app) attachCmd() *cobra.Command {
	var now, noResume bool
	c := &cobra.Command{
		Use:   "attach <agent|ticket>",
		Short: "Take an agent over in its harness's TUI; quitting the TUI hands it back",
		Long: "Stop the agent's session at a safe point (the end of its turn; --now interrupts it), " +
			"then run its harness's TUI on the same conversation in this terminal. Quitting the TUI " +
			"hands the agent back to slop-bot, which resumes it. With --no-resume, the agent is " +
			"released to you: slop-bot stops running it, and the ticket stays assigned.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			at := &attachRun{
				client: c, stdin: a.deps.Stdin, stdout: cmd.OutOrStdout(), stderr: cmd.ErrOrStderr(),
				holder: newHolder(),
			}
			// Not cmd's context: Ctrl-C in the TUI reaches this process too,
			// and must not end the attach. Signals are handled in run.
			return at.run(context.WithoutCancel(cmd.Context()), args[0], &api.AttachRequest{Now: now, NoResume: noResume})
		},
	}
	c.Flags().BoolVar(&now, "now", false, "interrupt the agent's turn instead of waiting for it to end")
	c.Flags().BoolVar(&noResume, "no-resume", false, "release the agent to you: slop-bot stops running it")
	return c
}

func (a *app) detachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "detach <agent|ticket>",
		Short: "Hand an attached agent back from another terminal, ending its TUI",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out api.HandedBack
			if err := a.do(cmd, http.MethodPost, agentPath(api.PathDetach, args[0]), nil, &out); err != nil {
				return err
			}
			return a.printOK(cmd, "handed back: %s", out.Outcome)
		},
	}
}

func agentPath(pattern, agent string) string {
	return strings.Replace(pattern, "{agent}", url.PathEscape(agent), 1)
}

// newHolder names this CLI for the lease: a second attach is told who has
// the agent. The random part keeps it unique across pid reuse.
func newHolder() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	tty := "a terminal"
	if t, err := os.Readlink("/proc/self/fd/0"); err == nil && strings.HasPrefix(t, "/dev/") {
		tty = t
	}
	return fmt.Sprintf("slop-bot attach (pid %d on %s, %s)", os.Getpid(), tty, hex.EncodeToString(b))
}

// attachRun is one `slop-bot attach`: the stream that holds the lease, and
// the TUI it runs in the foreground.
type attachRun struct {
	client         *control.Client
	stdin          io.Reader
	stdout, stderr io.Writer
	holder         string
	agent          string
	// pid is the TUI's, once it runs.
	pid int
	// ending is set once this attach is over as far as the CLI knows: it is
	// handing back, or the daemon said the attach ended. The daemon closes
	// the stream then, and that is the attach ending, not a restart to
	// reconnect through; a hand-back retries through a restart itself.
	ending atomic.Bool
}

// launched tells the daemon the TUI's pid, so that if this CLI dies the
// daemon can make sure the TUI is gone before resuming the conversation.
// A reconnected daemon is told again: it starts knowing nothing.
func (at *attachRun) launched(ctx context.Context) {
	if err := at.client.Do(ctx, http.MethodPost, agentPath(api.PathLaunched, at.agent), &api.Launched{Holder: at.holder, PID: at.pid}, nil); err != nil {
		at.say("slop-bot: couldn't tell the daemon the TUI's pid: %v", err)
	}
}

func (at *attachRun) run(ctx context.Context, target string, req *api.AttachRequest) error {
	req.Holder = at.holder
	if !req.Now {
		// Said before the daemon answers, so before it is known whether the
		// harness hands over (which waits for the turn) or is joined (which
		// doesn't).
		at.say("Attaching; a hand-over waits for the agent's turn to end (--now interrupts it)...")
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Once the TUI runs it owns the terminal's signals: Ctrl-C and Ctrl-\ are
	// its, and this process only passes on what would otherwise end it.
	// Until then, any of them gives up the wait for the agent's turn.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, os.Interrupt, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	opened := make(chan struct{})
	go func() {
		select {
		case <-sigs:
			cancel()
		case <-opened:
		}
	}()
	body, att, err := at.open(streamCtx, target, req)
	close(opened)
	if err != nil {
		if streamCtx.Err() != nil {
			return errors.New("attach: given up; the agent carries on")
		}
		return err
	}
	at.agent = att.Agent
	at.show(att)
	tui, err := at.startTUI(ctx, withTerminalEnv(att.Command, os.Environ()))
	if err != nil || att.Released {
		_ = body.Close()
	}
	if err != nil {
		if !att.Released {
			at.handBack(ctx)
		}
		return err
	}
	if att.Released {
		// Nothing to hand back: just stay in the foreground until it quits.
		for {
			select {
			case s := <-sigs:
				tui.forward(s)
			case <-tui.exited:
				return nil
			}
		}
	}
	at.pid = tui.cmd.Process.Pid
	at.launched(ctx)
	frames := make(chan api.AttachFrame)
	go at.follow(streamCtx, body, frames)
	if at.supervise(tui, sigs, frames) {
		at.ending.Store(true)
		at.handBack(ctx)
	}
	return nil
}

// supervise waits for the TUI to exit, ending it when the daemon detaches
// it or reports the attach over. It reports whether the agent still needs
// handing back.
func (at *attachRun) supervise(tui *tuiProcess, sigs <-chan os.Signal, frames <-chan api.AttachFrame) bool {
	handBack := true
	for {
		select {
		case s := <-sigs:
			tui.forward(s)
		case f, ok := <-frames:
			if !ok {
				frames = nil
				continue
			}
			switch f.Type {
			case api.FrameDetach:
				at.say("\r\nslop-bot: detached from another terminal; ending the TUI.")
				tui.end()
			case api.FrameEnded:
				at.say("\r\nslop-bot: the agent was handed back without this terminal (%s); ending the TUI.", f.Reason)
				handBack = false
				tui.end()
			}
		case <-tui.exited:
			tui.stopKill()
			return handBack
		}
	}
}

// tuiProcess is the harness's TUI, running in the person's terminal.
type tuiProcess struct {
	cmd    *exec.Cmd
	exited chan struct{}
	kill   *time.Timer
}

func (at *attachRun) startTUI(ctx context.Context, c *api.Command) (*tuiProcess, error) {
	if c == nil {
		return nil, errors.New("attach: the daemon gave no TUI to run")
	}
	cmd := exec.CommandContext(ctx, c.Path, c.Args...) // #nosec G204 -- the daemon's own harness command, for the user's own terminal
	cmd.Env, cmd.Dir = c.Env, c.Dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = at.stdin, at.stdout, at.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("attach: starting the TUI: %w", err)
	}
	t := &tuiProcess{cmd: cmd, exited: make(chan struct{})}
	go func() {
		// Its exit status is the person's business, not the attach's.
		_ = cmd.Wait()
		close(t.exited)
	}()
	return t, nil
}

// forward passes on the signals that would end this process, so the TUI
// ends with it; the terminal sends Ctrl-C and the like to the TUI itself.
func (t *tuiProcess) forward(s os.Signal) {
	if s == syscall.SIGTERM || s == syscall.SIGHUP {
		_ = t.cmd.Process.Signal(s)
	}
}

// end asks the TUI to quit, and kills it if it hasn't after a grace.
func (t *tuiProcess) end() {
	_ = t.cmd.Process.Signal(syscall.SIGTERM)
	if t.kill == nil {
		t.kill = time.AfterFunc(tuiTermGrace, func() { _ = t.cmd.Process.Kill() })
	}
}

func (t *tuiProcess) stopKill() {
	if t.kill != nil {
		t.kill.Stop()
	}
}

// open starts the attach stream and reads its first frame.
func (at *attachRun) open(ctx context.Context, target string, req *api.AttachRequest) (*scannedBody, *api.Attachment, error) {
	body, err := at.client.StreamRequest(ctx, http.MethodPost, agentPath(api.PathAttach, target), req)
	if err != nil {
		return nil, nil, err
	}
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	var f api.AttachFrame
	if !sc.Scan() || json.Unmarshal(sc.Bytes(), &f) != nil || f.Type != api.FrameAttached || f.Attach == nil {
		_ = body.Close()
		return nil, nil, fmt.Errorf("attach: the daemon's answer wasn't an attach: %v", sc.Err())
	}
	return &scannedBody{sc: sc, Closer: body}, f.Attach, nil
}

// scannedBody is a stream whose first line has been read: the rest is read
// through the same scanner, so nothing it buffered is lost.
type scannedBody struct {
	sc *bufio.Scanner
	io.Closer
}

// follow passes the stream's frames on. When the daemon goes away (a
// restart), it reconnects as the same holder until it is back, or until
// the reconnect is refused because the agent was handed back meanwhile.
func (at *attachRun) follow(ctx context.Context, body *scannedBody, out chan<- api.AttachFrame) {
	defer close(out)
	for {
		if !at.relay(ctx, body, out) || ctx.Err() != nil || at.ending.Load() {
			return
		}
		at.say("\r\nslop-bot: lost the daemon; reconnecting (your TUI keeps running)...")
		var err error
		body, err = at.reconnect(ctx)
		if err != nil {
			var refused *control.APIError
			if errors.As(err, &refused) {
				select {
				case out <- api.AttachFrame{Type: api.FrameEnded, Reason: refused.Message}:
				case <-ctx.Done():
				}
			}
			return
		}
		at.say("\r\nslop-bot: reconnected.")
		at.launched(ctx)
	}
}

// relay passes one stream's frames on until it ends, then closes it. It
// reports false when ctx ended first.
func (at *attachRun) relay(ctx context.Context, body *scannedBody, out chan<- api.AttachFrame) bool {
	defer func() { _ = body.Close() }()
	for body.sc.Scan() {
		var f api.AttachFrame
		if json.Unmarshal(body.sc.Bytes(), &f) != nil {
			continue
		}
		if f.Type == api.FrameEnded {
			at.ending.Store(true)
		}
		select {
		case out <- f:
		case <-ctx.Done():
			return false
		}
	}
	return true
}

// reconnect retries the attach as this holder until the daemon answers.
// An answer refusing it is returned as the *control.APIError it is.
func (at *attachRun) reconnect(ctx context.Context) (*scannedBody, error) {
	wait := retryEvery
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		body, _, err := at.open(ctx, at.agent, &api.AttachRequest{Holder: at.holder, Reattach: true})
		if err == nil || refusal(err) {
			return body, err
		}
		wait = min(2*wait, retryMax)
	}
}

// refusal reports whether err is the daemon refusing a request, as against
// failing it: a 5xx (a busy database, say) is worth retrying, and taking it
// for a refusal would end the person's TUI.
func refusal(err error) bool {
	var apiErr *control.APIError
	return errors.As(err, &apiErr) && apiErr.Status < http.StatusInternalServerError
}

// handBack tells the daemon the TUI has exited, retrying through a daemon
// restart for a while; past that the lease expiring hands the agent back.
func (at *attachRun) handBack(ctx context.Context) {
	deadline := time.Now().Add(handBackFor)
	wait := retryEvery
	for {
		var out api.HandedBack
		err := at.client.Do(ctx, http.MethodPost, agentPath(api.PathHandBack, at.agent), &api.HandBack{Holder: at.holder}, &out)
		if err == nil {
			at.say("Handed agent %s back to slop-bot: %s.", at.agent, out.Outcome)
			return
		}
		if refusal(err) || time.Now().After(deadline) {
			at.say("slop-bot: handing agent %s back: %v. Its lease expiring will hand it back.", at.agent, err)
			return
		}
		time.Sleep(wait)
		wait = min(2*wait, retryMax)
	}
}

func (at *attachRun) show(att *api.Attachment) {
	var b strings.Builder
	fmt.Fprintf(&b, "Agent %s, ticket %s: %q\n", att.Agent, att.Ticket, att.Title)
	fmt.Fprintf(&b, "  state: %s\n", att.State)
	if att.Note != "" {
		fmt.Fprintf(&b, "  note: %s\n", att.Note)
	}
	if len(att.Held) > 0 {
		b.WriteString("  held messages (delivered when you hand it back):\n")
		for _, m := range att.Held {
			fmt.Fprintf(&b, "    from %s (%s): %s\n", m.From, m.Kind, m.Body)
		}
	}
	if att.Released {
		fmt.Fprintf(&b, "Released: slop-bot no longer runs this agent, and ticket %s stays assigned to you.\n", att.Ticket)
		b.WriteString("The agent's own slop-bot commands (ask, done, ...) no longer work from this TUI.\n")
	} else if att.Join {
		b.WriteString("Joining the agent's running session. Detaching from it (Ctrl-b d) hands the agent back to slop-bot;\n" +
			"quitting the harness ends the session, and slop-bot resumes the conversation once you're out.\n")
	} else {
		b.WriteString("Quitting the TUI hands the agent back to slop-bot.\n")
	}
	_, _ = io.WriteString(at.stdout, strings.ReplaceAll(b.String(), "\n", "\r\n"))
}

func (at *attachRun) say(format string, args ...any) {
	_, _ = fmt.Fprintf(at.stderr, format+"\r\n", args...)
}

// terminalVars are the attaching terminal's own: the daemon's environment
// (under systemd or launchd) has none of them, or another terminal's.
var terminalVars = []string{"TERM", "COLORTERM", "TERM_PROGRAM", "TERM_PROGRAM_VERSION", "LANG"}

// withTerminalEnv is c with this terminal's variables from local laid over
// the daemon's.
func withTerminalEnv(c *api.Command, local []string) *api.Command {
	if c == nil {
		return nil
	}
	out := *c
	mine := map[string]string{}
	for _, kv := range local {
		k, _, _ := strings.Cut(kv, "=")
		if slices.Contains(terminalVars, k) || strings.HasPrefix(k, "LC_") {
			mine[k] = kv
		}
	}
	out.Env = make([]string, 0, len(c.Env)+len(mine))
	for _, kv := range c.Env {
		if k, _, _ := strings.Cut(kv, "="); mine[k] == "" {
			out.Env = append(out.Env, kv)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(mine)) {
		out.Env = append(out.Env, mine[k])
	}
	return &out
}
