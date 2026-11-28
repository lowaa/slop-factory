package doctor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/lowaa/slop-factory/slop-bot/internal/service"
)

// Command is one probe: a harness's --version, its sign-in status, its
// model list.
type Command struct {
	// Path is the executable, as LookPath found it.
	Path string
	Args []string
	// Env, when set, is the probe's whole environment: the one the
	// harness's agents would get, so an API key they don't get can't make a
	// sign-in check pass. Nil inherits doctor's own.
	Env []string
	// Input is written to stdin.
	Input string
	// Done, when set, keeps stdin open until it reports the output holds
	// the answer, and then ends the command. Codex's app-server exits at
	// EOF on stdin before it answers, so closing stdin after Input would
	// lose the reply.
	Done func(stdout string) bool
}

// Commands finds and runs the probes. Tests substitute a fake, so no check
// runs a real harness.
type Commands interface {
	LookPath(file string) (string, error)
	// Run runs c. A non-zero exit is a Result: `codex login status`
	// answers through it. The error is for a command that couldn't run,
	// or ran past its time.
	Run(ctx context.Context, c *Command) (*service.Result, error)
}

// ExecCommands runs probes for real.
type ExecCommands struct {
	// Timeout bounds each probe, so a harness that hangs (waiting on a
	// network, or a prompt) can't hang doctor.
	Timeout time.Duration
	// WaitDelay is how long a child a probe left behind may hold its
	// output open after the probe exits, before it is ended with the
	// probe's group. Zero means defaultWaitDelay.
	WaitDelay time.Duration
	// GroupDelay is how long, once the group has been ended, its output may
	// stay open before doctor stops reading it: only a child that left the
	// group can hold it that long. Zero means defaultGroupDelay.
	GroupDelay time.Duration
}

// LookPath is exec.LookPath.
func (ExecCommands) LookPath(file string) (string, error) { return exec.LookPath(file) }

const (
	defaultWaitDelay  = 2 * time.Second
	defaultGroupDelay = 10 * time.Second
)

// Run runs c, killing it at the timeout.
//
// Its output is read from pipes doctor owns, not through exec.Cmd's
// copying: that is cut off at WaitDelay, and what the probe wrote but the
// copy hadn't yet read (under load, all of it) would be lost. Read here, it
// is kept until the pipes close, which ending the group makes certain.
func (e ExecCommands) Run(ctx context.Context, c *Command) (*service.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, e.Timeout)
	defer cancel()
	// #nosec G204 -- the path is a harness binary found on the user's PATH, the args are doctor's own
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	inOwnGroup(cmd)
	cmd.Env = c.Env
	// Still exec's own bound on its stdin copy, which a child can block.
	cmd.WaitDelay = or(e.WaitDelay, defaultWaitDelay)
	stdout, err := newCapture()
	if err != nil {
		return nil, err
	}
	defer stdout.close()
	stderr, err := newCapture()
	if err != nil {
		return nil, err
	}
	defer stderr.close()
	cmd.Stdout, cmd.Stderr = stdout.w, stderr.w
	var stdin io.WriteCloser
	if c.Done == nil {
		cmd.Stdin = strings.NewReader(c.Input)
	} else if stdin, err = cmd.StdinPipe(); err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("doctor: running %s: %w", c.Path, err)
	}
	stdout.started()
	stderr.started()
	stderr.drain()
	if c.Done == nil {
		stdout.drain()
		werr := cmd.Wait()
		e.settle(cmd, stdout, stderr)
		return result(ctx, c, &outcome{err: werr, stdout: stdout.buf.String(), stderr: stderr.buf.String()})
	}
	// A write error means it already exited; the scan then ends at EOF.
	_, _ = io.WriteString(stdin, c.Input)
	var out bytes.Buffer
	done := false
	sc := bufio.NewScanner(stdout.r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		out.Write(sc.Bytes())
		out.WriteByte('\n')
		if done = c.Done(out.String()); done {
			break
		}
	}
	// Closing stdin ends a well-behaved server; cancelling kills one that
	// isn't. Wait reaps it on every path.
	_ = stdin.Close()
	if done {
		cancel()
	}
	werr := cmd.Wait()
	e.settle(cmd, stderr)
	return result(ctx, c, &outcome{err: werr, stdout: out.String(), stderr: stderr.buf.String(), answered: done})
}

// settle waits, once the probe has exited, for its outputs to close. A
// child still holding them gets WaitDelay, and is then ended with the
// probe's group; what it wrote is kept, because the pipes close only once
// the group has gone. A child that left the group can hold them past even
// that, so after GroupDelay they are closed, and only its unread output
// is lost.
func (e ExecCommands) settle(cmd *exec.Cmd, caps ...*capture) {
	if drained(or(e.WaitDelay, defaultWaitDelay), caps) {
		return
	}
	endGroup(cmd)
	if drained(or(e.GroupDelay, defaultGroupDelay), caps) {
		return
	}
	for _, c := range caps {
		_ = c.r.Close()
		<-c.done
	}
}

// drained says whether every capture reached its end within d.
func drained(d time.Duration, caps []*capture) bool {
	bound := time.NewTimer(d)
	defer bound.Stop()
	for _, c := range caps {
		select {
		case <-c.done:
		case <-bound.C:
			return false
		}
	}
	return true
}

func or(d, def time.Duration) time.Duration {
	if d == 0 {
		return def
	}
	return d
}

// capture is one of a probe's output streams, on a pipe of doctor's own.
type capture struct {
	r, w *os.File
	buf  bytes.Buffer
	// done closes once drain has read to the end; buf is then complete.
	done chan struct{}
}

func newCapture() (*capture, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("doctor: making a pipe: %w", err)
	}
	return &capture{r: r, w: w, done: make(chan struct{})}, nil
}

// started drops doctor's copy of the write end, which the probe now has:
// the pipe then ends when the probe and its children have closed it.
func (c *capture) started() { _ = c.w.Close() }

func (c *capture) drain() {
	go func() {
		defer close(c.done)
		_, _ = io.Copy(&c.buf, c.r)
	}()
}

func (c *capture) close() {
	_ = c.r.Close()
	_ = c.w.Close()
}

// outcome is how a probe ended: Run's or Wait's error, what it printed,
// and whether Done saw the answer before it was ended.
type outcome struct {
	err            error
	stdout, stderr string
	answered       bool
}

func result(ctx context.Context, c *Command, o *outcome) (*service.Result, error) {
	res := &service.Result{Stdout: o.stdout, Stderr: o.stderr}
	err := o.err
	if errors.Is(err, exec.ErrWaitDelay) {
		// It exited 0, but left a child holding its stdin: settle has
		// ended that child with its group, and the exit counts.
		err = nil
	}
	if o.answered {
		// Killed after it answered: the answer is what counts.
		return res, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("doctor: %s %s: %w", c.Path, strings.Join(c.Args, " "), ctxErr)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	if err != nil {
		return nil, fmt.Errorf("doctor: running %s: %w", c.Path, err)
	}
	return res, nil
}
