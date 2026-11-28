// Package sessionhost hosts the terminal sessions TUI drivers run a harness
// in. Sessions outlive the daemon, so a restarted daemon lists and adopts
// them instead of starting over.
package sessionhost

import (
	"context"
	"errors"
)

// SessionHost runs named terminal sessions.
type SessionHost interface {
	// Start runs spec in a new detached session.
	Start(ctx context.Context, spec *StartSpec) error
	// List names every session, live or dead.
	List(ctx context.Context) ([]string, error)
	// SendKeys presses keys, by key name (e.g. "Enter", "Escape", "C-c").
	SendKeys(ctx context.Context, session string, keys ...string) error
	// SendText types text literally, key by key.
	SendText(ctx context.Context, session, text string) error
	// Paste delivers text as one bracketed paste, so a multi-line text
	// lands in the prompt without its newlines submitting it early.
	Paste(ctx context.Context, session, text string) error
	// Capture is the visible screen, wrapped lines joined.
	Capture(ctx context.Context, session string) (string, error)
	// Status is the session's pane state.
	Status(ctx context.Context, session string) (*Status, error)
	// WaitExit returns once the session's process has died or the session
	// has been killed. It may return early; callers check Status.
	WaitExit(ctx context.Context, session string) error
	// SetTag and Tag keep a value with the session, so a restarted daemon
	// can read back what it knew (the conversation id).
	SetTag(ctx context.Context, session, name, value string) error
	Tag(ctx context.Context, session, name string) (string, error)
	// Kill ends the session and its process.
	Kill(ctx context.Context, session string) error
	// AttachCommand is the argv that joins a person's terminal to session.
	AttachCommand(session string) []string
}

// StartSpec is a session to start.
type StartSpec struct {
	Name string
	Argv []string
	// Env is the process's complete environment, as KEY=VALUE.
	Env []string
	Dir string
}

// Status is a session's pane state.
type Status struct {
	// Dead means the process has exited and the pane is kept
	// (remain-on-exit) so its status can be read.
	Dead bool
	// ExitStatus is the exit status of a dead pane: 128+n for death by
	// signal n.
	ExitStatus int
	// Attached is the number of people's terminals joined to the session.
	Attached int
}

// ErrNoSession means the session doesn't exist.
var ErrNoSession = errors.New("sessionhost: no such session")
