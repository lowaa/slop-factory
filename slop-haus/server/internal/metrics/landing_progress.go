package metrics

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
)

// LandingProgress is the landing queue as the metrics see it, adapted where the
// collector is wired because the kernel names no domain package.
type LandingProgress struct {
	// Step is the step the landing in hand is on ("" when none has begun), for
	// StepAge, and StepBound is how long that step may take before the runner
	// is considered stuck.
	Step      string
	StepAge   time.Duration
	StepBound time.Duration
	// Queued is every landing waiting or in hand; OldestWait is the longest any
	// of them has waited.
	Queued     int
	OldestWait time.Duration
	// TopWait is the wait of the waiting landing that ranks first.
	TopWait time.Duration
	// BatchesWithoutCI are testing batches with no CI pipeline, the longest
	// for OldestBatchWithoutCI.
	BatchesWithoutCI     int
	OldestBatchWithoutCI time.Duration
}

// LandingSource is every landing queue of the context's tenant.
type LandingSource interface {
	LandingProgress(ctx context.Context) (LandingProgress, error)
}

// LandingCollector exports whether the merge queue is moving, counted at scrape
// time:
//
//   - slophaus_landing_head_step_seconds{step} and
//     slophaus_landing_head_step_bound_seconds{step}: how long the landing in
//     hand has been on its step, and the bound past which it is stuck. Absent
//     while nothing has begun.
//   - slophaus_landing_queue_length and slophaus_landing_oldest_wait_seconds:
//     how long the queue is and how long its longest waiter has waited. These
//     grow on a healthy busy queue, so nothing pages on them.
//   - slophaus_landing_top_ranked_wait_seconds: how long the waiting landing
//     that ranks first has waited; it is next in line, so a long wait is
//     starvation rather than queue length.
//   - slophaus_landing_batches_without_ci and
//     slophaus_landing_oldest_batch_without_ci_seconds: batches testing with no
//     pipeline recorded, so no CI will report for them.
//
// Summed or maxed over tenants like the other series.
type LandingCollector struct {
	src     LandingSource
	roster  tenantstate.Roster
	logger  *slog.Logger
	step    *prometheus.Desc
	bound   *prometheus.Desc
	length  *prometheus.Desc
	oldest  *prometheus.Desc
	top     *prometheus.Desc
	noCI    *prometheus.Desc
	noCIAge *prometheus.Desc
}

var _ prometheus.Collector = (*LandingCollector)(nil)

func NewLandingCollector(src LandingSource, roster tenantstate.Roster, logHandler slog.Handler) *LandingCollector {
	return &LandingCollector{
		src: src, roster: roster, logger: slog.New(logHandler),
		step: prometheus.NewDesc("slophaus_landing_head_step_seconds",
			"How long the landing in hand has been on its current step.", []string{"step"}, nil),
		bound: prometheus.NewDesc("slophaus_landing_head_step_bound_seconds",
			"How long the landing in hand may stay on its current step before it counts as stuck.", []string{"step"}, nil),
		length: prometheus.NewDesc("slophaus_landing_queue_length",
			"Landings waiting or in hand.", nil, nil),
		oldest: prometheus.NewDesc("slophaus_landing_oldest_wait_seconds",
			"How long the longest-waiting landing has waited since it joined the queue.", nil, nil),
		top: prometheus.NewDesc("slophaus_landing_top_ranked_wait_seconds",
			"How long the waiting landing that ranks first (priority less aging) has waited.", nil, nil),
		noCI: prometheus.NewDesc("slophaus_landing_batches_without_ci",
			"Batches in testing with no CI pipeline recorded.", nil, nil),
		noCIAge: prometheus.NewDesc("slophaus_landing_oldest_batch_without_ci_seconds",
			"How long the longest such batch has been testing.", nil, nil),
	}
}

func (c *LandingCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.step
	ch <- c.bound
	ch <- c.length
	ch <- c.oldest
	ch <- c.top
	ch <- c.noCI
	ch <- c.noCIAge
}

func (c *LandingCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), lockCollectTimeout)
	defer cancel()
	type stepKey = string
	ages, bounds := map[stepKey]time.Duration{}, map[stepKey]time.Duration{}
	var queued int
	var oldest, top, noCIAge time.Duration
	var noCI int
	err := tenantstate.Each(ctx, c.roster, func(ctx context.Context, _ uuid.UUID) error {
		p, err := c.src.LandingProgress(ctx)
		if err != nil {
			return err
		}
		queued += p.Queued
		oldest = max(oldest, p.OldestWait)
		top = max(top, p.TopWait)
		noCI += p.BatchesWithoutCI
		noCIAge = max(noCIAge, p.OldestBatchWithoutCI)
		if p.Step != "" {
			ages[p.Step] = max(ages[p.Step], p.StepAge)
			bounds[p.Step] = max(bounds[p.Step], p.StepBound)
		}
		return nil
	})
	if err != nil {
		// A gap, not a zero: a zero would read as "the queue is moving".
		c.logger.Error("collect landing progress metrics", "error", err)
		for _, d := range []*prometheus.Desc{c.length, c.oldest, c.top, c.noCI, c.noCIAge} {
			ch <- prometheus.NewInvalidMetric(d, err)
		}
		return
	}
	for s, age := range ages {
		ch <- prometheus.MustNewConstMetric(c.step, prometheus.GaugeValue, age.Seconds(), s)
		ch <- prometheus.MustNewConstMetric(c.bound, prometheus.GaugeValue, bounds[s].Seconds(), s)
	}
	ch <- prometheus.MustNewConstMetric(c.length, prometheus.GaugeValue, float64(queued))
	ch <- prometheus.MustNewConstMetric(c.oldest, prometheus.GaugeValue, oldest.Seconds())
	ch <- prometheus.MustNewConstMetric(c.top, prometheus.GaugeValue, top.Seconds())
	ch <- prometheus.MustNewConstMetric(c.noCI, prometheus.GaugeValue, float64(noCI))
	ch <- prometheus.MustNewConstMetric(c.noCIAge, prometheus.GaugeValue, noCIAge.Seconds())
}
