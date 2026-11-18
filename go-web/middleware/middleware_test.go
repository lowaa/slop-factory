package middleware_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lowaa/slop-factory/go-web/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newHandler(buf *bytes.Buffer) slog.Handler {
	return slog.NewTextHandler(buf, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return a
	}})
}

func muxWith(wrap func(http.Handler) http.Handler, h http.HandlerFunc) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /things/{id}", h)
	return wrap(mux)
}

func TestLoggingLogsPathByDefault(t *testing.T) {
	var buf bytes.Buffer
	h := muxWith(func(n http.Handler) http.Handler { return middleware.Logging(n, newHandler(&buf)) },
		func(w http.ResponseWriter, _ *http.Request) {})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/things/secretid", nil))
	assert.Contains(t, buf.String(), "path=/things/secretid")
	assert.NotContains(t, buf.String(), "route=")
}

func TestLoggingRouteLabelsHidePath(t *testing.T) {
	var buf bytes.Buffer
	h := muxWith(func(n http.Handler) http.Handler {
		return middleware.Logging(n, newHandler(&buf), middleware.WithRouteLabels())
	},
		func(w http.ResponseWriter, _ *http.Request) {})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/things/secretid", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/hidden/name", nil))
	out := buf.String()
	assert.Contains(t, out, `route="GET /things/{id}"`)
	assert.Contains(t, out, "route=unmatched")
	assert.NotContains(t, out, "secretid")
	assert.NotContains(t, out, "hidden")
}

func TestRecoverDefault(t *testing.T) {
	var buf bytes.Buffer
	h := muxWith(func(n http.Handler) http.Handler { return middleware.Recover(n, newHandler(&buf)) },
		func(http.ResponseWriter, *http.Request) { panic("boom") })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/things/1", nil))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.JSONEq(t, `{"error":"internal server error"}`, rec.Body.String())
	assert.Contains(t, buf.String(), "err=boom")
	assert.Contains(t, buf.String(), "path=/things/1")
}

func TestRecoverPanicSitesOmitsPanicValue(t *testing.T) {
	var buf bytes.Buffer
	h := muxWith(func(n http.Handler) http.Handler {
		return middleware.Recover(n, newHandler(&buf), middleware.WithPanicSites(), middleware.WithRouteLabels())
	}, func(http.ResponseWriter, *http.Request) { panic("patient-name") })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/things/1", nil))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	out := buf.String()
	assert.NotContains(t, out, "patient-name")
	assert.Contains(t, out, "panic_type=string")
	assert.Contains(t, out, "stack_id=")
	assert.Contains(t, out, "middleware_test.go:")
	assert.Contains(t, out, `route="GET /things/{id}"`)
}

func TestRecoverPanicSitesAloneLogsRouteNotPath(t *testing.T) {
	var buf bytes.Buffer
	h := muxWith(func(n http.Handler) http.Handler {
		return middleware.Recover(n, newHandler(&buf), middleware.WithPanicSites())
	},
		func(http.ResponseWriter, *http.Request) { panic("x") })
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/things/777", nil))
	assert.Contains(t, buf.String(), `route="GET /things/{id}"`)
	assert.NotContains(t, buf.String(), "777")
}

func TestRecoverRepanicsErrAbortHandler(t *testing.T) {
	var buf bytes.Buffer
	h := middleware.Recover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }), newHandler(&buf))
	rec := httptest.NewRecorder()
	assert.PanicsWithValue(t, http.ErrAbortHandler, func() { h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil)) })
	assert.Empty(t, buf.String())
}

func TestLoggingRecordsMethodAndDuration(t *testing.T) {
	var buf bytes.Buffer
	h := middleware.Logging(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), newHandler(&buf))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/p", nil))
	assert.Contains(t, buf.String(), "level=INFO msg=request method=POST")
	assert.Contains(t, buf.String(), "duration=")
}

func TestSecurityHeaders(t *testing.T) {
	var next http.HandlerFunc = func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("x")) }
	for _, https := range []bool{false, true} {
		rec := httptest.NewRecorder()
		middleware.SecurityHeaders(next, https).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		res := rec.Result()
		assert.Equal(t, "default-src 'none'; frame-ancestors 'none'", res.Header.Get("Content-Security-Policy"))
		assert.Equal(t, "nosniff", res.Header.Get("X-Content-Type-Options"))
		assert.Equal(t, "no-referrer", res.Header.Get("Referrer-Policy"))
		assert.Equal(t, "same-origin", res.Header.Get("Cross-Origin-Resource-Policy"))
		assert.Equal(t, "no-store", res.Header.Get("Cache-Control"))
		if https {
			assert.Equal(t, "max-age=31536000; includeSubDomains", res.Header.Get("Strict-Transport-Security"))
		} else {
			assert.Empty(t, res.Header.Get("Strict-Transport-Security"))
		}
	}
}
