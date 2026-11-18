// Package reqtrace ties a request's server logs to the W3C traceparent the
// browser sent (js-components/logging/src/tracing), so a frontend error's
// trace id finds the server's logs for the same request.
package reqtrace

import (
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/propagation"
)

// Middleware takes the traceparent into the request's context and logs each
// failed answer with that context, which is how otellog adds trace_id and
// span_id to the record. 5xx is an error; 4xx is a warning, except 401, which
// is every signed-out visitor's first request and would bury the rest.
//
// Only the trace and span ids are kept: tracestate and baggage are free text
// from the caller, so they are never read and never reach anything the request
// calls. A header that is not a valid traceparent is ignored and the request
// goes on untraced. The ids are random per request and name nothing about the
// sender. Nothing the caller controls is logged: the route is the mux's
// pattern, not the URL, which could hold ids or a query string.
func Middleware(next http.Handler, logHandler slog.Handler) http.Handler {
	propagator := propagation.TraceContext{}
	logger := slog.New(logHandler)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		carrier := propagation.MapCarrier{"traceparent": r.Header.Get("Traceparent")}
		r = r.WithContext(propagator.Extract(r.Context(), carrier))
		rec := &statusRecorder{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(rec, r)
		level, ok := levelOf(rec.status)
		// Probes of paths the api doesn't have are scanner noise any anonymous
		// caller can generate at will, and name nothing a developer could fix.
		if !ok || (r.Pattern == "" && rec.status < 500) {
			return
		}
		logger.Log(r.Context(), level, "request failed",
			"method", r.Method, "route", routeOf(r), "status", rec.status,
			"duration_ms", time.Since(start).Milliseconds())
	})
}

func levelOf(status int) (slog.Level, bool) {
	switch {
	case status >= 500:
		return slog.LevelError, true
	case status >= 400 && status != http.StatusUnauthorized:
		return slog.LevelWarn, true
	}
	return 0, false
}

// routeOf is the pattern the mux matched, which is a template; a request it
// matched nothing for has none, and must not fall back to the caller's path.
func routeOf(r *http.Request) string {
	if r.Pattern == "" {
		return "unmatched"
	}
	return r.Pattern
}

// statusRecorder notes the status written. Unwrap keeps http.ResponseController
// (and so flushing, which the board's event stream needs) working through it.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Flush is here for handlers that assert http.Flusher rather than use a
// ResponseController (the MCP and event streams).
func (s *statusRecorder) Flush() { _ = http.NewResponseController(s.ResponseWriter).Flush() }
