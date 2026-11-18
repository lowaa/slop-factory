package metrics

import (
	"slices"

	"github.com/prometheus/client_golang/prometheus"
)

// invariantMetrics is the merge queue's cross-table rules the invariant job
// found broken. A gauge: it is the count of the last pass, so an alert needs no
// increase() and a repaired break clears it.
type invariantMetrics struct {
	violations *prometheus.GaugeVec
	rules      []string
}

func newInvariantMetrics() *invariantMetrics {
	return &invariantMetrics{violations: prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "slophaus_merge_queue_invariant_violations",
		Help: "Violations of the merge queue's rules across landings, batches and attempts found by the last check, by rule, summed over tenants. Alert only: nothing repairs them.",
	}, []string{"rule"})}
}

// TrackInvariantRules starts a series at zero for each rule the checker can
// report, so an alert on the value fires on the first violation.
func (m *Metrics) TrackInvariantRules(rules []string) {
	m.invariants.rules = slices.Clone(rules)
	for _, r := range rules {
		m.invariants.violations.WithLabelValues(r)
	}
}

// SetInvariantViolations replaces the series with the last check's counts by
// rule. A tracked rule missing from counts is zero. Call TrackInvariantRules
// before the first pass: it is not safe to call concurrently with this.
func (m *Metrics) SetInvariantViolations(counts map[string]int) {
	m.invariants.violations.Reset()
	for _, r := range m.invariants.rules {
		m.invariants.violations.WithLabelValues(r)
	}
	for r, n := range counts {
		m.invariants.violations.WithLabelValues(r).Set(float64(n))
	}
}
