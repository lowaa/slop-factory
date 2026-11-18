package reqtrace_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/logging"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reqtrace"
)

const (
	traceID     = "4bf92f3577b34da6a3ce929d0e0e4736"
	traceparent = "00-" + traceID + "-00f067aa0ba902b7-01"
)

// serve runs one request through Middleware over a mux with a failing route, a
// healthy one and a streaming one, and returns what the api's real log
// handler wrote.
func serve(t *testing.T, req *http.Request) (*httptest.ResponseRecorder, string) {
	t.Helper()
	var out bytes.Buffer
	logs, err := logging.New("", &out)
	require.NoError(t, err)
	t.Cleanup(func() { _ = logs.Close(context.Background()) })

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/things/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("GET /api/missing", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("GET /api/private", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("GET /api/ok", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /api/stream", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		assert.NoError(t, http.NewResponseController(w).Flush(), "the recorder must pass a flush through")
	})
	rec := httptest.NewRecorder()
	reqtrace.Middleware(mux, logs.Handler()).ServeHTTP(rec, req)
	return rec, out.String()
}

func TestServerErrorLogCarriesTheBrowsersTraceID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/things/secret-id?token=abc", nil)
	req.Header.Set("Traceparent", traceparent)

	_, log := serve(t, req)

	assert.Contains(t, log, "trace_id="+traceID)
	assert.Contains(t, log, "route=\"GET /api/things/{id}\"")
	assert.Contains(t, log, "status=500")
	assert.NotContains(t, log, "secret-id", "a route template, never the URL")
	assert.NotContains(t, log, "token=abc")
}

func TestClientErrorsAreLoggedAsWarningsExceptUnauthorized(t *testing.T) {
	_, log := serve(t, httptest.NewRequest(http.MethodGet, "/api/missing", nil))
	assert.Contains(t, log, "request failed")
	assert.Contains(t, log, "status=404")
	assert.Regexp(t, `duration_ms=\d+`, log)
	assert.Contains(t, log, "WARN")

	_, log = serve(t, httptest.NewRequest(http.MethodGet, "/api/things/x", nil))
	assert.Contains(t, log, "ERROR")

	_, log = serve(t, httptest.NewRequest(http.MethodGet, "/no/such/path", nil))
	assert.Empty(t, log, "a 404 for a route the api lacks is scanner noise")

	_, log = serve(t, httptest.NewRequest(http.MethodGet, "/api/private", nil))
	assert.Empty(t, log)

	_, log = serve(t, httptest.NewRequest(http.MethodGet, "/api/ok", nil))
	assert.NotContains(t, log, "request failed")
}

func TestContextCarriesOnlyTheTraceparent(t *testing.T) {
	var got trace.SpanContext
	h := reqtrace.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = trace.SpanContextFromContext(r.Context())
	}), slog.DiscardHandler)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Traceparent", traceparent)
	req.Header.Set("Tracestate", "vendor=free text")
	req.Header.Set("Baggage", "user=alice")
	h.ServeHTTP(httptest.NewRecorder(), req)

	assert.Equal(t, traceID, got.TraceID().String())
	assert.Equal(t, "00f067aa0ba902b7", got.SpanID().String())
	assert.Empty(t, got.TraceState().String())
}

func TestInvalidTraceparentIsIgnored(t *testing.T) {
	for _, header := range []string{
		"garbage",
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		"00-" + traceID + "-00f067aa0ba902b7-01-extra\nforged_attr=1",
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/things/1", nil)
		req.Header.Set("Traceparent", header)

		_, log := serve(t, req)

		assert.Contains(t, log, "status=500", header)
		assert.NotContains(t, log, "trace_id", header)
		assert.NotContains(t, log, "forged_attr", header)
	}
}

func TestOnlyServerErrorsAreLogged(t *testing.T) {
	_, log := serve(t, httptest.NewRequest(http.MethodGet, "/api/ok", nil))
	assert.Empty(t, log)
}

func TestUnmatchedRouteIsNotLoggedByPath(t *testing.T) {
	var out bytes.Buffer
	logs, err := logging.New("", &out)
	require.NoError(t, err)
	t.Cleanup(func() { _ = logs.Close(context.Background()) })
	h := reqtrace.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}), logs.Handler())

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/private/ids/123", nil))

	assert.Contains(t, out.String(), "route=unmatched")
	assert.NotContains(t, out.String(), "123")
}

func TestStillFlushable(t *testing.T) {
	rec, _ := serve(t, httptest.NewRequest(http.MethodGet, "/api/stream", nil))
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.True(t, rec.Flushed)
}
