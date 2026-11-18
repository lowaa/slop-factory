package metrics

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
)

const lockCollectTimeout = 10 * time.Second

// LockLoad is one integration lock as the metrics see it: how long its
// current holder has held it (0 when free) and how many wait behind it. The
// kernel names no domain package, so the lock state the board's panel reads
// is adapted to this where the collector is wired.
type LockLoad struct {
	Held    time.Duration
	Waiters int
}

// LockSource is every lock of the context's tenant.
type LockSource interface {
	LockLoads(ctx context.Context) ([]LockLoad, error)
}

// LockCollector exports how long the integration lock has been held and how
// many are waiting for it, counted at scrape time from the same state as the
// panel:
//
//   - slophaus_integration_lock_held_seconds: the longest current hold, 0
//     when no lock is held. A holder that is alive but waiting on CI that
//     will never report looks like any other holder, so only its age tells.
//   - slophaus_integration_lock_waiters: agents queued behind any lock.
//
// Like the other collectors the series are summed over every tenant (a
// tenant is a person's workspace, so none is named); the scrape's namespace
// label is what an alert keys on.
type LockCollector struct {
	locks   LockSource
	roster  tenantstate.Roster
	logger  *slog.Logger
	held    *prometheus.Desc
	waiters *prometheus.Desc
}

var _ prometheus.Collector = (*LockCollector)(nil)

func NewLockCollector(locks LockSource, roster tenantstate.Roster, logHandler slog.Handler) *LockCollector {
	return &LockCollector{
		locks: locks, roster: roster, logger: slog.New(logHandler),
		held: prometheus.NewDesc("slophaus_integration_lock_held_seconds",
			"How long the longest-held integration lock has been held by the current holder. 0 when none is held.", nil, nil),
		waiters: prometheus.NewDesc("slophaus_integration_lock_waiters",
			"Agents queued for an integration lock.", nil, nil),
	}
}

func (c *LockCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.held
	ch <- c.waiters
}

func (c *LockCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), lockCollectTimeout)
	defer cancel()
	var longest time.Duration
	waiters := 0
	err := tenantstate.Each(ctx, c.roster, func(ctx context.Context, _ uuid.UUID) error {
		locks, err := c.locks.LockLoads(ctx)
		if err != nil {
			return err
		}
		for _, l := range locks {
			longest = max(longest, l.Held)
			waiters += l.Waiters
		}
		return nil
	})
	if err != nil {
		// A gap, not a zero: a zero would read as "the lock is free".
		c.logger.Error("collect integration lock metrics", "error", err)
		ch <- prometheus.NewInvalidMetric(c.held, err)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.held, prometheus.GaugeValue, longest.Seconds())
	ch <- prometheus.MustNewConstMetric(c.waiters, prometheus.GaugeValue, float64(waiters))
}
