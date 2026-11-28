package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"sync"
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
	Run    func(context.Context) error

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

// Name implements Component.
func (p *Periodic) Name() string { return p.N }

// Start implements Component.
func (p *Periodic) Start(context.Context) error {
	if p.Every <= 0 || p.Clock == nil || p.Logger == nil || p.Run == nil {
		return errors.New("lifecycle: a Periodic needs Every, Clock, Logger and Run")
	}
	p.stop, p.done = make(chan struct{}), make(chan struct{})
	go p.loop() // #nosec G118 -- the loop outlives Start's ctx; Stop ends it
	return nil
}

func (p *Periodic) loop() {
	defer close(p.done)
	ctx := context.Background()
	for {
		if err := p.Run(ctx); err != nil {
			p.Logger.Warn("lifecycle: a periodic run failed", "component", p.N, "err", err)
		}
		select {
		case <-p.Clock.After(p.Every):
		case <-p.stop:
			return
		}
	}
}

// Stop implements Component: it waits for a run in progress to finish.
func (p *Periodic) Stop(ctx context.Context) error {
	if p.done == nil {
		return nil
	}
	p.stopOnce.Do(func() { close(p.stop) })
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
