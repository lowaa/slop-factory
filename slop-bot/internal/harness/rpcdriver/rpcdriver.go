// Package rpcdriver is the shared kit behind every programmatic harness
// driver (claude-code/stream-json, codex/app-server, pi/rpc): it runs the
// process through agentproc, reads its stdout as newline-delimited messages,
// and maps the harness's own protocol, through a per-harness Codec, onto
// harness.Conversation.
package rpcdriver

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"syscall"
	"time"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/agentproc"
)

// maxLine bounds one protocol message. Harnesses put whole tool outputs in a
// line, so the default 64KiB scanner buffer is far too small.
const maxLine = 32 << 20

// Wire is the codec's handle on the process: writes are serialised, so a
// steer and a protocol reply can't interleave mid-line.
type Wire struct {
	mu sync.Mutex
	w  io.Writer
}

// NewWire wraps w. Codec tests use it to capture what a codec writes.
func NewWire(w io.Writer) *Wire { return &Wire{w: w} }

// WriteLine writes b and a newline as one message.
func (w *Wire) WriteLine(b []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.w.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("rpcdriver: writing to the harness: %w", err)
	}
	return nil
}

// Codec is one harness's protocol.
type Codec interface {
	// Open runs the handshake, after the process starts and before anything
	// is sent. It returns the conversation's id. Lines the handshake waits
	// for arrive through Decode; Open may block on them via the next channel.
	Open(ctx context.Context, w *Wire, next func(context.Context) ([]byte, error)) (string, error)
	// Send encodes a user message. midTurn says a turn is running, for
	// protocols that steer differently from starting a turn.
	Send(ctx context.Context, w *Wire, msg *harness.Message, midTurn bool) error
	// Interrupt asks the harness to stop the running turn.
	Interrupt(ctx context.Context, w *Wire) error
	// Decode turns one stdout line into events. It may write replies (an
	// approval, a ping) through w.
	Decode(line []byte, w *Wire) ([]harness.Event, error)
}

// DefaultKillGrace is Options.KillGrace's default.
const DefaultKillGrace = 5 * time.Second

// Options configure one conversation.
type Options struct {
	Harness, Driver string
	Codec           Codec
	Spec            *harness.LaunchSpec
	Logger          *slog.Logger
	Clock           clock.Clock
	// KillGrace is how long a SIGTERM gets before SIGKILL; zero means
	// DefaultKillGrace.
	KillGrace time.Duration
	// Attach is the hand-over plan's TUI command, given the conversation id.
	Attach func(id string) *harness.LaunchSpec
}

// Conversation is a programmatic session.
type Conversation struct {
	opts   *Options
	proc   *agentproc.Process
	wire   *Wire
	id     string
	events chan harness.Event

	mu sync.Mutex
	// inTurn is the turn state as the harness last reported it.
	inTurn bool
	// settled is closed and replaced whenever a turn ends, so Stop can wait
	// for "not in a turn" without polling.
	settled chan struct{}
	// lines carries every stdout line, in order. Open reads the handshake
	// from it; dispatch reads the rest, so no line can fall between them.
	lines  chan []byte
	exited chan struct{}
}

// Start launches the process and runs the codec's handshake.
func Start(ctx context.Context, opts *Options) (*Conversation, error) {
	proc, err := agentproc.Start(ctx, opts.Spec)
	if err != nil {
		return nil, err
	}
	c := &Conversation{
		opts:    opts,
		proc:    proc,
		wire:    &Wire{w: proc.Stdin},
		events:  make(chan harness.Event, 64),
		settled: make(chan struct{}),
		lines:   make(chan []byte, 64),
		exited:  make(chan struct{}),
	}
	close(c.settled) // not in a turn yet
	go c.read()
	id, err := opts.Codec.Open(ctx, c.wire, c.nextLine)
	if err != nil {
		proc.Kill()
		for range c.lines { // let read finish
		}
		return nil, fmt.Errorf("rpcdriver: %s handshake: %w (stderr: %s)", opts.Harness, err, lastLine(proc.Stderr()))
	}
	c.mu.Lock()
	c.id = id
	c.mu.Unlock()
	c.emit(harness.Event{Kind: harness.Ready})
	go c.dispatch()
	return c, nil
}

func (c *Conversation) nextLine(ctx context.Context) ([]byte, error) {
	select {
	case line, ok := <-c.lines:
		if !ok {
			return nil, errors.New("the harness exited during the handshake")
		}
		return line, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// read owns stdout, passing each line on, until EOF. A line over maxLine is
// dropped and reading goes on: stopping would leave the harness running
// with nobody reading its turn end or its exit, and the oversized lines seen
// in practice are summaries (pi's agent_end repeats the whole run) that no
// codec needs.
func (c *Conversation) read() {
	defer func() { _ = c.proc.Stdout.Close() }()
	r := bufio.NewReaderSize(c.proc.Stdout, 64<<10)
	for {
		line, err := readLine(r, maxLine)
		if errors.Is(err, errLineTooLong) {
			c.opts.Logger.Warn("rpcdriver: dropped an oversized line", "harness", c.opts.Harness, "max", maxLine)
			continue
		}
		if len(line) > 0 {
			c.lines <- line
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				c.opts.Logger.Warn("rpcdriver: reading the harness", "harness", c.opts.Harness, "err", err)
			}
			break
		}
	}
	close(c.lines)
}

var errLineTooLong = errors.New("rpcdriver: line too long")

// readLine returns the next line without its LF, split on LF only (a JSON
// string may hold U+2028, which some line readers split on). A line longer
// than max is consumed and errLineTooLong returned, so the next call starts
// at the following line. At the end it returns any last unterminated line
// with io.EOF.
func readLine(r *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	tooLong := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(chunk) > max+1 { // +1: the LF isn't part of the line
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case err != nil:
			if tooLong {
				return nil, errLineTooLong
			}
			return line, err
		case tooLong:
			return nil, errLineTooLong
		default:
			return line[:len(line)-1], nil
		}
	}
}

// dispatch decodes every line after the handshake, then reports the exit and
// closes Events.
func (c *Conversation) dispatch() {
	for line := range c.lines {
		evs, err := c.opts.Codec.Decode(line, c.wire)
		if err != nil {
			c.opts.Logger.Warn("rpcdriver: undecodable line", "harness", c.opts.Harness, "err", err)
			continue
		}
		for _, ev := range evs {
			c.emit(ev)
		}
	}
	code := c.proc.ExitCode()
	var exitErr error
	if code != 0 {
		exitErr = fmt.Errorf("%s exited with status %d: %s", c.opts.Harness, code, lastLine(c.proc.Stderr()))
	}
	c.setTurn(false)
	c.events <- harness.Event{Kind: harness.Exited, At: c.opts.Clock.Now(), ExitCode: code, Err: exitErr}
	close(c.events)
	close(c.exited)
}

// emit tracks turn state from the events it passes on. Sending blocks when
// the consumer falls behind: back-pressure onto the harness's stdout rather
// than dropped events.
func (c *Conversation) emit(ev harness.Event) {
	if ev.At.IsZero() {
		ev.At = c.opts.Clock.Now()
	}
	switch ev.Kind {
	case harness.TurnStarted:
		c.setTurn(true)
	case harness.TurnEnded:
		c.setTurn(false)
	}
	c.events <- ev
}

func (c *Conversation) setTurn(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if on && !c.inTurn {
		c.settled = make(chan struct{})
	}
	if !on && c.inTurn {
		close(c.settled)
	}
	c.inTurn = on
}

// Ref implements harness.Conversation.
func (c *Conversation) Ref() harness.ConversationRef {
	c.mu.Lock()
	defer c.mu.Unlock()
	return harness.ConversationRef{Harness: c.opts.Harness, Driver: c.opts.Driver, ID: c.id}
}

// Send implements harness.Conversation.
//
// A message sent while idle opens the turn here, before the harness reports
// it: otherwise a graceful Stop landing between the write and the harness's
// turn-start would see no turn, close stdin, and cut the turn off.
func (c *Conversation) Send(ctx context.Context, msg *harness.Message) error {
	c.mu.Lock()
	mid := c.inTurn
	c.mu.Unlock()
	if !mid {
		c.setTurn(true)
	}
	if err := c.opts.Codec.Send(ctx, c.wire, msg, mid); err != nil {
		if !mid {
			c.setTurn(false)
		}
		return err
	}
	return nil
}

// Interrupt implements harness.Conversation.
func (c *Conversation) Interrupt(ctx context.Context) error {
	return c.opts.Codec.Interrupt(ctx, c.wire)
}

// Events implements harness.Conversation.
func (c *Conversation) Events() <-chan harness.Event { return c.events }

// Stop implements harness.Conversation. Without force it waits for the turn
// to settle, then closes stdin, which every supported harness takes as "end
// the session"; with force, or when ctx ends first, it kills the group.
func (c *Conversation) Stop(ctx context.Context, force bool) error {
	if !force {
		c.mu.Lock()
		settled := c.settled
		c.mu.Unlock()
		select {
		case <-settled:
		case <-c.exited:
			return nil
		case <-ctx.Done():
			return c.kill()
		}
		_ = c.proc.Stdin.Close()
		select {
		case <-c.exited:
			return nil
		case <-ctx.Done():
			return c.kill()
		}
	}
	return c.kill()
}

// kill asks the group to stop, and only after KillGrace has passed with no
// exit does it force it, so the harness can flush its session state.
func (c *Conversation) kill() error {
	_ = c.proc.Signal(syscall.SIGTERM)
	grace := c.opts.KillGrace
	if grace == 0 {
		grace = DefaultKillGrace
	}
	select {
	case <-c.exited:
		return nil
	case <-clock.Or(c.opts.Clock).After(grace):
	}
	c.proc.Kill()
	<-c.exited
	return nil
}

// Exited is closed once the process has gone and Events has closed.
func (c *Conversation) Exited() <-chan struct{} { return c.exited }

// AttachPlan implements harness.Conversation: programmatic sessions are
// handed over, by stopping them and resuming the same id in the TUI.
func (c *Conversation) AttachPlan(context.Context) (*harness.AttachPlan, error) {
	if c.opts.Attach == nil {
		return nil, fmt.Errorf("rpcdriver: %s has no TUI to hand over to", c.opts.Harness)
	}
	return &harness.AttachPlan{Kind: harness.HandOver, Command: c.opts.Attach(c.Ref().ID)}, nil
}

func lastLine(s string) string {
	for len(s) > 0 && s[len(s)-1] == '\n' {
		s = s[:len(s)-1]
	}
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '\n' {
			return s[i+1:]
		}
	}
	return s
}
