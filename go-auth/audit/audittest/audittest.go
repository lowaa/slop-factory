// Package audittest records audit events for tests to assert on.
package audittest

import (
	"log/slog"
	"sync"

	"github.com/lowaa/slop-factory/go-auth/audit"
)

// Counter is an audit.Counter that remembers what it counted.
type Counter struct {
	mu     sync.Mutex
	counts map[string]int
}

// NewCounter is an empty Counter.
func NewCounter() *Counter {
	return &Counter{counts: map[string]int{}}
}

// NewRecorder is a Recorder that logs nowhere and counts into the returned
// Counter.
func NewRecorder() (*audit.Recorder, *Counter) {
	c := NewCounter()
	return audit.New(&audit.Config{Logger: slog.DiscardHandler, Counter: c}), c
}

// AuditEvent is audit.Counter.
func (c *Counter) AuditEvent(action, method string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[action]++
	if method != "" {
		c.counts[action+"/"+method]++
	}
}

// Count is how many times action was recorded, with any method.
func (c *Counter) Count(action audit.Action) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[string(action)]
}

// CountMethod is how many times action was recorded with method.
func (c *Counter) CountMethod(action audit.Action, method string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[string(action)+"/"+method]
}
