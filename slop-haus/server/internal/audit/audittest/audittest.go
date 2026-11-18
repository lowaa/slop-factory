// Package audittest records slop-haus's audit events for tests to assert
// on.
package audittest

import (
	"log/slog"

	goaudittest "github.com/lowaa/slop-factory/go-auth/audit/audittest"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
)

// Counter is an audit.Counter that remembers what it counted.
type Counter = goaudittest.Counter

// NewCounter is an empty Counter.
func NewCounter() *Counter {
	return goaudittest.NewCounter()
}

// NewRecorder is a Recorder with slop-haus's action set that logs nowhere
// and counts into the returned Counter.
func NewRecorder() (*audit.Recorder, *Counter) {
	c := NewCounter()
	return audit.New(slog.DiscardHandler, c), c
}
