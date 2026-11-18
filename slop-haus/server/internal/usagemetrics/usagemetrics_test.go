package usagemetrics_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/usagemetrics"
)

type empty struct{}

func newRecorder(t *testing.T) (*usagemetrics.Recorder, *prometheus.Registry) {
	t.Helper()
	rec := usagemetrics.New()
	reg := prometheus.NewRegistry()
	reg.MustRegister(rec.Collectors()...)
	return rec, reg
}

func newServer(t *testing.T, rec *usagemetrics.Recorder, boom bool) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {})
	api := humago.New(mux, huma.DefaultConfig("t", "1"))
	api.UseMiddleware(usagemetrics.Operation)
	huma.Register(api, huma.Operation{OperationID: "list-things", Method: http.MethodGet, Path: "/api/things/{id}"},
		func(_ context.Context, _ *struct {
			ID string `path:"id"`
		}) (*struct{ Body empty }, error) {
			if boom {
				panic("boom")
			}
			return &struct{ Body empty }{}, nil
		})
	rec.SetOperations([]string{"list-things", "never-called"})
	return rec.Middleware(mux)
}

func count(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
	metrics:
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if want, ok := labels[l.GetName()]; ok && want != l.GetValue() {
					continue metrics
				}
			}
			if len(m.GetLabel()) != len(labels) {
				continue
			}
			if m.GetCounter() != nil {
				return m.GetCounter().GetValue()
			}
		}
	}
	return -1
}

func do(h http.Handler, method, path string, mod func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if mod != nil {
		mod(req)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestMiddleware_CountsByOperationOutcomeAndClient(t *testing.T) {
	rec, reg := newRecorder(t)
	h := newServer(t, rec, false)

	do(h, http.MethodGet, "/api/things/abc-123", func(r *http.Request) { r.Header.Set("Authorization", "Bearer x") })
	do(h, http.MethodGet, "/api/things/def-456", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "s", Value: "v"}) })
	do(h, http.MethodGet, "/api/things/ghi", nil)

	for _, client := range []string{"bearer", "browser", "anonymous"} {
		got := count(t, reg, usagemetrics.RequestsMetric, map[string]string{"operation": "list-things", "outcome": "2xx", "client": client})
		assert.Equal(t, 1.0, got, client)
	}
}

func TestMiddleware_UnmatchedHasOneLabelWhateverThePath(t *testing.T) {
	rec, reg := newRecorder(t)
	h := newServer(t, rec, false)

	do(h, http.MethodGet, "/wp-admin/one", nil)
	do(h, http.MethodGet, "/wp-admin/two", nil)

	assert.Equal(t, 2.0, count(t, reg, usagemetrics.RequestsMetric, map[string]string{"operation": "unmatched", "outcome": "4xx", "client": "anonymous"}))
}

func TestMiddleware_SkipsHealthProbes(t *testing.T) {
	rec, reg := newRecorder(t)
	h := newServer(t, rec, false)

	do(h, http.MethodGet, "/healthz", nil)
	do(h, http.MethodGet, "/api/version", nil)

	assert.Equal(t, -1.0, count(t, reg, usagemetrics.RequestsMetric, map[string]string{"operation": "unmatched", "outcome": "2xx", "client": "anonymous"}))
}

func TestMiddleware_PanicCountsAs5xxAndPropagates(t *testing.T) {
	rec, reg := newRecorder(t)
	h := newServer(t, rec, true)

	assert.Panics(t, func() { do(h, http.MethodGet, "/api/things/x", nil) })

	assert.Equal(t, 1.0, count(t, reg, usagemetrics.RequestsMetric, map[string]string{"operation": "list-things", "outcome": "5xx", "client": "anonymous"}))
}

func TestOperations_NeverCalledOperationStartsAtZero(t *testing.T) {
	rec, reg := newRecorder(t)
	newServer(t, rec, false)

	assert.Equal(t, 0.0, count(t, reg, usagemetrics.RequestsMetric, map[string]string{"operation": "never-called", "outcome": "2xx", "client": "browser"}))
}

func TestToolCall_BoundsLabelValues(t *testing.T) {
	rec, reg := newRecorder(t)
	rec.Known("ticket_get")

	rec.ToolCall("ticket_get", "worker", usagemetrics.OutcomeRefused, time.Millisecond)
	rec.ToolCall("made_up_by_a_caller", "agent-uuid-1234", usagemetrics.OutcomeInvalid, 0)
	rec.ToolCall("another_made_up", "agent-uuid-5678", usagemetrics.OutcomeInvalid, 0)
	rec.ToolCall("ticket_get", "worker", usagemetrics.Outcome("outcome-from-a-caller"), 0)

	assert.Equal(t, 1.0, count(t, reg, usagemetrics.ToolCallsMetric, map[string]string{"tool": "ticket_get", "outcome": "refused", "agent_kind": "worker"}))
	assert.Equal(t, 2.0, count(t, reg, usagemetrics.ToolCallsMetric, map[string]string{"tool": "unknown", "outcome": "invalid", "agent_kind": "other"}))
	assert.Equal(t, 1.0, count(t, reg, usagemetrics.ToolCallsMetric, map[string]string{"tool": "ticket_get", "outcome": "error", "agent_kind": "worker"}), "an outcome outside the set is an error")
	assert.Equal(t, 1, testutil.CollectAndCount(rec.Collectors()[2]), "only known tools have a duration series")
	assert.Equal(t, uint64(2), histogramCount(t, reg, "ticket_get"), "each known call is timed; unknown tools are not")
}

// The privacy guarantee: whatever is recorded, only these label names exist.
func TestEverySeriesCarriesOnlyAllowedLabels(t *testing.T) {
	rec, reg := newRecorder(t)
	h := newServer(t, rec, false)
	rec.Known("ticket_get")
	do(h, http.MethodGet, "/api/things/1", nil)
	rec.ToolCall("ticket_get", "console", usagemetrics.OutcomeOK, time.Second)

	families, err := reg.Gather()
	require.NoError(t, err)
	require.NotEmpty(t, families)
	for _, f := range families {
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				assert.Contains(t, append([]string{"le"}, usagemetrics.AllowedLabels...), l.GetName(), "%s", f.GetName())
			}
		}
	}
	assert.Equal(t, []string{"operation", "outcome", "client", "tool", "agent_kind"}, usagemetrics.AllowedLabels)
}

func histogramCount(t *testing.T, reg *prometheus.Registry, tool string) uint64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != usagemetrics.ToolDurationMetric {
			continue
		}
		for _, m := range f.GetMetric() {
			if m.GetLabel()[0].GetValue() == tool {
				return m.GetHistogram().GetSampleCount()
			}
		}
	}
	return 0
}
