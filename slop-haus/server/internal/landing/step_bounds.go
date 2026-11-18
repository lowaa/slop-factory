package landing

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/lowaa/slop-factory/go-clock"
)

// otherStepBound is how long a step that is not a wait for CI may take: a
// merge of main, or the merge itself, is a few code host calls, and the runner
// retries them for far less than this.
const otherStepBound = 15 * time.Minute

// checksReadTTL is how long the repo timeout is kept between reads, so a
// metrics scrape does not call CI every time.
const checksReadTTL = 10 * time.Minute

// StepBounds says how long each landing step may take before the runner is
// stuck on it. For waiting_checks it is the bound the runner itself waits to
// (one bound, then a second for the pipeline it restarted), so a landing past
// it is one the runner failed to hand back.
type StepBounds struct {
	pipelines Pipelines
	cfg       Config
	clock     clock.Clock
	log       *slog.Logger

	mu      sync.Mutex
	timeout time.Duration
	readAt  time.Time
}

// NewStepBounds reads the checks timeout from pipelines; clk may be nil.
func NewStepBounds(pipelines Pipelines, cfg Config, clk clock.Clock, logHandler slog.Handler) *StepBounds {
	return &StepBounds{pipelines: pipelines, cfg: cfg.withDefaults(), clock: clock.Or(clk), log: slog.New(logHandler)}
}

// Of is the bound for step.
func (b *StepBounds) Of(ctx context.Context, step Step) time.Duration {
	if step != WaitingChecks {
		return otherStepBound
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.timeout <= 0 || b.clock.Now().Sub(b.readAt) > checksReadTTL {
		b.timeout, b.readAt = checksBound(ctx, b.pipelines, &b.cfg, b.log), b.clock.Now()
	}
	return 2 * b.timeout
}

// checksBound is the longest the runner waits for a change's checks: the repo's
// CI timeout (the fallback when unreadable) plus the restart margin.
func checksBound(ctx context.Context, p Pipelines, cfg *Config, log *slog.Logger) time.Duration {
	timeout, err := p.RepoTimeout(ctx)
	switch {
	case err != nil:
		log.WarnContext(ctx, "landing: CI repo timeout unreadable; using the fallback", "err", err)
		timeout = cfg.FallbackChecksTimeout
	case timeout <= 0:
		// Woodpecker reports 0 when the repo has no timeout of its own and the
		// server's default applies, which the API does not expose: not a fault,
		// so not a warning on every landing.
		log.DebugContext(ctx, "landing: CI repo has no timeout of its own; using the fallback")
		timeout = cfg.FallbackChecksTimeout
	}
	return timeout + cfg.RestartMargin
}
