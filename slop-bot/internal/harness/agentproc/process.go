// Package agentproc runs a harness's programmatic process: a child in its own
// process group, with its stdin and stdout as pipes, its stderr kept for
// diagnosis, and its environment cleaned (AgentEnv).
package agentproc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
)

// stderrKeep is how much of a process's stderr is kept: enough for the last
// error a harness printed before dying.
const stderrKeep = 64 << 10

// stderrDrain bounds how long an exit waits for stderr to drain once the
// harness itself has gone. Normally it drains at once; a child the harness
// left behind (a backgrounded shell, an MCP server) can hold stderr open
// indefinitely, and without a bound that would keep the exit from ever
// being reported.
const stderrDrain = 5 * time.Second

// Process is a running harness process.
type Process struct {
	cmd    *exec.Cmd
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	stderr *tail
	done   chan struct{}
	exit   exitResult
}

type exitResult struct {
	code int
	err  error
}

// Start runs spec. The process outlives ctx only until Kill or its own exit:
// ctx governs starting, not the process's life, which the conversation owns.
func Start(ctx context.Context, spec *harness.LaunchSpec) (*Process, error) {
	return start(ctx, spec, stderrDrain)
}

func start(_ context.Context, spec *harness.LaunchSpec, drain time.Duration) (*Process, error) {
	if spec == nil || spec.Path == "" {
		return nil, errors.New("agentproc: no command")
	}
	cmd := exec.Command(spec.Path, spec.Args...) // #nosec G204 -- the harness binary and its flags come from slop-bot's own adapters
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	// Its own group, so Kill reaches the harness's children (shells, MCP
	// servers) too, and a terminal's Ctrl-C to the daemon doesn't.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	// Not cmd.StdoutPipe: Wait closes that pipe's read end, so output the
	// harness wrote just before exiting would be lost to a reader that
	// hasn't got to it yet. With our own pipe the reader drains it to EOF
	// whenever the process exits.
	stdout, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	cmd.Stdout = stdoutW
	cmd.WaitDelay = drain
	p := &Process{cmd: cmd, Stdin: stdin, Stdout: stdout, stderr: &tail{max: stderrKeep}, done: make(chan struct{})}
	cmd.Stderr = p.stderr
	err = cmd.Start()
	// The child has its own copy of the write end; ours must go, or the
	// reader never sees EOF.
	_ = stdoutW.Close()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("agentproc: starting %s: %w", spec.Path, err)
	}
	go p.wait()
	return p, nil
}

func (p *Process) wait() {
	err := p.cmd.Wait()
	// A stderr still held open past the drain bound is not the harness
	// failing: its exit status is what's reported.
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
		err = nil
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			code = 128 + int(ws.Signal())
		}
	}
	p.exit = exitResult{code: code, err: err}
	close(p.done)
}

// Done is closed once the process has exited.
func (p *Process) Done() <-chan struct{} { return p.done }

// ExitCode is the exit status, valid after Done: 128+n for death by signal n.
func (p *Process) ExitCode() int { <-p.done; return p.exit.code }

// Err is a failure to wait on the process, not a non-zero exit.
func (p *Process) Err() error { <-p.done; return p.exit.err }

// Stderr is the tail of what the process wrote to stderr.
func (p *Process) Stderr() string { return p.stderr.String() }

// PID is the process id, which is also its process group id.
func (p *Process) PID() int { return p.cmd.Process.Pid }

// Signal sends sig to the whole process group.
func (p *Process) Signal(sig syscall.Signal) error {
	select {
	case <-p.done:
		return nil
	default:
	}
	return syscall.Kill(-p.cmd.Process.Pid, sig)
}

// Kill ends the whole process group and waits for the exit.
func (p *Process) Kill() {
	_ = p.Signal(syscall.SIGKILL)
	<-p.done
}

// tail is an io.Writer that keeps the last max bytes written.
type tail struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tail) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(b), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
