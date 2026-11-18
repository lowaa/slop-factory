// Package middleware holds global interceptors (logging, panic recovery,
// security headers) applied to every route.
package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"github.com/lowaa/slop-factory/go-web/httpx"
)

// unmatchedRoute is what a request no route matched is logged under: the
// path is whatever the caller sent.
const unmatchedRoute = "unmatched"

// Option turns on an optional feature of Logging or Recover.
type Option func(*options)

type options struct {
	routeLabels bool
	panicSites  bool
}

// WithRouteLabels logs the matched route template ("GET /api/tests/{id}")
// under "route" instead of the request path. Use it where a path can carry
// ids or names that must not reach the logs.
func WithRouteLabels() Option { return func(o *options) { o.routeLabels = true } }

// WithPanicSites makes Recover log only the panic value's type, plus a
// stable id of the panicking stack and the panicking frame's file:line,
// instead of the value itself. A panic value can embed request data (an
// index into a slice of the caller's input, a failed type assertion on
// their JSON). Implies route labels in Recover's log line.
func WithPanicSites() Option { return func(o *options) { o.panicSites = true } }

func newOptions(opts []Option) *options {
	o := &options{}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// requestAttr is the attribute that identifies the request in a log line.
func (o *options) requestAttr(r *http.Request) slog.Attr {
	if o.routeLabels {
		return slog.String("route", routeOf(r))
	}
	return slog.String("path", r.URL.Path)
}

// Logging applies to every route -- structured per-request logging via
// slog. The handler is injected rather than reached for via slog's
// package-level functions; nil means slog.Default()'s handler.
func Logging(next http.Handler, logHandler slog.Handler, opts ...Option) http.Handler {
	o := newOptions(opts)
	logger := newLogger(logHandler)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		logger.LogAttrs(r.Context(), slog.LevelInfo, "request",
			slog.String("method", r.Method), o.requestAttr(r), slog.Duration("duration", time.Since(start)))
	})
}

// routeOf is the pattern the mux matched r to. The mux sets it on the very
// request it was handed, so it is visible once next has returned, as long
// as no middleware in between passed on a copy.
func routeOf(r *http.Request) string {
	if r.Pattern == "" {
		return unmatchedRoute
	}
	return r.Pattern
}

// Recover applies to every route -- converts a panic into a 500 instead of
// crashing the server. A panic after the handler has started its response
// cannot change the status already sent; the error body is then appended.
// http.ErrAbortHandler is re-panicked so net/http still aborts the response.
func Recover(next http.Handler, logHandler slog.Handler, opts ...Option) http.Handler {
	o := newOptions(opts)
	logger := newLogger(logHandler)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				// net/http's documented way to abort a response silently.
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				if o.panicSites {
					stackID, site := panicSite()
					logger.LogAttrs(r.Context(), slog.LevelError, "panic recovered",
						slog.String("panic_type", fmt.Sprintf("%T", rec)), slog.String("stack_id", stackID),
						slog.String("site", site), slog.String("route", routeOf(r)))
				} else {
					logger.LogAttrs(r.Context(), slog.LevelError, "panic recovered",
						slog.Any("err", rec), o.requestAttr(r))
				}
				httpx.WriteError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// panicSite identifies the stack of the panic being recovered by a hash of
// its function names -- stable across requests and builds that don't move
// code, so the same bug groups together -- and names the frame that
// panicked. Called from the deferred recover, so the panicking frames are
// still on the stack.
func panicSite() (stackID, site string) {
	pcs := make([]uintptr, 32)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs)])
	h := sha256.New()
	panicked := false
	for {
		f, more := frames.Next()
		// The frame after runtime.gopanic is the one that panicked.
		if panicked && site == "" {
			site = f.File + ":" + strconv.Itoa(f.Line)
		}
		panicked = panicked || f.Function == "runtime.gopanic"
		_, _ = h.Write([]byte(f.Function + "\n"))
		if !more {
			break
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:12], site
}

// SecurityHeaders sets the headers every response carries. The API only
// ever returns JSON, so its policy forbids everything: no scripts, no
// framing. HSTS only when served over https -- sending it over plain http
// is ignored by browsers anyway, and wrong for loopback development.
func SecurityHeaders(next http.Handler, https bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		if https {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func newLogger(h slog.Handler) *slog.Logger {
	if h == nil {
		return slog.Default()
	}
	return slog.New(h)
}
