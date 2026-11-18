package attachment

import (
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
)

// Observer is told of the bytes that enter and leave attachment storage.
type Observer interface {
	Stored(tenantID uuid.UUID, bytes int64)
	Removed(tenantID uuid.UUID, bytes int64)
}

type nopObserver struct{}

func (nopObserver) Stored(uuid.UUID, int64)  {}
func (nopObserver) Removed(uuid.UUID, int64) {}

// Metrics counts attachment bytes per tenant. The tenant label is bounded by
// the number of tenants, which is small.
type Metrics struct {
	bytes *prometheus.CounterVec
}

var _ Observer = (*Metrics)(nil)

func NewMetrics() *Metrics {
	return &Metrics{bytes: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "slophaus_attachment_bytes_total",
		Help: "Attachment bytes stored, and removed by a request, by tenant and direction. Removals by the sweeper (a deleted ticket, an archived tenant, a pruned conversation) are not counted: attachment_usage holds a tenant's usage.",
	}, []string{"tenant", "direction"})}
}

func (m *Metrics) Collectors() []prometheus.Collector { return []prometheus.Collector{m.bytes} }

func (m *Metrics) Stored(t uuid.UUID, n int64) {
	m.bytes.WithLabelValues(t.String(), "stored").Add(float64(n))
}

func (m *Metrics) Removed(t uuid.UUID, n int64) {
	m.bytes.WithLabelValues(t.String(), "removed").Add(float64(n))
}
