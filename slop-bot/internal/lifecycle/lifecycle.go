// Package lifecycle starts the daemon's components in order and stops them
// in reverse, so nothing outlives what it depends on.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// Component is anything the daemon owns that holds resources: a store, a
// listener, a poll loop, a subscription.
type Component interface {
	Name() string
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// Group runs components as one.
type Group struct {
	components []Component
	started    []Component
	logger     *slog.Logger
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
// error.
func (g *Group) Stop(ctx context.Context) error {
	var errs []error
	for i := len(g.started) - 1; i >= 0; i-- {
		c := g.started[i]
		g.logger.Debug("stopping", "component", c.Name())
		if err := c.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("stopping %s: %w", c.Name(), err))
		}
	}
	g.started = nil
	return errors.Join(errs...)
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
