package scripted

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// LongCommand is a shell command for a scripted model to run that lasts
// until the test releases it: it reads a FIFO, so it blocks until the test
// writes a marker there, and the marker in a later request's tool results
// says which request delivered its result. A real tool call's length is
// then the test's to decide, with no sleep on either side.
type LongCommand struct {
	fifo   string
	marker string
}

// NewLongCommand makes the n'th long command's FIFO in dir. Each n has its
// own FIFO and marker.
func NewLongCommand(dir string, n int) (*LongCommand, error) {
	l := &LongCommand{fifo: filepath.Join(dir, fmt.Sprintf("long-%d.fifo", n)), marker: fmt.Sprintf("long-tool-%d-released", n)}
	if err := unix.Mkfifo(l.fifo, 0o600); err != nil {
		return nil, fmt.Errorf("scripted: %w", err)
	}
	return l, nil
}

// Command is the shell command to script.
func (l *LongCommand) Command() string { return "cat " + shellQuote(l.fifo) }

// Marker is what the command prints once released: its tool result.
func (l *LongCommand) Marker() string { return l.marker }

// Release writes the marker to the FIFO. Opening it for writing waits for
// the command to open it for reading, so it is also the point at which the
// tool is known to be running.
func (l *LongCommand) Release(ctx context.Context) error {
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

// Unblock ends a command still waiting on the FIFO, so a test that never
// released it (an interrupt, a forced stop) leaves no process behind, should
// the harness have left it running. With no reader there is nothing to do.
func (l *LongCommand) Unblock() {
	f, err := os.OpenFile(l.fifo, os.O_WRONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return
	}
	_, _ = f.WriteString("cleanup\n")
	_ = f.Close()
}

// shellQuote is s as one POSIX shell word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
