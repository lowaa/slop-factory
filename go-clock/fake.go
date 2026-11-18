package clock

import (
	"sync"
	"time"
)

// Fake is a Clock a test moves by hand with Advance.
type Fake struct {
	mu      sync.Mutex
	now     time.Time
	waiters []*waiter
	changed chan struct{}
}

type waiter struct {
	at      time.Time
	every   time.Duration // 0 for a one-shot After
	ch      chan time.Time
	stopped bool
}

func NewFake(start time.Time) *Fake {
	return &Fake{now: start, changed: make(chan struct{})}
}

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *Fake) After(d time.Duration) <-chan time.Time {
	return f.add(d, 0).ch
}

func (f *Fake) NewTicker(d time.Duration) Ticker {
	if d <= 0 {
		panic("clock: non-positive ticker interval")
	}
	return &fakeTicker{f: f, w: f.add(d, d)}
}

func (f *Fake) add(d, every time.Duration) *waiter {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := &waiter{at: f.now.Add(d), every: every, ch: make(chan time.Time, 1)}
	if every == 0 && d <= 0 {
		// As time.After(0): due at once, without waiting for an Advance.
		w.ch <- f.now
		w.stopped = true
		return w
	}
	f.waiters = append(f.waiters, w)
	f.signal()
	return w
}

// signal wakes BlockUntil callers; f.mu must be held.
func (f *Fake) signal() {
	close(f.changed)
	f.changed = make(chan struct{})
}

// Waiters is the number of live timers and tickers.
func (f *Fake) Waiters() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.liveLocked()
}

func (f *Fake) liveLocked() int {
	n := 0
	for _, w := range f.waiters {
		if !w.stopped {
			n++
		}
	}
	return n
}

// BlockUntil returns once n timers or tickers are waiting, so a test advances
// time only after the code under test has started waiting on it.
func (f *Fake) BlockUntil(n int) {
	for {
		f.mu.Lock()
		if f.liveLocked() >= n {
			f.mu.Unlock()
			return
		}
		ch := f.changed
		f.mu.Unlock()
		<-ch
	}
}

// Advance moves time forward by d, firing what falls due in order. A ticker
// that is not being read drops ticks, as a real one does.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	target := f.now.Add(d)
	for {
		var next *waiter
		for _, w := range f.waiters {
			if !w.stopped && !w.at.After(target) && (next == nil || w.at.Before(next.at)) {
				next = w
			}
		}
		if next == nil {
			break
		}
		f.now = next.at
		select {
		case next.ch <- next.at:
		default:
		}
		if next.every > 0 {
			next.at = next.at.Add(next.every)
		} else {
			next.stopped = true
		}
	}
	f.now = target
	live := f.waiters[:0]
	for _, w := range f.waiters {
		if !w.stopped {
			live = append(live, w)
		}
	}
	f.waiters = live
}

type fakeTicker struct {
	f *Fake
	w *waiter
}

func (t *fakeTicker) C() <-chan time.Time { return t.w.ch }

func (t *fakeTicker) Stop() {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	t.w.stopped = true
	t.f.signal()
}
