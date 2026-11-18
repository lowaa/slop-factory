package metrics

import (
	"slices"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Landing outcomes the batch metrics tell apart; every other outcome of a
// batch member counts as BatchOther.
const (
	BatchMerged = "merged"
	BatchRed    = "red"
	BatchOther  = "other"
)

// Results of one batch run, by how the batch ended.
const (
	BatchRunGreen           = "green"
	BatchRunRed             = "red"
	BatchRunVoided          = "voided"
	BatchRunEvidenceRefused = "evidence_refused"
	BatchRunFailed          = "failed"
)

// batchRunResults is the closed set of the batch run series' labels.
var batchRunResults = []string{BatchRunGreen, BatchRunRed, BatchRunVoided, BatchRunEvidenceRefused, BatchRunFailed}

// batchRedOutcomes are the outcomes that mean the batch's CI run failed for
// the member, alone or together with the others.
var batchRedOutcomes = map[string]bool{"checks_red": true, "batch_interaction": true}

// landingMetrics are the server-side merge queue's series, held by Metrics.
// They are summed over every tenant, like the other series.
type landingMetrics struct {
	outcomes   *prometheus.CounterVec
	queueDepth prometheus.Histogram
	timeToLand prometheus.Histogram
	batch      *prometheus.CounterVec
	batchRuns  *prometheus.CounterVec
	flakyRetry *prometheus.CounterVec
	now        func() time.Time
	// lastFinished is the unix time of the last landing that ended, merged or
	// handed back; the process start until one does, so a restart cannot make a
	// busy queue look as if nothing had finished for a long time.
	lastFinished prometheus.Gauge
	known        map[string]bool
}

func newLandingMetrics(now func() time.Time) *landingMetrics {
	if now == nil {
		now = time.Now
	}
	l := &landingMetrics{
		now: now,
		lastFinished: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "slophaus_landing_last_finished_timestamp_seconds",
			Help: "Unix time the last server-side landing ended, merged or handed back. The process start until one does.",
		}),
		outcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "slophaus_landing_outcomes_total",
			Help: "Server-side landing attempts, by how each ended (the outcome code).",
		}, []string{"outcome"}),
		queueDepth: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "slophaus_landing_queue_depth",
			Help:    "Other landings waiting when the runner took a change, across all apps of the tenant.",
			Buckets: []float64{0, 1, 2, 3, 5, 8, 13, 21},
		}),
		timeToLand: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "slophaus_landing_time_to_land_seconds",
			Help:    "From a change joining the landing queue to its merge. Includes any time handed back to its author.",
			Buckets: []float64{60, 180, 300, 600, 900, 1200, 1800, 2700, 3600, 7200, 14400},
		}),
		batch: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "slophaus_landing_batch_members_total",
			Help: "Landing attempts of changes tested in a batch of 2 or more, by the member's own recorded outcome: merged, red (checks_red or batch_interaction) or other. A red batch's members are landed alone, so this is member fates, not whether batch runs were green: for that use slophaus_landing_batches_total.",
		}, []string{"result"}),
		batchRuns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "slophaus_landing_batches_total",
			Help: "Batch runs, once each when the batch ends, by result: green (all members merged on the batch's evidence), red (the batch's CI run failed, even if its members then landed alone), voided (main moved, or a member dropped or could not be claimed), evidence_refused (the gate refused the batch's run), failed (handed back for another reason). The batch red rate is red over all results.",
		}, []string{"result"}),
		flakyRetry: newFlakyRetryCounter(),
		known:      map[string]bool{},
	}
	l.lastFinished.Set(float64(now().Unix()))
	return l
}

func (l *landingMetrics) collectors() []prometheus.Collector {
	return []prometheus.Collector{l.outcomes, l.queueDepth, l.timeToLand, l.batch, l.batchRuns, l.flakyRetry, l.lastFinished}
}

// TrackLanding starts a series at zero for each outcome the landing runner can
// record, and for the batch results, so an alert on increase() fires on the
// first event. An outcome not named here is counted as OtherMethod, so the
// label stays a closed set.
func (m *Metrics) TrackLanding(outcomes []string) {
	for _, o := range outcomes {
		m.landing.known[o] = true
		m.landing.outcomes.WithLabelValues(o)
	}
	m.landing.known[OtherMethod] = true
	m.landing.outcomes.WithLabelValues(OtherMethod)
	for _, r := range []string{BatchMerged, BatchRed, BatchOther} {
		m.landing.batch.WithLabelValues(r)
	}
	for _, r := range batchRunResults {
		m.landing.batchRuns.WithLabelValues(r)
	}
}

// LandingOutcome counts one landing attempt (landing.Observer). queueDepth is
// negative where it was not known; enqueuedFor is how long the change had
// waited, used for a merge only; batchSize is 1 outside a batch.
func (m *Metrics) LandingOutcome(outcome string, queueDepth int, enqueuedFor time.Duration, batchSize int) {
	l := m.landing
	label := outcome
	if !l.known[outcome] {
		label = OtherMethod
	}
	l.outcomes.WithLabelValues(label).Inc()
	l.lastFinished.Set(float64(l.now().Unix()))
	if queueDepth >= 0 {
		l.queueDepth.Observe(float64(queueDepth))
	}
	if outcome == "merged" && enqueuedFor > 0 {
		l.timeToLand.Observe(enqueuedFor.Seconds())
	}
	if batchSize > 1 {
		result := BatchOther
		switch {
		case outcome == "merged":
			result = BatchMerged
		case batchRedOutcomes[outcome]:
			result = BatchRed
		}
		l.batch.WithLabelValues(result).Inc()
	}
}

// LandingBatch counts one batch run by how it ended (landing.Observer). A
// result not in the closed set is counted as failed.
func (m *Metrics) LandingBatch(result string) {
	if !slices.Contains(batchRunResults, result) {
		result = BatchRunFailed
	}
	m.landing.batchRuns.WithLabelValues(result).Inc()
}
