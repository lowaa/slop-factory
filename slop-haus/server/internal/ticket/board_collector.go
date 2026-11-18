package ticket

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// stateDurationBuckets are the upper bounds, in seconds, of the time-in-state
// and lead-time histograms: from a minute (a ticket bounced straight back to the queue)
// through the hours a ticket waits on a person, to a fortnight.
var stateDurationBuckets = []float64{
	60, 5 * 60, 15 * 60, 30 * 60,
	3600, 2 * 3600, 4 * 3600, 8 * 3600, 12 * 3600,
	86400, 2 * 86400, 4 * 86400, 7 * 86400, 14 * 86400,
}

const collectTimeout = 10 * time.Second

// BoardCollector exports the board's shape and how long tickets spend in
// each state, counted from the database at scrape time rather than kept in
// the process:
//
//   - slophaus_tickets: tickets now, by status and priority.
//   - slophaus_ticket_state_duration_seconds: a histogram of how long
//     tickets stayed in a state, by state and the priority they had when
//     they entered it, observed once the ticket has left it. Read off
//     ticket_status_changes, so it is cumulative over every move ever
//     recorded, and a restart neither loses nor repeats an observation.
//   - slophaus_ticket_lead_time_seconds: a histogram of how long tickets took
//     from creation to done, by the priority they had on reaching it. One
//     observation each time a ticket is done, so a ticket reopened and
//     finished again counts again, from its creation.
//
// Both are summed over every tenant, and carry no label that names one or
// names a ticket: a tenant is a person's workspace and a ticket's title is
// its content, and neither belongs in a metric. Nothing here tells tenants
// apart; if that is ever needed, an opaque per-tenant id is the label to add,
// never the tenant's name.
type BoardCollector struct {
	db       *db.DB
	logger   *slog.Logger
	tickets  *prometheus.Desc
	duration *prometheus.Desc
	leadTime *prometheus.Desc
}

var _ prometheus.Collector = (*BoardCollector)(nil)

func NewBoardCollector(database *db.DB, logHandler slog.Handler) *BoardCollector {
	return &BoardCollector{
		db:     database,
		logger: slog.New(logHandler),
		tickets: prometheus.NewDesc("slophaus_tickets",
			"Tickets on the boards now, by status and priority.", []string{"status", "priority"}, nil),
		duration: prometheus.NewDesc("slophaus_ticket_state_duration_seconds",
			"How long tickets stayed in a state before leaving it, by state and the priority they had on entering it.",
			[]string{"state", "priority"}, nil),
		leadTime: prometheus.NewDesc("slophaus_ticket_lead_time_seconds",
			"How long tickets took from creation to done, by the priority they had when done.", []string{"priority"}, nil),
	}
}

func (c *BoardCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.tickets
	ch <- c.duration
	ch <- c.leadTime
}

type boardKey struct{ status, priority string }

type durations struct {
	count   uint64
	sum     float64
	buckets map[float64]uint64
}

func (c *BoardCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
	defer cancel()

	counts := map[boardKey]uint64{}
	spans := map[boardKey]*durations{}
	leads := map[boardKey]*durations{}
	if err := c.read(ctx, counts, spans, leads); err != nil {
		// A scrape that reports nothing is a gap a rule can see; one that
		// reported half the tenants would pass for a drop in tickets.
		c.logger.Error("collect board metrics", "error", err)
		ch <- prometheus.NewInvalidMetric(c.tickets, err)
		return
	}

	for _, status := range model.Statuses() {
		for _, priority := range priorities {
			key := boardKey{string(status), string(priority)}
			ch <- prometheus.MustNewConstMetric(c.tickets, prometheus.GaugeValue, float64(counts[key]), key.status, key.priority)
		}
	}
	for key, d := range spans {
		ch <- prometheus.MustNewConstHistogram(c.duration, d.count, d.sum, d.buckets, key.status, key.priority)
	}
	for key, d := range leads {
		ch <- prometheus.MustNewConstHistogram(c.leadTime, d.count, d.sum, d.buckets, key.priority)
	}
}

var priorities = []model.Priority{model.PriorityLow, model.PriorityNormal, model.PriorityHigh, model.PriorityUrgent}

func (c *BoardCollector) read(ctx context.Context, counts map[boardKey]uint64, spans, leads map[boardKey]*durations) error {
	tenants, err := tenantstate.ActiveIDs(ctx, c.db.Global())
	if err != nil {
		return err
	}
	for _, id := range tenants {
		err := c.db.InReadTx(db.WithTenant(ctx, id), func(q db.Querier) error {
			if err := readCounts(ctx, q, counts); err != nil {
				return err
			}
			if err := readHistogram(ctx, q, stateStays, spans); err != nil {
				return err
			}
			return readHistogram(ctx, q, leadTimes, leads)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func readCounts(ctx context.Context, q db.Querier, counts map[boardKey]uint64) error {
	rows, err := q.Query(ctx, `SELECT status, priority, count(*) FROM tickets GROUP BY status, priority`)
	if err != nil {
		return fmt.Errorf("count tickets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key boardKey
		var n uint64
		if err := rows.Scan(&key.status, &key.priority, &n); err != nil {
			return fmt.Errorf("scan ticket count: %w", err)
		}
		counts[key] += n
	}
	return rows.Err()
}

// stateStays is each finished stay: a stay ends at the ticket's next change,
// so a ticket's current state has no row yet.
const stateStays = `
	SELECT to_status AS state, priority,
	       EXTRACT(EPOCH FROM lead(changed_at) OVER (PARTITION BY tenant_id, ticket_id ORDER BY id) - changed_at)::float8 AS secs
	  FROM ticket_status_changes`

// leadTimes is each arrival at done, measured from the ticket's creation:
// its first change.
const leadTimes = `
	SELECT state, priority, secs FROM (
		SELECT to_status AS state, priority,
		       EXTRACT(EPOCH FROM changed_at - first_value(changed_at) OVER (PARTITION BY tenant_id, ticket_id ORDER BY id))::float8 AS secs
		  FROM ticket_status_changes
	) arrivals WHERE state = 'done'`

// readHistogram adds this tenant's observations -- rows of (state, priority,
// secs) from stays, a null secs being no observation -- to into.
func readHistogram(ctx context.Context, q db.Querier, stays string, into map[boardKey]*durations) error {
	rows, err := q.Query(ctx, `
		WITH stays AS (`+stays+`)
		SELECT state, priority, b.le, count(*), sum(secs)::float8, count(*) FILTER (WHERE secs <= b.le)
		  FROM stays CROSS JOIN unnest($1::float8[]) AS b(le)
		 WHERE secs IS NOT NULL
		 GROUP BY state, priority, b.le`, stateDurationBuckets)
	if err != nil {
		return fmt.Errorf("read durations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			key       boardKey
			le, sum   float64
			n, within uint64
		)
		if err := rows.Scan(&key.status, &key.priority, &le, &n, &sum, &within); err != nil {
			return fmt.Errorf("scan duration: %w", err)
		}
		d := into[key]
		if d == nil {
			d = &durations{buckets: map[float64]uint64{}}
			into[key] = d
		}
		d.buckets[le] += within
		// Each bucket's row repeats the group's total: take it once.
		if le == stateDurationBuckets[0] {
			d.count += n
			d.sum += sum
		}
	}
	return rows.Err()
}
