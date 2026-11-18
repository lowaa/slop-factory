// Package clocktest has clocks for tests.
package clocktest

import (
	"sync"
	"time"

	"github.com/lowaa/slop-factory/go-clock"
)

// noTimers gives Fixed and Stepped the rest of clock.Clock. They only report
// a time, so a timer on one would never fire: fail loudly, and use
// clock.Fake for code that waits.
type noTimers struct{}

func (noTimers) After(time.Duration) <-chan time.Time { panic("clocktest: use clock.Fake for timers") }
func (noTimers) NewTicker(time.Duration) clock.Ticker { panic("clocktest: use clock.Fake for tickers") }

// Fixed is a clock that stays where it is put.
type Fixed struct {
	noTimers
	mu sync.Mutex
	t  time.Time
}

func NewFixed(t time.Time) *Fixed { return &Fixed{t: t} }

func (f *Fixed) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

// Set moves the clock to t.
func (f *Fixed) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = t
}

// Advance moves the clock forward by d.
func (f *Fixed) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

// Stepped is a clock that moves forward by a step on every Now, so
// successive readings differ (a duration measured with it is non-zero).
type Stepped struct {
	noTimers
	mu   sync.Mutex
	t    time.Time
	step time.Duration
}

// NewStepped returns start on the first Now, then start+step, and so on.
func NewStepped(start time.Time, step time.Duration) *Stepped {
	return &Stepped{t: start, step: step}
}

func (s *Stepped) Now() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.t
	s.t = s.t.Add(s.step)
	return t
}
