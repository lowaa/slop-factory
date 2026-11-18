// Package clock is a service's one injectable source of time, so
// time-dependent behaviour is tested by advancing a Fake rather than waiting.
//
// Temporal workflow code keeps workflow.Now: a workflow must be
// deterministic on replay, which no injected clock can promise.
package clock

import (
	"context"
	"time"
)

type Clock interface {
	Now() time.Time
	NewTicker(d time.Duration) Ticker
	After(d time.Duration) <-chan time.Time
}

type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// Real is the wall clock.
type Real struct{}

func (Real) Now() time.Time                         { return time.Now() }
func (Real) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (Real) NewTicker(d time.Duration) Ticker       { return realTicker{time.NewTicker(d)} }

type realTicker struct{ *time.Ticker }

func (t realTicker) C() <-chan time.Time { return t.Ticker.C }

// Func is a clock whose Now is the function; timers and tickers run on the
// wall clock.
type Func func() time.Time

func (f Func) Now() time.Time                       { return f() }
func (Func) After(d time.Duration) <-chan time.Time { return Real{}.After(d) }
func (Func) NewTicker(d time.Duration) Ticker       { return Real{}.NewTicker(d) }

// Or returns c, or the wall clock when c is nil.
func Or(c Clock) Clock {
	if c == nil {
		return Real{}
	}
	return c
}

// Since is the time elapsed on c since t.
func Since(c Clock, t time.Time) time.Duration { return c.Now().Sub(t) }

// WithDeadline is context.WithDeadline measured on c: a context's own
// deadline runs on the wall clock, which a Fake does not move.
func WithDeadline(parent context.Context, c Clock, deadline time.Time) (context.Context, context.CancelFunc) {
	// The wall clock has a native deadline, which frees its timer when the
	// context ends early and reports Deadline().
	if _, ok := c.(Real); ok {
		return context.WithDeadline(parent, deadline)
	}
	ctx, cancel := context.WithCancel(parent)
	d := deadline.Sub(c.Now())
	if d <= 0 {
		cancel()
		return ctx, cancel
	}
	timer := c.After(d)
	go func() {
		select {
		case <-timer:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}
