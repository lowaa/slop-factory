package doctor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
}

// LookPath is exec.LookPath.
func (ExecCommands) LookPath(file string) (string, error) { return exec.LookPath(file) }

// waitDelay is how long Wait waits for a killed probe's pipes to close: a
// harness's own children can hold them open after it dies.
const waitDelay = 2 * time.Second

// Run runs c, killing it at the timeout.
func (e ExecCommands) Run(ctx context.Context, c *Command) (*service.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, e.Timeout)
	defer cancel()
	// #nosec G204 -- the path is a harness binary found on the user's PATH, the args are doctor's own
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	inOwnGroup(cmd)
	cmd.WaitDelay = waitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stderr = &stderr
	if c.Done == nil {
		cmd.Stdin = strings.NewReader(c.Input)
		cmd.Stdout = &stdout
		return result(ctx, c, cmd.Run(), stdout.String(), stderr.String(), false)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("doctor: running %s: %w", c.Path, err)
	}
	// A write error means it already exited; the scan then ends at EOF.
	_, _ = io.WriteString(stdin, c.Input)
	done := false
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		stdout.Write(sc.Bytes())
		stdout.WriteByte('\n')
		if done = c.Done(stdout.String()); done {
			break
		}
	}
	// Closing stdin ends a well-behaved server; cancelling kills one that
	// isn't. Wait reaps it on every path.
	_ = stdin.Close()
	if done {
		cancel()
	}
	return result(ctx, c, cmd.Wait(), stdout.String(), stderr.String(), done)
}

func result(ctx context.Context, c *Command, err error, stdout, stderr string, answered bool) (*service.Result, error) {
	res := &service.Result{Stdout: stdout, Stderr: stderr}
	if answered {
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
