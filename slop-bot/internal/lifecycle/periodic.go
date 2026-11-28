package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/lowaa/slop-factory/go-clock"
)

// Periodic is a component that runs Run once when it starts, then every
// Every on Clock, until it stops: a sweep. A failed run is logged, and the
// next one tries again.
type Periodic struct {
	N      string
	Every  time.Duration
	Clock  clock.Clock
	Logger *slog.Logger
	// Run is given a context Stop cancels.
	Run func(context.Context) error

	cancel context.CancelFunc
	done   chan struct{}
}

// Name implements Component.
func (p *Periodic) Name() string { return p.N }

// Start implements Component.
func (p *Periodic) Start(context.Context) error {
	if p.Every <= 0 || p.Clock == nil || p.Logger == nil || p.Run == nil {
		return errors.New("lifecycle: a Periodic needs Every, Clock, Logger and Run")
	}
	// Not Start's ctx, which ends with starting; Stop ends this one.
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel, p.done = cancel, make(chan struct{})
	go p.loop(ctx)
	return nil
}

func (p *Periodic) loop(ctx context.Context) {
	defer close(p.done)
	for {
		if err := p.Run(ctx); err != nil && ctx.Err() == nil {
			p.Logger.Warn("lifecycle: a periodic run failed", "component", p.N, "err", err)
		}
		select {
		case <-p.Clock.After(p.Every):
		case <-ctx.Done():
			return
		}
	}
}

// Stop implements Component: it cancels a run in progress and waits, until
// ctx ends, for it to return. A run that outlasts ctx is still going when
// Stop returns; Exited says when it has gone, and the group waits for that
// before stopping what the run uses.
func (p *Periodic) Stop(ctx context.Context) error {
	if p.done == nil {
		return nil
	}
	p.cancel()
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Exited implements Exiter.
func (p *Periodic) Exited() <-chan struct{} {
	if p.done == nil {
		return closed
	}
	return p.done
}

// closed is a channel that is already closed: what never started has
// exited.
var closed = func() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}()
