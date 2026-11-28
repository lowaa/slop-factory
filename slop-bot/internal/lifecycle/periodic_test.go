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

func TestPeriodicNeedsItsFields(t *testing.T) {
	t.Parallel()
	assert.Error(t, (&Periodic{N: "x"}).Start(context.Background()))
}
