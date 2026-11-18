package email

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Outbox sends email from a background worker, never from the request that
// asked for it.
//
// Not for throughput: a request that sends mail for a registered address
// and not for an unknown one would take a mail server's round trip longer,
// and that difference answers the question uniform responses are there to
// hide. A mail server that is down is also then a logged failure rather
// than a failed request.
//
// The queue lives in memory, so mail queued when the process dies is lost;
// the link can be asked for again.
type Outbox struct {
	sender  Sender
	logger  *slog.Logger
	timeout time.Duration

	mu     sync.Mutex
	closed bool
	queue  chan outboxMessage
	done   chan struct{}
}

type outboxMessage struct {
	to, subject, body string
}

// OutboxConfig sizes the queue and bounds each send.
type OutboxConfig struct {
	QueueSize   int
	SendTimeout time.Duration
}

func NewOutbox(sender Sender, cfg *OutboxConfig, logHandler slog.Handler) *Outbox {
	o := &Outbox{
		sender:  sender,
		logger:  slog.New(logHandler),
		timeout: cfg.SendTimeout,
		queue:   make(chan outboxMessage, cfg.QueueSize),
		done:    make(chan struct{}),
	}
	go o.run()
	return o
}

// Enqueue never blocks: a full queue drops the message, logged, rather
// than stall the request -- stalling would bring back the timing
// difference the outbox exists to remove.
func (o *Outbox) Enqueue(to, subject, body string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		o.logger.Error("email dropped: outbox closed", "subject", subject)
		return
	}
	select {
	case o.queue <- outboxMessage{to: to, subject: subject, body: body}:
	default:
		o.logger.Error("email dropped: outbox full", "subject", subject)
	}
}

// Close stops taking mail and waits for what is queued to be sent.
func (o *Outbox) Close() {
	o.mu.Lock()
	if !o.closed {
		o.closed = true
		close(o.queue)
	}
	o.mu.Unlock()
	<-o.done
}

func (o *Outbox) run() {
	defer close(o.done)
	for m := range o.queue {
		ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
		if err := o.sender.Send(ctx, m.to, m.subject, m.body); err != nil {
			// Not the body: it can hold a live one-time link.
			o.logger.Error("send email", "subject", m.subject, "err", err)
		}
		cancel()
	}
}
