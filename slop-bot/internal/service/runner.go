package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// Result is what a command printed and how it exited.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Runner runs the service manager's commands (systemctl, loginctl,
// launchctl). Tests substitute a fake, so nothing touches the real user's
// service manager.
type Runner interface {
	// Run runs name with args. A non-zero exit is a Result, not an error:
	// `systemctl is-active` reports state through its exit code. The error
	// is for a command that couldn't run at all.
	Run(ctx context.Context, name string, args ...string) (*Result, error)
}

// ExecRunner runs commands for real.
type ExecRunner struct{}

// Run runs the command and captures its output.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) (*Result, error) {
	// #nosec G204 -- name is one of systemctl, loginctl or launchctl, and the args are built by this package
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	// A command killed by cancellation exits -1; that's no answer, so it
	// mustn't read as a state.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("service: running %s: %w", name, ctxErr)
	}
	res := &Result{Stdout: stdout.String(), Stderr: stderr.String()}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	if err != nil {
		return nil, fmt.Errorf("service: running %s: %w", name, err)
	}
	return res, nil
}

// commander logs each command and turns a failed exit into an error, for
// the calls where failure is failure.
type commander struct {
	runner Runner
	logger *slog.Logger
}

func (c *commander) run(ctx context.Context, name string, args ...string) (*Result, error) {
	c.logger.Debug("service command", "cmd", name, "args", args)
	return c.runner.Run(ctx, name, args...)
}

func (c *commander) must(ctx context.Context, name string, args ...string) (*Result, error) {
	res, err := c.run(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("service: %s %s exited %d: %s", name, strings.Join(args, " "),
			res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return res, nil
}
