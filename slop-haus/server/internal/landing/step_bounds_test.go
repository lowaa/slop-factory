package landing

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/go-clock"
)

type timeoutPipelines struct {
	Pipelines
	timeout time.Duration
	err     error
	reads   int
}

func (p *timeoutPipelines) RepoTimeout(context.Context) (time.Duration, error) {
	p.reads++
	return p.timeout, p.err
}

func TestStepBounds_ChecksFollowTheRunnersWaitAndOtherStepsAreFixed(t *testing.T) {
	cfg := Config{RestartMargin: 5 * time.Minute, FallbackChecksTimeout: 20 * time.Minute}
	clk := clock.NewFake(time.Unix(0, 0))
	p := &timeoutPipelines{timeout: time.Hour}
	b := NewStepBounds(p, cfg, clk, slog.DiscardHandler)
	ctx := context.Background()

	assert.Equal(t, 130*time.Minute, b.Of(ctx, WaitingChecks), "twice the repo timeout plus margin: the lost pipeline's and its restart's")
	assert.Equal(t, otherStepBound, b.Of(ctx, MergingMain))
	assert.Equal(t, otherStepBound, b.Of(ctx, Merging))

	b.Of(ctx, WaitingChecks)
	assert.Equal(t, 1, p.reads, "kept between scrapes")
	p.timeout = 2 * time.Hour
	clk.Advance(checksReadTTL + time.Second)
	assert.Equal(t, 250*time.Minute, b.Of(ctx, WaitingChecks))
}

func TestStepBounds_UnreadableTimeoutFallsBack(t *testing.T) {
	b := NewStepBounds(&timeoutPipelines{err: errors.New("down")}, Config{RestartMargin: 5 * time.Minute, FallbackChecksTimeout: 20 * time.Minute}, nil, slog.DiscardHandler)
	assert.Equal(t, 50*time.Minute, b.Of(context.Background(), WaitingChecks))
}

func TestChecksBound_ZeroRepoTimeoutUsesTheFallbackWithoutWarning(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := Config{}.withDefaults()

	got := checksBound(context.Background(), zeroTimeout{}, &cfg, log)

	assert.Equal(t, cfg.FallbackChecksTimeout+cfg.RestartMargin, got)
	assert.NotContains(t, buf.String(), "WARN")
	assert.Contains(t, buf.String(), "level=DEBUG")
}

type zeroTimeout struct{ Pipelines }

func (zeroTimeout) RepoTimeout(context.Context) (time.Duration, error) { return 0, nil }
