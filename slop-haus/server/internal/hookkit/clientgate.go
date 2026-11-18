package hookkit

import (
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/lowaa/slop-factory/go-auth/clientip"
)

// Gate wraps a door's handler.
type Gate func(http.Handler) http.Handler

// AddrLimiter counts requests by key; go-auth/ratelimit.Limiter is one.
type AddrLimiter interface {
	Allow(key string) bool
}

// ClientAddr is the address a request is to be counted against.
type ClientAddr func(r *http.Request) netip.Addr

// ClientGate refuses a client address that is over its limit with a 429,
// before anything of the request is read: no body, no signature check, no
// database. Unlike a per-source limit it must not depend on who the sender
// claims to be, because on the internet the stranger's cost to us is what is
// being bounded.
//
// window is the limiter's window, told to the client as Retry-After. The
// refusal is logged at debug level only: a log line per attempt is the
// attacker's to fill the disk with.
func ClientGate(limiter AddrLimiter, addr ClientAddr, window time.Duration, logHandler slog.Handler) Gate {
	logger := slog.New(logHandler)
	retryAfter := strconv.Itoa(int(window.Seconds()))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// An unparsable connection address yields the zero Addr, whose
			// key is the shared "invalid IP" bucket: such requests are
			// limited together rather than let through.
			key := clientip.RateKey(addr(r))
			if !limiter.Allow(key) {
				logger.DebugContext(r.Context(), "client over the hook limit", "client", key)
				w.Header().Set("Retry-After", retryAfter)
				http.Error(w, "too many requests", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
