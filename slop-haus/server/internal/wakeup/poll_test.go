package wakeup_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/wakeup"
)

type pollResult struct {
	outcome wakeup.Outcome
	err     error
}

func startPoll(ctx context.Context, clock wakeup.Clock, timeout time.Duration, closed <-chan struct{}, check func(context.Context) (bool, error)) <-chan pollResult {
	out := make(chan pollResult, 1)
	go func() {
		o, err := wakeup.Poll(ctx, clock, time.Second, timeout, closed, check)
		out <- pollResult{o, err}
	}()
	return out
}

func await(t *testing.T, ch <-chan pollResult) pollResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("the poll did not end")
		return pollResult{}
	}
}

func TestPollChecksAtOnceAndOnEveryTickUntilDone(t *testing.T) {
	clock := newManualClock()
	checks := 0
	out := startPoll(context.Background(), clock, time.Hour, nil, func(context.Context) (bool, error) {
		checks++
		return checks == 3, nil
	})

	clock.ticks <- time.Now()
	clock.ticks <- time.Now()

	got := await(t, out)
	assert.Equal(t, wakeup.Found, got.outcome)
	assert.NoError(t, got.err)
	assert.Equal(t, 3, checks, "once at the start, then once per tick")
}

func TestPollTimesOut(t *testing.T) {
	clock := newManualClock()
	out := startPoll(context.Background(), clock, time.Hour, nil, func(context.Context) (bool, error) { return false, nil })

	clock.deadline <- time.Now()

	assert.Equal(t, wakeup.TimedOut, await(t, out).outcome)
}

func TestPollWithNoTimeoutChecksOnceAndNeverWaits(t *testing.T) {
	checks := 0
	// The manual clock never ticks: a poll that waited would hang.
	o, err := wakeup.Poll(context.Background(), newManualClock(), time.Second, 0, nil, func(context.Context) (bool, error) {
		checks++
		return false, nil
	})

	require.NoError(t, err)
	assert.Equal(t, wakeup.TimedOut, o)
	assert.Equal(t, 1, checks)
}

func TestPollEndsWhenClosedEvenIfTheCheckFoundSomething(t *testing.T) {
	clock := newManualClock()
	closed := make(chan struct{})
	out := startPoll(context.Background(), clock, time.Hour, closed, func(context.Context) (bool, error) { return false, nil })
	clock.ticks <- time.Now()

	close(closed)
	assert.Equal(t, wakeup.Closed, await(t, out).outcome)

	// Shutdown outranks a check that found something: the answer may never
	// reach the caller, and what it found is still there to be found.
	o, err := wakeup.Poll(context.Background(), newManualClock(), time.Second, time.Hour, closed, func(context.Context) (bool, error) { return true, nil })
	require.NoError(t, err)
	assert.Equal(t, wakeup.Closed, o)
}

func TestPollEndsWithTheContextsErrorOrTheChecks(t *testing.T) {
	clock := newManualClock()
	ctx, cancel := context.WithCancel(context.Background())
	out := startPoll(ctx, clock, time.Hour, nil, func(context.Context) (bool, error) { return false, nil })
	clock.ticks <- time.Now()
	cancel()
	assert.ErrorIs(t, await(t, out).err, context.Canceled)

	boom := errors.New("boom")
	_, err := wakeup.Poll(context.Background(), newManualClock(), time.Second, time.Hour, nil, func(context.Context) (bool, error) { return false, boom })
	assert.ErrorIs(t, err, boom, "a failed check ends the poll: its caller retries with a backoff of its own")
}

func TestPollShutdownOutranksACheckThatFailed(t *testing.T) {
	closed := make(chan struct{})
	close(closed)

	o, err := wakeup.Poll(context.Background(), newManualClock(), time.Second, time.Hour, closed, func(context.Context) (bool, error) {
		return false, errors.New("the database went away as the server did")
	})

	require.NoError(t, err)
	assert.Equal(t, wakeup.Closed, o)
}

func TestPollACallerWhoHasGoneIsNotHandedWhatTheCheckFound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	_, err := wakeup.Poll(ctx, newManualClock(), time.Second, time.Hour, nil, func(context.Context) (bool, error) {
		cancel() // the caller gives up while the read is in flight, and the read finishes
		return true, nil
	})

	assert.ErrorIs(t, err, context.Canceled)
}

func TestPollChecksOnceEvenWhenAlreadyClosedAndCountsIt(t *testing.T) {
	closed := make(chan struct{})
	close(closed)
	checks := 0

	o, err := wakeup.Poll(context.Background(), newManualClock(), time.Second, time.Hour, closed, func(context.Context) (bool, error) {
		checks++
		return true, nil
	})

	require.NoError(t, err)
	assert.Equal(t, wakeup.Closed, o)
	assert.Equal(t, 1, checks, "the check runs once, as documented: the caller learns it found nothing handed over")
}

func TestPollAsksTheClockForTheIntervalAndTimeoutItWasGiven(t *testing.T) {
	clock := newManualClock()
	out := make(chan pollResult, 1)
	go func() {
		o, err := wakeup.Poll(context.Background(), clock, 7*time.Second, 42*time.Minute, nil, func(context.Context) (bool, error) { return false, nil })
		out <- pollResult{o, err}
	}()
	clock.ticks <- time.Now() // the poll is parked: it has asked the clock

	clock.mu.Lock()
	interval, timeout := clock.interval, clock.timeout
	clock.mu.Unlock()
	assert.Equal(t, 7*time.Second, interval)
	assert.Equal(t, 42*time.Minute, timeout)
	clock.deadline <- time.Now()
	await(t, out)
}

// A caller that gives up while a read is in flight must be able to cut it
// short: the check is handed the caller's context, not a fresh one.
func TestPollHandsTheCheckTheCallersContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	inCheck := make(chan struct{})
	out := make(chan pollResult, 1)
	go func() {
		o, err := wakeup.Poll(ctx, newManualClock(), time.Second, time.Hour, nil, func(c context.Context) (bool, error) {
			close(inCheck)
			<-c.Done()
			return false, c.Err() // a read cut short by the caller giving up
		})
		out <- pollResult{o, err}
	}()
	<-inCheck

	cancel()

	got := await(t, out)
	assert.ErrorIs(t, got.err, context.Canceled)
}

func TestPollAGoneCallerOutranksAFailureTheCancellationCaused(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	boom := errors.New("read cut short")

	_, err := wakeup.Poll(ctx, newManualClock(), time.Second, time.Hour, nil, func(context.Context) (bool, error) {
		cancel()
		return false, boom
	})

	assert.ErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, boom)
}

func TestPollWithNoTimeoutAndNoIntervalNeverStartsATicker(t *testing.T) {
	// Zero interval with the real clock would panic in time.NewTicker.
	o, err := wakeup.Poll(context.Background(), wakeup.RealClock{}, 0, 0, nil, func(context.Context) (bool, error) { return false, nil })

	require.NoError(t, err)
	assert.Equal(t, wakeup.TimedOut, o)
}

func TestPollAnImmediateFindingNeedsNoWait(t *testing.T) {
	checks := 0
	// The manual clock never ticks: waiting at all would hang.
	o, err := wakeup.Poll(context.Background(), newManualClock(), time.Second, time.Hour, nil, func(context.Context) (bool, error) {
		checks++
		return true, nil
	})

	require.NoError(t, err)
	assert.Equal(t, wakeup.Found, o)
	assert.Equal(t, 1, checks)
}

func TestPollACheckThatFailsAfterATickEndsThePollWithItsError(t *testing.T) {
	clock := newManualClock()
	boom := errors.New("the second read failed")
	checks := 0
	out := startPoll(context.Background(), clock, time.Hour, nil, func(context.Context) (bool, error) {
		checks++
		if checks == 2 {
			return false, boom
		}
		return false, nil
	})

	clock.ticks <- time.Now()

	assert.ErrorIs(t, await(t, out).err, boom)
}

func TestPollTheDeadlineStillEndsItAfterTicks(t *testing.T) {
	clock := newManualClock()
	out := startPoll(context.Background(), clock, time.Hour, nil, func(context.Context) (bool, error) { return false, nil })
	clock.ticks <- time.Now()
	clock.ticks <- time.Now()

	clock.deadline <- time.Now()

	assert.Equal(t, wakeup.TimedOut, await(t, out).outcome)
}
