// Package lifecycle starts the daemon's components in order and stops them
// in reverse, so nothing outlives what it depends on.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/lowaa/slop-factory/go-clock"
)

// Component is anything the daemon owns that holds resources: a store, a
// listener, a poll loop, a subscription.
type Component interface {
	Name() string
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// Exiter is a component whose Stop can give up at its deadline while it is
// still running (a periodic job in a database call): Exited closes once it
// has really stopped.
type Exiter interface {
	Exited() <-chan struct{}
}

// Group runs components as one.
type Group struct {
	components []Component
	started    []Component
	logger     *slog.Logger
	// exitWait bounds the wait for components that outlived their Stop,
	// on clock; 0 waits as long as they take.
	exitWait time.Duration
	clock    clock.Clock
}

// BoundExits bounds Stop's wait for components that outlived their own
// Stop: all of them together get wait, from the first such wait, on clk.
// Past it Stop carries on, closing what they use: a service manager kills
// the daemon at a deadline derived from this, and being killed mid-close
// is worse.
func (g *Group) BoundExits(wait time.Duration, clk clock.Clock) *Group {
	g.exitWait, g.clock = wait, clk
	return g
}

// NewGroup makes a group; components start in the order given.
func NewGroup(logger *slog.Logger, components ...Component) *Group {
	return &Group{components: components, logger: logger}
}

// Start starts each component in turn. If one fails, those already started
// are stopped, in reverse, before Start returns its error.
func (g *Group) Start(ctx context.Context) error {
	for _, c := range g.components {
		g.logger.Debug("starting", "component", c.Name())
		if err := c.Start(ctx); err != nil {
			startErr := fmt.Errorf("starting %s: %w", c.Name(), err)
			return errors.Join(startErr, g.Stop(ctx))
		}
		g.started = append(g.started, c)
	}
	return nil
}

// Stop stops every started component in reverse order. It carries on past a
// failure, so one stuck component doesn't leak the rest, and reports every
// error. An Exiter whose Stop failed is waited for, past ctx, before
// anything started before it is stopped: those are what it uses, and
// closing them under it is worse than a slow shutdown.
func (g *Group) Stop(ctx context.Context) error {
	var errs []error
	// One deadline for every exit wait, set at the first (see BoundExits):
	// a context's Done, which every wait sees once it has passed.
	var deadline <-chan struct{}
	for i := len(g.started) - 1; i >= 0; i-- {
		c := g.started[i]
		g.logger.Debug("stopping", "component", c.Name())
		if err := c.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("stopping %s: %w", c.Name(), err))
			if e, ok := c.(Exiter); ok {
				if deadline == nil && g.exitWait > 0 {
					exitCtx, cancel := clock.WithDeadline(context.Background(), g.clock, g.clock.Now().Add(g.exitWait))
					defer cancel()
					deadline = exitCtx.Done()
				}
				if err := g.awaitExit(c.Name(), e, deadline); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	g.started = nil
	return errors.Join(errs...)
}

// awaitExit waits for e to exit, or for deadline (nil: none).
func (g *Group) awaitExit(name string, e Exiter, deadline <-chan struct{}) error {
	select {
	case <-e.Exited():
		return nil
	default:
	}
	g.logger.Warn("lifecycle: waiting for a component that outlived its stop, before stopping what it uses", "component", name)
	select {
	case <-e.Exited():
		return nil
	case <-deadline:
		g.logger.Error("lifecycle: gave up waiting for a component that outlived its stop; stopping what it uses anyway", "component", name)
		return fmt.Errorf("lifecycle: %s still running after its stop and the exit wait", name)
	}
}

// Func adapts a pair of functions into a Component.
type Func struct {
	N         string
	StartFunc func(context.Context) error
	StopFunc  func(context.Context) error
}

// Name returns the component's name.
func (f *Func) Name() string { return f.N }

// Start calls StartFunc, if set.
func (f *Func) Start(ctx context.Context) error {
	if f.StartFunc == nil {
		return nil
	}
	return f.StartFunc(ctx)
}

// Stop calls StopFunc, if set.
func (f *Func) Stop(ctx context.Context) error {
	if f.StopFunc == nil {
		return nil
	}
	return f.StopFunc(ctx)
}
