package email

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingSender struct {
	mu      sync.Mutex
	sent    []string
	block   chan struct{}
	failing bool
}

func (s *recordingSender) Send(_ context.Context, to, subject, body string) error {
	if s.block != nil {
		<-s.block
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, to+"|"+subject+"|"+body)
	if s.failing {
		return errors.New("smtp is down")
	}
	return nil
}

func (s *recordingSender) messages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sent...)
}

func TestOutbox_CloseSendsWhatIsQueued(t *testing.T) {
	s := &recordingSender{}
	o := NewOutbox(s, &OutboxConfig{QueueSize: 4, SendTimeout: time.Second}, slog.NewTextHandler(&bytes.Buffer{}, nil))
	o.Enqueue("a@example.test", "one", "body one")
	o.Enqueue("b@example.test", "two", "body two")
	o.Close()
	assert.Equal(t, []string{"a@example.test|one|body one", "b@example.test|two|body two"}, s.messages())
}

// A request must never wait on the mail server: a full queue drops.
func TestOutbox_FullQueueDropsWithoutBlocking(t *testing.T) {
	s := &recordingSender{block: make(chan struct{})}
	var logs bytes.Buffer
	o := NewOutbox(s, &OutboxConfig{QueueSize: 1, SendTimeout: time.Second}, slog.NewTextHandler(&logs, nil))

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 5; i++ {
			o.Enqueue("a@example.test", "s", "secret-link")
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Enqueue blocked on a stuck sender")
	}
	close(s.block)
	o.Close()
	assert.Less(t, len(s.messages()), 5)
	assert.Contains(t, logs.String(), "outbox full")
	assert.NotContains(t, logs.String(), "secret-link", "a dropped message's body is never logged")
}

func TestOutbox_SendFailureIsLoggedWithoutTheBody(t *testing.T) {
	s := &recordingSender{failing: true}
	var logs bytes.Buffer
	o := NewOutbox(s, &OutboxConfig{QueueSize: 1, SendTimeout: time.Second}, slog.NewTextHandler(&logs, nil))
	o.Enqueue("a@example.test", "Reset", "https://example.test/reset#token=secret")
	o.Close()
	require.Len(t, s.messages(), 1)
	assert.Contains(t, logs.String(), "smtp is down")
	assert.NotContains(t, logs.String(), "token=secret")
}

func TestOutbox_EnqueueAfterCloseDrops(t *testing.T) {
	s := &recordingSender{}
	o := NewOutbox(s, &OutboxConfig{QueueSize: 1, SendTimeout: time.Second}, slog.NewTextHandler(&bytes.Buffer{}, nil))
	o.Close()
	o.Enqueue("a@example.test", "late", "body")
	o.Close()
	assert.Empty(t, s.messages())
}

// hangingSender stands in for a mail server that accepts the connection and
// then never answers: its first send blocks until the outbox gives up on
// it, as SMTPSender's connection does when its context ends.
type hangingSender struct {
	recordingSender
	hung chan struct{}
	once sync.Once
}

func (s *hangingSender) Send(ctx context.Context, to, subject, body string) error {
	first := false
	s.once.Do(func() { first = true })
	if first {
		close(s.hung)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Minute):
			return errors.New("the outbox never gave up on the send")
		}
	}
	return s.recordingSender.Send(ctx, to, subject, body)
}

// A send the mail server never answers is abandoned at the send timeout,
// logged, and the outbox goes on to serve what came after it.
func TestOutbox_AHungSendIsAbandonedAtTheTimeoutAndTheOutboxCarriesOn(t *testing.T) {
	s := &hangingSender{hung: make(chan struct{})}
	var logs syncBuffer
	const timeout = 50 * time.Millisecond
	o := NewOutbox(s, &OutboxConfig{QueueSize: 4, SendTimeout: timeout}, slog.NewTextHandler(&logs, nil))

	start := time.Now()
	o.Enqueue("stuck@example.test", "Stuck", "https://example.test/reset#token=stuck-secret")
	<-s.hung
	o.Enqueue("later@example.test", "Later", "body later")
	o.Close()
	elapsed := time.Since(start)

	assert.Equal(t, []string{"later@example.test|Later|body later"}, s.messages(), "the next message is still sent")
	assert.GreaterOrEqual(t, elapsed, timeout)
	assert.Less(t, elapsed, 10*time.Second, "abandoned at the timeout, not waited out")
	assert.Contains(t, logs.String(), "send email")
	assert.Contains(t, logs.String(), "subject=Stuck")
	assert.Contains(t, logs.String(), context.DeadlineExceeded.Error())
	assert.NotContains(t, logs.String(), "stuck-secret", "not the body")
}

// syncBuffer is a log sink safe to read after the outbox's worker wrote to
// it from another goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
