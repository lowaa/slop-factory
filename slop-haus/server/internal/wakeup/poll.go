package wakeup

import (
	"context"
	"time"
)

// RealClock is the Clock a server runs on.
type RealClock struct{}

func (RealClock) Ticker(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

func (RealClock) After(d time.Duration) <-chan time.Time {
	return time.After(d)
}

// Outcome is how a Poll ended, when it did not fail.
type Outcome int

const (
	// Found: check reported it was done.
	Found Outcome = iota
	// TimedOut: the timeout passed first.
	TimedOut
	// Closed: the closed channel was closed, as at shutdown.
	Closed
)

// Poll runs check at once (and only once, for a timeout of zero or less) and then on every tick, until it reports done, the
// timeout passes, closed is closed, or ctx is done.
//
// It is the loop every wait here shares -- a worker poll, work a conductor's
// host waits for, a stream waiting for the next event -- for the reason Waiter polls:
// the state being waited on is in Postgres, so a replica other than the one
// that wrote it hears of it by asking, not by being told. A check that fails
// ends the poll with its error: unlike a worker poll, whose caller would
// only call straight back, these callers retry with a backoff of their own.
//
// What a poll returns is decided in this order: shutdown (every open poll must
// be answered), then a caller that has gone (it must not be handed what it
// will not read, or a failure its own cancellation caused), then the check's
// failure, then what it found. A timeout is last: work the check finds on a
// tick as the timeout falls due is still handed over, not thrown away to be
// found again.
func Poll(ctx context.Context, clock Clock, interval, timeout time.Duration, closed <-chan struct{}, check func(context.Context) (done bool, err error)) (Outcome, error) {
	var (
		deadline <-chan time.Time
		ticks    <-chan time.Time
	)
	for first := true; ; first = false {
		done, err := check(ctx)
		// What outranks what: shutdown, then a caller that has gone, then the
		// check's own failure or finding.
		select {
		case <-closed:
			return Closed, nil
		default:
		}
		if ctx.Err() != nil {
			return Found, ctx.Err()
		}
		if err != nil {
			return Found, err
		}
		if done {
			return Found, nil
		}
		if timeout <= 0 {
			return TimedOut, nil
		}
		if first {
			// Only now that there is something to wait for.
			var stop func()
			ticks, stop = clock.Ticker(interval)
			defer stop()
			deadline = clock.After(timeout)
		}
		select {
		case <-ctx.Done():
			return Found, ctx.Err()
		case <-closed:
			return Closed, nil
		case <-deadline:
			// A timeout that is due together with a shutdown or a gone caller
			// does not outrank them: select chooses among ready cases at random.
			select {
			case <-closed:
				return Closed, nil
			default:
			}
			if ctx.Err() != nil {
				return Found, ctx.Err()
			}
			return TimedOut, nil
		case <-ticks:
		}
	}
}
