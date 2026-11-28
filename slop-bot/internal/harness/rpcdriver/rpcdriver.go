// Package rpcdriver is the shared kit behind every programmatic harness
// driver (claude-code/stream-json, codex/app-server, pi/rpc): it runs the
// process through agentproc, reads its stdout as newline-delimited messages,
// and maps the harness's own protocol, through a per-harness Codec, onto
// harness.Conversation.
package rpcdriver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/driverkit"
)

// maxLine bounds one protocol message. Harnesses put whole tool outputs in a
// line, so the default 64KiB scanner buffer is far too small.
const maxLine = 32 << 20

// oversizedPrefix is how much of an oversized line is kept for the codec
// (Oversizer): enough for a record's type and the small fields JSON
// encoders put before a long one.
const oversizedPrefix = 64 << 10

// maxQueued bounds the lines waiting for the harness to read its stdin. A
// harness that leaves this many unread has stopped reading: a write past it
// fails rather than grow without end.
const maxQueued = 1024

var (
	errWireClosed = errors.New("rpcdriver: the harness's input is closed")
	errWireFull   = errors.New("rpcdriver: the harness isn't reading its input")
)

// Wire is the codec's handle on the process: writes are serialised, so a
// steer and a protocol reply can't interleave mid-line. A conversation's
// Wire queues each line for a writer goroutine of its own, so a write never
// waits on the harness reading: codecs write under their own locks, which
// Decode also takes, and a write blocked on a full stdin there would stop
// stdout being read, and a harness blocked writing stdout never reads
// stdin again.
type Wire struct {
	mu   sync.Mutex
	cond *sync.Cond
	w    io.Writer
	// c is closed once the queue is written, for a queued Wire; nil for
	// NewWire's, which writes in WriteLine.
	c      io.Closer
	queue  [][]byte
	closed bool
	err    error
}

// NewWire wraps w, writing in WriteLine itself. Codec tests use it to
// capture what a codec writes.
func NewWire(w io.Writer) *Wire { return &Wire{w: w} }

// newQueuedWire writes to wc from a goroutine of its own, until close or
// abort.
func newQueuedWire(wc io.WriteCloser) *Wire {
	w := &Wire{w: wc, c: wc}
	w.cond = sync.NewCond(&w.mu)
	go w.write()
	return w
}

// WriteLine queues b and a newline as one message, or for NewWire's,
// writes it.
func (w *Wire) WriteLine(b []byte) error {
	line := append(append(make([]byte, 0, len(b)+1), b...), '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case w.c == nil:
		if _, err := w.w.Write(line); err != nil {
			return fmt.Errorf("rpcdriver: writing to the harness: %w", err)
		}
		return nil
	case w.err != nil:
		return w.err
	case w.closed:
		return errWireClosed
	case len(w.queue) >= maxQueued:
		return errWireFull
	}
	w.queue = append(w.queue, line)
	w.cond.Signal()
	return nil
}

func (w *Wire) write() {
	for {
		w.mu.Lock()
		for len(w.queue) == 0 && !w.closed {
			w.cond.Wait()
		}
		if len(w.queue) == 0 {
			w.mu.Unlock()
			_ = w.c.Close()
			return
		}
		line := w.queue[0]
		w.queue = w.queue[1:]
		w.mu.Unlock()
		if _, err := w.w.Write(line); err != nil {
			w.mu.Lock()
			w.err = fmt.Errorf("rpcdriver: writing to the harness: %w", err)
			w.queue, w.closed = nil, true
			w.cond.Broadcast()
			w.mu.Unlock()
			_ = w.c.Close()
			return
		}
	}
}

// close closes stdin once what is queued has been written: how a graceful
// Stop ends the session.
func (w *Wire) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	w.cond.Signal()
}

// abort drops what is queued and closes stdin now, ending a write blocked
// on it: once the harness has exited nothing will read it, and a child it
// left holding the pipe might never.
func (w *Wire) abort() {
	w.mu.Lock()
	w.queue, w.closed = nil, true
	w.cond.Signal()
	w.mu.Unlock()
	_ = w.c.Close()
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

// Oversizer is a codec that can make something of a line too long to read
// whole: it is given the line's first bytes, which PrefixObject reads, so
// that a turn end carrying a huge result still ends the turn.
type Oversizer interface {
	Oversized(prefix []byte, w *Wire) ([]harness.Event, error)
}

// PrefixObject is the JSON object of the top-level fields that are whole in
// prefix, the start of a longer line (those before the one it cuts), or nil
// when there are none.
func PrefixObject(prefix []byte) []byte {
	fields := prefixFields(prefix)
	if len(fields) == 0 {
		return nil
	}
	b, err := json.Marshal(fields)
	if err != nil {
		return nil
	}
	return b
}

func prefixFields(prefix []byte) map[string]json.RawMessage {
	dec := json.NewDecoder(bytes.NewReader(prefix))
	fields := map[string]json.RawMessage{}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return fields
	}
	for dec.More() {
		tok, err := dec.Token()
		key, ok := tok.(string)
		if err != nil || !ok {
			break
		}
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			break
		}
		fields[key] = v
	}
	return fields
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
	// MaxLine bounds one stdout line; zero is 32MiB.
	MaxLine int
}

// Conversation is a programmatic session.
type Conversation struct {
	opts   *Options
	proc   *agentproc.Process
	wire   *Wire
	id     string
	events *driverkit.Events

	mu sync.Mutex
	// inTurn is the turn state as the harness last reported it.
	inTurn bool
	// settled is closed and replaced whenever a turn ends, so Stop can wait
	// for "not in a turn" without polling.
	settled chan struct{}
	// lines carries every stdout line, in order. Open reads the handshake
	// from it; dispatch reads the rest, so no line can fall between them.
	lines  chan stdoutLine
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
		wire:    newQueuedWire(proc.Stdin),
		events:  driverkit.NewEvents(opts.Logger),
		settled: make(chan struct{}),
		lines:   make(chan stdoutLine, 64),
		exited:  make(chan struct{}),
	}
	close(c.settled) // not in a turn yet
	go c.read()
	id, err := opts.Codec.Open(ctx, c.wire, c.nextLine)
	if err != nil {
		proc.Kill()
		for range c.lines { // let read finish
		}
		c.wire.abort()
		c.events.Close()
		return nil, fmt.Errorf("rpcdriver: %s handshake: %w (stderr: %s)", opts.Harness, err, lastLine(proc.Stderr()))
	}
	c.mu.Lock()
	c.id = id
	c.mu.Unlock()
	c.emit(harness.Event{Kind: harness.Ready})
	go c.dispatch()
	return c, nil
}

// stdoutLine is one line of stdout, or, oversized, the start of one.
type stdoutLine struct {
	b         []byte
	oversized bool
}

func (c *Conversation) nextLine(ctx context.Context) ([]byte, error) {
	for {
		select {
		case line, ok := <-c.lines:
			if !ok {
				return nil, errors.New("the harness exited during the handshake")
			}
			if line.oversized {
				c.opts.Logger.Warn("rpcdriver: dropped an oversized line in the handshake", "harness", c.opts.Harness)
				continue
			}
			return line.b, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// read owns stdout, passing each line on, until EOF. A line over MaxLine is
// passed on as its first bytes, for the codec to make what it can of
// (Oversizer), and reading goes on: stopping would leave the harness
// running with nobody reading its turn end or its exit.
func (c *Conversation) read() {
	defer func() { _ = c.proc.Stdout.Close() }()
	r := bufio.NewReaderSize(c.proc.Stdout, 64<<10)
	limit := c.opts.MaxLine
	if limit <= 0 {
		limit = maxLine
	}
	for {
		line, err := readLine(r, limit, min(oversizedPrefix, limit))
		if errors.Is(err, errLineTooLong) {
			c.lines <- stdoutLine{b: line, oversized: true}
			continue
		}
		if len(line) > 0 {
			c.lines <- stdoutLine{b: line}
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
// than max is consumed, its first keep bytes returned with errLineTooLong,
// so the next call starts at the following line. At the end it returns any
// last unterminated line with io.EOF.
func readLine(r *bufio.Reader, max, keep int) ([]byte, error) {
	var line []byte
	tooLong := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !tooLong {
			line = append(line, chunk...)
			if len(line) > max+1 { // +1: the LF isn't part of the line
				tooLong, line = true, line[:min(keep, len(line))]
			}
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case err != nil:
			if tooLong {
				return line, errLineTooLong
			}
			return line, err
		case tooLong:
			return line, errLineTooLong
		default:
			return line[:len(line)-1], nil
		}
	}
}

// dispatch decodes every line after the handshake, then reports the exit and
// closes Events.
func (c *Conversation) dispatch() {
	for line := range c.lines {
		if line.oversized {
			for _, ev := range c.oversized(line.b) {
				c.emit(ev)
			}
			continue
		}
		evs, err := c.opts.Codec.Decode(line.b, c.wire)
		if err != nil {
			c.opts.Logger.Warn("rpcdriver: undecodable line", "harness", c.opts.Harness, "err", err)
			continue
		}
		for _, ev := range evs {
			c.emit(ev)
		}
	}
	c.wire.abort()
	code := c.proc.ExitCode()
	var exitErr error
	if code != 0 {
		exitErr = fmt.Errorf("%s exited with status %d: %s", c.opts.Harness, code, lastLine(c.proc.Stderr()))
	}
	c.setTurn(false)
	c.events.Push(harness.Event{Kind: harness.Exited, At: c.opts.Clock.Now(), ExitCode: code, Err: exitErr})
	c.events.Close()
	close(c.exited)
}

// emit tracks turn state from the events it passes on. It never waits for
// the consumer (driverkit.Events): Decode answers the harness's requests on
// this goroutine, and the exit it reports is what a Stop waits for.
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
	c.events.Push(ev)
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

// oversized hands the start of a line too long to read whole to a codec
// that can use it.
func (c *Conversation) oversized(prefix []byte) []harness.Event {
	o, ok := c.opts.Codec.(Oversizer)
	if !ok {
		c.opts.Logger.Warn("rpcdriver: dropped an oversized line", "harness", c.opts.Harness)
		return nil
	}
	evs, err := o.Oversized(prefix, c.wire)
	if err != nil {
		c.opts.Logger.Warn("rpcdriver: an oversized line", "harness", c.opts.Harness, "err", err)
		return nil
	}
	return evs
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
func (c *Conversation) Events() <-chan harness.Event { return c.events.C() }

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
			return c.kill(ctx)
		}
		c.wire.close()
		select {
		case <-c.exited:
			return nil
		case <-ctx.Done():
			return c.kill(ctx)
		}
	}
	return c.kill(ctx)
}

// kill asks the group to stop, and only after KillGrace has passed with no
// exit, or ctx has ended, does it force it, so the harness can flush its
// session state. The wait after SIGKILL is bounded by agentproc, which
// reports the exit once stdout is drained or its drain bound passes; and
// by nothing else, since the events are queued whether or not anyone reads
// them.
func (c *Conversation) kill(ctx context.Context) error {
	_ = c.proc.Signal(syscall.SIGTERM)
	grace := c.opts.KillGrace
	if grace == 0 {
		grace = DefaultKillGrace
	}
	select {
	case <-c.exited:
		return nil
	case <-ctx.Done():
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
