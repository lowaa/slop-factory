package metrics

import "github.com/prometheus/client_golang/prometheus"

func newFlakyRetryCounter() *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "slophaus_landing_flaky_retries_total",
		Help: "Known-flaky retries by the merge queue, once per retried workflow when the retry ends: rescued (red, then green on the retry) or confirmed (red twice). expired is whether the workflow's landingKnownFlaky entry was past its review date. A workflow's rescue rate is rescued over both.",
	}, []string{"workflow", "result", "expired"})
}

// LandingFlakyRetry counts the result of one known-flaky retry
// (landing.FlakyObserver).
func (m *Metrics) LandingFlakyRetry(workflow, result string, expired bool) {
	exp := "false"
	if expired {
		exp = "true"
	}
	m.landing.flakyRetry.WithLabelValues(workflow, result, exp).Inc()
}
