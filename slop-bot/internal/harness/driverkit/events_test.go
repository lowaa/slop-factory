package driverkit

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
)

func TestPushNeverWaitsForTheConsumerAndKeepsTheOrder(t *testing.T) {
	t.Parallel()
	e := NewEvents(slog.New(slog.DiscardHandler))
	const n = 3 * backlogWarn
	for i := range n {
		assert.True(t, e.Push(harness.Event{Kind: harness.Text, ExitCode: i}))
	}
	e.Close()
	assert.False(t, e.Push(harness.Event{Kind: harness.Exited}), "nothing is queued after Close")

	got := 0
	for ev := range e.C() {
		assert.Equal(t, got, ev.ExitCode)
		got++
	}
	assert.Equal(t, n, got, "every event pushed before Close is delivered, then C closes")
}

func TestCloseWithNothingQueuedClosesC(t *testing.T) {
	t.Parallel()
	e := NewEvents(slog.New(slog.DiscardHandler))
	e.Close()
	e.Close()
	_, open := <-e.C()
	assert.False(t, open)
}
