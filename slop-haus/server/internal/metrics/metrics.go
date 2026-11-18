// Package metrics is slop-haus's Prometheus instrumentation: the counters
// alerting keys on, on a registry of the server's own, served by Handler on
// a listener that no door in front of the server routes to.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// OtherMethod is the method label for a sign-in method Options did not
// name, so the label's values stay a fixed, small set whatever a caller
// passes.
const OtherMethod = "other"

// Options are what Metrics pre-creates series for. A counter that first
// appears already at 1 hides that increment from increase(); starting
// every known series at zero is what lets an alert fire on the first
// event rather than the second.
type Options struct {
	// Actions are the audit actions that can be recorded.
	Actions []string
	// Methods are the sign-in methods in use: password and the configured
	// providers' names. Any other method is counted as OtherMethod.
	Methods []string
	// SecretKinds are the kinds of credential a refused agent write can name.
	SecretKinds []string
	// Now is the clock the landing series read; time.Now when nil.
	Now func() time.Time
}

// Metrics holds slop-haus's counters. Its methods are safe for concurrent
// use.
type Metrics struct {
	registry    *prometheus.Registry
	methods     map[string]bool
	audit       *prometheus.CounterVec
	rateLimited *prometheus.CounterVec
	overflow    *prometheus.CounterVec
	secrets     *prometheus.CounterVec
	landing     *landingMetrics
	invariants  *invariantMetrics
}

// New builds the counters on a registry of their own, rather than
// client_golang's global one: what /metrics serves is then exactly what
// this package registered, and a test can build as many as it likes.
func New(opts *Options) *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		methods:  map[string]bool{"": true, OtherMethod: true},
		audit: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "slophaus_audit_events_total",
			Help: "Security events, by audit action and sign-in method (empty where the action has none). Each is also an audit log line.",
		}, []string{"action", "method"}),
		rateLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "slophaus_rate_limit_refusals_total",
			Help: "Attempts refused by a rate limit, by limiter.",
		}, []string{"limiter"}),
		overflow: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "slophaus_rate_limit_overflow_total",
			Help: "Attempts counted in a limiter's shared overflow bucket because its key table was full, by limiter.",
		}, []string{"limiter"}),
		secrets: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "slophaus_secret_write_refused_total",
			Help: "Agent writes refused because the text looked like a credential, by kind of credential. Any increase means an agent tried to write one.",
		}, []string{"kind"}),
	}
	m.landing = newLandingMetrics(opts.Now)
	m.invariants = newInvariantMetrics()
	for _, kind := range opts.SecretKinds {
		m.secrets.WithLabelValues(kind)
	}
	for _, method := range opts.Methods {
		m.methods[method] = true
	}
	m.registry.MustRegister(m.audit, m.rateLimited, m.overflow, m.secrets)
	m.registry.MustRegister(m.landing.collectors()...)
	m.registry.MustRegister(m.invariants.violations)
	m.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	for _, action := range opts.Actions {
		for method := range m.methods {
			m.audit.WithLabelValues(action, method)
		}
	}
	return m
}

// Handler serves the registry in the Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{Registry: m.registry})
}

// Register adds collectors built elsewhere -- go-llm-usage's, say -- to
// what Handler serves.
func (m *Metrics) Register(cs ...prometheus.Collector) error {
	for _, c := range cs {
		if err := m.registry.Register(c); err != nil {
			return err
		}
	}
	return nil
}

// AuditEvent counts one audit event (audit.Counter).
func (m *Metrics) AuditEvent(action, method string) {
	if !m.methods[method] {
		method = OtherMethod
	}
	m.audit.WithLabelValues(action, method).Inc()
}

// SecretWriteRefused counts one agent write refused for holding a credential
// (mcpserver.Refusals).
func (m *Metrics) SecretWriteRefused(kind string) {
	m.secrets.WithLabelValues(kind).Inc()
}

// TrackLimiter starts a limiter's series at zero; see Options.
func (m *Metrics) TrackLimiter(name string) {
	m.rateLimited.WithLabelValues(name)
	m.overflow.WithLabelValues(name)
}

// RateLimitRefused counts one refused attempt (ratelimit.Observer).
func (m *Metrics) RateLimitRefused(limiter string) {
	m.rateLimited.WithLabelValues(limiter).Inc()
}

// RateLimitOverflowed counts one attempt that went to the overflow bucket
// (ratelimit.Observer).
func (m *Metrics) RateLimitOverflowed(limiter string) {
	m.overflow.WithLabelValues(limiter).Inc()
}
