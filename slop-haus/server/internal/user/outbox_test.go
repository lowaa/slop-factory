package user_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// blockingSender holds every send until released, standing in for a slow
// or unreachable mail server.
type blockingSender struct {
	release chan struct{}
	mu      sync.Mutex
	sent    []string
	err     error
}

func (b *blockingSender) Send(_ context.Context, to, _, _ string) error {
	<-b.release
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sent = append(b.sent, to)
	return b.err
}

func (b *blockingSender) recipients() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.sent...)
}

// The point of the outbox: however slow the mail server, the request that
// queued the mail does not wait for it.
func TestEnqueueDoesNotWaitForTheMailServer(t *testing.T) {
	sender := &blockingSender{release: make(chan struct{})}
	o := user.NewOutbox(sender, &user.OutboxConfig{QueueSize: 4, SendTimeout: time.Second}, slog.DiscardHandler)

	done := make(chan struct{})
	go func() {
		o.Enqueue("a@example.com", "s", "b")
		o.Enqueue("b@example.com", "s", "b")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Enqueue blocked on the send")
	}

	close(sender.release)
	o.Close()
	assert.Equal(t, []string{"a@example.com", "b@example.com"}, sender.recipients(), "Close waits for queued mail")
}

func TestAFullQueueDropsRatherThanBlocks(t *testing.T) {
	sender := &blockingSender{release: make(chan struct{})}
	o := user.NewOutbox(sender, &user.OutboxConfig{QueueSize: 1, SendTimeout: time.Second}, slog.DiscardHandler)

	done := make(chan struct{})
	go func() {
		for range 5 {
			o.Enqueue("a@example.com", "s", "b")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Enqueue blocked on a full queue")
	}
	close(sender.release)
	o.Close()
	assert.Less(t, len(sender.recipients()), 5)
}

func TestAFailedSendDoesNotStopTheOutbox(t *testing.T) {
	sender := &blockingSender{release: make(chan struct{}), err: errors.New("mail server down")}
	close(sender.release)
	o := user.NewOutbox(sender, &user.OutboxConfig{QueueSize: 4, SendTimeout: time.Second}, slog.DiscardHandler)

	o.Enqueue("a@example.com", "s", "b")
	o.Enqueue("b@example.com", "s", "b")
	o.Close()
	assert.Len(t, sender.recipients(), 2)
}

func TestEnqueueAfterCloseIsDroppedNotAPanic(t *testing.T) {
	sender := &blockingSender{release: make(chan struct{})}
	close(sender.release)
	o := user.NewOutbox(sender, &user.OutboxConfig{QueueSize: 1, SendTimeout: time.Second}, slog.DiscardHandler)
	o.Close()

	assert.NotPanics(t, func() { o.Enqueue("a@example.com", "s", "b") })
	o.Close()
}
