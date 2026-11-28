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
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
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
}

func (at *attachRun) run(ctx context.Context, target string, req *api.AttachRequest) error {
	req.Holder = at.holder
	if !req.Now {
		at.say("Waiting for the agent's turn to end (--now interrupts it)...")
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	body, att, err := at.open(streamCtx, target, req)
	if err != nil {
		return err
	}
	at.agent = att.Agent
	at.show(att)
	if att.Command == nil {
		_ = body.Close()
		return errors.New("attach: the daemon gave no TUI to run")
	}
	child := exec.CommandContext(ctx, att.Command.Path, att.Command.Args...) // #nosec G204 -- the daemon's own harness command, for the user's own terminal
	child.Env, child.Dir = att.Command.Env, att.Command.Dir
	child.Stdin, child.Stdout, child.Stderr = at.stdin, at.stdout, at.stderr
	// The TUI owns the terminal's signals: Ctrl-C and Ctrl-\ are its, and
	// this process only passes on what would otherwise end it.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, os.Interrupt, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	if err := child.Start(); err != nil {
		_ = body.Close()
		if !att.Released {
			at.handBack(ctx)
		}
		return fmt.Errorf("attach: starting the TUI: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()
	if att.Released {
		_ = body.Close()
		for {
			select {
			case s := <-sigs:
				forward(child, s)
			case <-exited:
				return nil
			}
		}
	}
	if err := at.client.Do(ctx, http.MethodPost, agentPath(api.PathLaunched, at.agent), &api.Launched{Holder: at.holder, PID: child.Process.Pid}, nil); err != nil {
		at.say("slop-bot: couldn't tell the daemon the TUI's pid: %v", err)
	}
	frames := make(chan api.AttachFrame)
	go at.follow(streamCtx, body, frames)
	lost := false
	var kill *time.Timer
	for {
		select {
		case s := <-sigs:
			forward(child, s)
		case f, ok := <-frames:
			if !ok {
				frames = nil
				continue
			}
			switch f.Type {
			case api.FrameDetach:
				at.say("\r\nslop-bot: detached from another terminal; ending the TUI.")
				kill = end(child)
			case api.FrameEnded:
				at.say("\r\nslop-bot: the agent was handed back without this terminal (%s); ending the TUI.", f.Reason)
				lost = true
				kill = end(child)
			}
		case <-exited:
			if kill != nil {
				kill.Stop()
			}
			if lost {
				return nil
			}
			at.handBack(ctx)
			return nil
		}
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
		sb := body
		for sb.sc.Scan() {
			var f api.AttachFrame
			if json.Unmarshal(sb.sc.Bytes(), &f) != nil {
				continue
			}
			select {
			case out <- f:
			case <-ctx.Done():
				_ = body.Close()
				return
			}
		}
		_ = body.Close()
		if ctx.Err() != nil {
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
	}
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
		var refused *control.APIError
		if err == nil || errors.As(err, &refused) {
			return body, err
		}
		wait = min(2*wait, retryMax)
	}
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
		var refused *control.APIError
		if errors.As(err, &refused) || time.Now().After(deadline) {
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
	} else {
		b.WriteString("Quitting the TUI hands the agent back to slop-bot.\n")
	}
	_, _ = io.WriteString(at.stdout, strings.ReplaceAll(b.String(), "\n", "\r\n"))
}

func (at *attachRun) say(format string, args ...any) {
	_, _ = fmt.Fprintf(at.stderr, format+"\r\n", args...)
}

// forward passes on the signals that would end this process, so the TUI
// ends with it; the terminal sends Ctrl-C and the like to the TUI itself.
func forward(child *exec.Cmd, s os.Signal) {
	if s == syscall.SIGTERM || s == syscall.SIGHUP {
		_ = child.Process.Signal(s)
	}
}

// end asks the TUI to quit, and kills it if it hasn't after a grace.
func end(child *exec.Cmd) *time.Timer {
	_ = child.Process.Signal(syscall.SIGTERM)
	return time.AfterFunc(tuiTermGrace, func() { _ = child.Process.Kill() })
}
