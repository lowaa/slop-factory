package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
)

func TestPeriodicRunsAtStartThenEveryPeriodUntilStopped(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC))
	runs := make(chan time.Time, 4)
	p := &Periodic{N: "sweep", Every: time.Hour, Clock: clk, Logger: slog.New(slog.DiscardHandler),
		Run: func(context.Context) error {
			runs <- clk.Now()
			return errors.New("a failed run doesn't stop the next")
		}}
	require.NoError(t, p.Start(context.Background()))
	assert.Equal(t, clk.Now(), <-runs, "at once")
	clk.BlockUntil(1)
	clk.Advance(30 * time.Minute)
	clk.Advance(30 * time.Minute)
	assert.Equal(t, clk.Now(), <-runs, "an hour on")
	clk.BlockUntil(1)
	require.NoError(t, p.Stop(context.Background()))
	require.NoError(t, p.Stop(context.Background()), "twice is fine")
	clk.Advance(time.Hour)
	assert.Empty(t, runs, "stopped")
}

// Stop cancels a run in progress, and returns once it has ended: a sweep
// mustn't carry on into the stores the daemon closes next.
func TestPeriodicStopCancelsARunInProgress(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	p := &Periodic{N: "sweep", Every: time.Hour, Clock: clock.NewFake(time.Now()), Logger: slog.New(slog.DiscardHandler),
		Run: func(ctx context.Context) error {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		}}
	require.NoError(t, p.Start(context.Background()))
	<-entered
	// A safety net, not a synchronisation: a Stop that doesn't cancel the
	// run gives up here instead of hanging the test.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	require.NoError(t, p.Stop(ctx))
	select {
	case <-p.Exited():
	default:
		assert.Fail(t, "Stop returned before the run ended")
	}
}

// A run that ignores its context outlives a Stop that gives up at its
// deadline; Exited says when it has really gone.
func TestPeriodicExitedOutlivesAStopThatGaveUp(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	p := &Periodic{N: "sweep", Every: time.Hour, Clock: clock.NewFake(time.Now()), Logger: slog.New(slog.DiscardHandler),
		Run: func(context.Context) error {
			close(entered)
			<-release
			return nil
		}}
	require.NoError(t, p.Start(context.Background()))
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, p.Stop(ctx), context.Canceled)
	select {
	case <-p.Exited():
		assert.Fail(t, "the run is still going")
	default:
	}
	close(release)
	<-p.Exited()
}

func TestPeriodicNeedsItsFields(t *testing.T) {
	t.Parallel()
	p := &Periodic{N: "x"}
	assert.Equal(t, "x", p.Name())
	assert.Error(t, p.Start(context.Background()))
	// Never started: nothing to stop, and nothing still running.
	assert.NoError(t, p.Stop(context.Background()))
	<-p.Exited()
}
