// Package driverkit holds what the rpcdriver and tuidriver kits share in
// carrying out harness.Conversation's contract.
package driverkit

import (
	"log/slog"
	"sync"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
)

// backlogWarn is the backlog at which a consumer is reported as falling
// behind: far past what a harness produces while its consumer writes one
// event to the database, so it means a consumer that stopped reading.
const backlogWarn = 4096

// Events is a conversation's event stream: Push never blocks, and the
// consumer reads the events, in order, from C. A harness's protocol replies
// (an approval, a dialog's dismissal, a hook's answer) are made on the
// goroutine that pushes, and must never wait for a consumer busy writing
// to the database; nor may a Stop wait for a consumer that has stopped
// reading. So the backlog is unbounded rather than back-pressured.
type Events struct {
	logger *slog.Logger
	out    chan harness.Event

	mu     sync.Mutex
	queue  []harness.Event
	closed bool
	warned bool
	// wake has a token when the queue gained events or closed.
	wake chan struct{}
}

// NewEvents starts the stream. Its goroutine ends once Close has been
// called and the consumer has read every event.
func NewEvents(logger *slog.Logger) *Events {
	e := &Events{logger: logger, out: make(chan harness.Event), wake: make(chan struct{}, 1)}
	go e.pump()
	return e
}

// C is the consumer's end. It closes after Close, once the events pushed
// before it have been read.
func (e *Events) C() <-chan harness.Event { return e.out }

// Push queues evs, unless the stream is closed: it reports whether they
// were queued.
func (e *Events) Push(evs ...harness.Event) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return false
	}
	e.queue = append(e.queue, evs...)
	if len(e.queue) >= backlogWarn && !e.warned {
		e.warned = true
		e.logger.Warn("driverkit: the conversation's consumer is falling behind", "backlog", len(e.queue))
	}
	e.signal()
	return true
}

// Close ends the stream: nothing more is queued, and C closes once the
// consumer has read what was. Calling it again does nothing.
func (e *Events) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	e.signal()
}

func (e *Events) signal() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *Events) pump() {
	defer close(e.out)
	for {
		e.mu.Lock()
		batch, closed := e.queue, e.closed
		e.queue = nil
		if len(batch) < backlogWarn {
			e.warned = false
		}
		e.mu.Unlock()
		for _, ev := range batch {
			e.out <- ev
		}
		if len(batch) > 0 {
			continue
		}
		if closed {
			return
		}
		<-e.wake
	}
}
