package supervisorwatch

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const collectTimeout = 10 * time.Second

// Collector exports what the supervisor-offline alert reads, counted at
// scrape time:
//
//   - slophaus_claimable_tickets: tickets waiting for an agent to take them.
//   - slophaus_last_supervisor_poll_timestamp_seconds: the newest poll.
//   - slophaus_supervisor_slots_stalled: slots of enabled agents that are
//     stalled (see Service.Slots); absent when they could not be judged.
//   - slophaus_supervisor_slots_paused: paused slots of enabled agents in
//     tenants with work waiting.
//   - slophaus_supervisor_slots_by_reason{reason}: online slots not running
//     a ticket, by the reason they report.
//   - slophaus_last_claim_timestamp_seconds: the newest claim by any agent,
//     which is what lets the alert tell a busy fleet
//     from one that has stopped.
//
// The timestamps are absent until the first poll or claim ever, which the
// rule reads as "never". Like BoardCollector, the series are summed over
// every tenant and name none: a tenant is a person's workspace.
type Collector struct {
	svc       *Service
	logger    *slog.Logger
	claimable *prometheus.Desc
	polled    *prometheus.Desc
	claimed   *prometheus.Desc
	stalled   *prometheus.Desc
	paused    *prometheus.Desc
	byReason  *prometheus.Desc
}

var _ prometheus.Collector = (*Collector)(nil)

func NewCollector(svc *Service, logHandler slog.Handler) *Collector {
	return &Collector{
		svc:    svc,
		logger: slog.New(logHandler),
		claimable: prometheus.NewDesc("slophaus_claimable_tickets",
			"Open tickets nothing but an agent's taking them is holding up. Zero while ticket processing is off.", nil, nil),
		polled: prometheus.NewDesc("slophaus_last_supervisor_poll_timestamp_seconds",
			"When any supervisor last polled. Absent until one has.", nil, nil),
		claimed: prometheus.NewDesc("slophaus_last_claim_timestamp_seconds",
			"When any agent last took a ticket. Absent until one has.", nil, nil),
		stalled: prometheus.NewDesc("slophaus_supervisor_slots_stalled",
			"Supervisor slots of enabled agents that are stalled: idle while they could take work, running without a lease heartbeat, or repeating one error.", nil, nil),
		paused: prometheus.NewDesc("slophaus_supervisor_slots_paused",
			"Paused supervisor slots of enabled agents, in tenants that have tickets waiting. How long they have been paused is the alert's to judge.", nil, nil),
		byReason: prometheus.NewDesc("slophaus_supervisor_slots_by_reason",
			"Online supervisor slots that are not running a ticket, by the reason they report. One series per reason code, so a slot is never named.", []string{"reason"}, nil),
	}
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.claimable
	ch <- c.polled
	ch <- c.claimed
	ch <- c.stalled
	ch <- c.paused
	ch <- c.byReason
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
	defer cancel()
	f, err := c.svc.Fleet(ctx)
	if err != nil {
		// A gap, not a zero: a zero would read as "nothing is waiting".
		c.logger.Error("collect supervisor metrics", "error", err)
		ch <- prometheus.NewInvalidMetric(c.claimable, err)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.claimable, prometheus.GaugeValue, float64(f.Claimable))
	if f.LastPolledAt != nil {
		ch <- prometheus.MustNewConstMetric(c.polled, prometheus.GaugeValue, float64(f.LastPolledAt.Unix()))
	}
	if f.LastClaimedAt != nil {
		ch <- prometheus.MustNewConstMetric(c.claimed, prometheus.GaugeValue, float64(f.LastClaimedAt.Unix()))
	}
	stalled, err := c.svc.StalledSlots(ctx)
	if err != nil {
		c.logger.Error("collect stalled slots", "error", err)
		ch <- prometheus.NewInvalidMetric(c.stalled, err)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.stalled, prometheus.GaugeValue, float64(stalled))
	paused, err := c.svc.PausedSlots(ctx)
	if err != nil {
		c.logger.Error("collect paused slots", "error", err)
		ch <- prometheus.NewInvalidMetric(c.paused, err)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.paused, prometheus.GaugeValue, float64(paused))
	byReason, err := c.svc.SlotsByReason(ctx)
	if err != nil {
		c.logger.Error("collect slots by reason", "error", err)
		ch <- prometheus.NewInvalidMetric(c.byReason, err)
		return
	}
	for reason, n := range byReason {
		ch <- prometheus.MustNewConstMetric(c.byReason, prometheus.GaugeValue, float64(n), string(reason))
	}
}
