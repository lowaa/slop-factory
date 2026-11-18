package main

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/metrics"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
)

const invariantCheckInterval = 5 * time.Minute

// invariantSink takes the counts of the last pass by rule; *metrics.Metrics is one.
type invariantSink interface {
	SetInvariantViolations(counts map[string]int)
}

// invariantMonitor checks the merge queue's cross-table rules on a timer, in
// every tenant whose queue is on. It only reports -- a metric and a log line per
// violation: it never repairs, so a break stays visible until its cause is fixed.
//
// The tables are read one after the other while the runners write them, so a
// violation counts only once a second pass sees it too.
type invariantMonitor struct {
	tenants  func(ctx context.Context) ([]uuid.UUID, error)
	enabled  func(ctx context.Context) (bool, error)
	check    func(ctx context.Context, tenant uuid.UUID) ([]landing.Violation, error)
	sink     invariantSink
	log      *slog.Logger
	interval time.Duration
	// seen are the violations the previous pass found; counts are the confirmed
	// ones, by tenant, so that a tenant whose check fails keeps what it had: a
	// blind check must not clear an alert.
	seen   map[string]bool
	counts map[uuid.UUID]map[string]int
}

func (m *invariantMonitor) Run(ctx context.Context) {
	t := time.NewTicker(m.interval)
	defer t.Stop()
	for {
		m.Pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Pass checks every tenant once.
func (m *invariantMonitor) Pass(ctx context.Context) {
	tenants, err := m.tenants(ctx)
	if err != nil {
		m.log.ErrorContext(ctx, "landing: invariants: list tenants", "err", err)
		return
	}
	seen := map[string]bool{}
	if m.counts == nil {
		m.counts = map[uuid.UUID]map[string]int{}
	}
	for _, tenant := range tenants {
		tctx := db.WithTenant(ctx, tenant)
		on, err := m.enabled(tctx)
		if err != nil {
			m.log.ErrorContext(ctx, "landing: invariants: read the queue mode", "tenant", tenant, "err", err)
			m.keep(tenant, seen)
			continue
		}
		if !on {
			delete(m.counts, tenant)
			continue
		}
		vs, err := m.check(tctx, tenant)
		if err != nil {
			m.log.ErrorContext(ctx, "landing: invariants: check", "tenant", tenant, "err", err)
			m.keep(tenant, seen)
			continue
		}
		counts := map[string]int{}
		for _, v := range vs {
			key := fmt.Sprintf("%s/%s/%d/%d", tenant, v.Rule, v.TicketID, v.BatchID)
			seen[key] = true
			if !m.seen[key] {
				continue
			}
			counts[v.Rule]++
			m.log.WarnContext(ctx, "landing: merge queue invariant violated", "tenant", tenant, "rule", v.Rule, "ticket", v.TicketID, "batch", v.BatchID, "detail", v.Detail)
		}
		m.counts[tenant] = counts
	}
	m.seen = seen
	total := map[string]int{}
	for t, c := range m.counts {
		if !slices.Contains(tenants, t) {
			delete(m.counts, t)
			continue
		}
		for rule, n := range c {
			total[rule] += n
		}
	}
	m.sink.SetInvariantViolations(total)
}

// keep carries a tenant's previous sightings into the next pass.
func (m *invariantMonitor) keep(tenant uuid.UUID, seen map[string]bool) {
	prefix := tenant.String() + "/"
	for k := range m.seen {
		if strings.HasPrefix(k, prefix) {
			seen[k] = true
		}
	}
}

// startInvariantMonitor runs the check beside the landing runner. mode decides
// per tenant whether its queue is on.
func startInvariantMonitor(ctx context.Context, database *db.DB, mode landing.Mode, manifests manifest.Store, telemetry *metrics.Metrics, logHandler slog.Handler) {
	if telemetry == nil {
		return
	}
	telemetry.TrackInvariantRules(landing.InvariantRules)
	policies := manifestBatchPolicy{manifests: manifests}
	m := &invariantMonitor{
		tenants: tenantstate.Active(database.Global()).Tenants,
		enabled: mode.Enabled,
		check: func(ctx context.Context, tenant uuid.UUID) ([]landing.Violation, error) {
			policy, err := policies.Policy(ctx)
			if err != nil {
				return nil, fmt.Errorf("read the batch policy: %w", err)
			}
			return landing.CheckInvariants(ctx, database, tenant, policy)
		},
		sink: telemetry, log: slog.New(logHandler), interval: invariantCheckInterval, seen: map[string]bool{},
	}
	go m.Run(ctx)
}
