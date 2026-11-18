package hookkit_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/clientip"
	"github.com/lowaa/slop-factory/go-auth/ratelimit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/hookkit"
)

type clock struct{ now atomic.Int64 }

func (c *clock) Now() time.Time          { return time.Unix(c.now.Load(), 0) }
func (c *clock) advance(d time.Duration) { c.now.Add(int64(d.Seconds())) }

type gateFixture struct {
	clock   *clock
	reached atomic.Int32
	handler http.Handler
}

func newGateFixture(t *testing.T, limit, maxKeys int, resolver *clientip.Resolver) *gateFixture {
	t.Helper()
	f := &gateFixture{clock: &clock{}}
	f.clock.now.Store(1_000_000)
	lim := ratelimit.New(&ratelimit.Config{Name: "t", Limit: limit, Window: time.Minute, MaxKeys: maxKeys, Now: f.clock.Now})
	addr := func(r *http.Request) netip.Addr {
		return resolver.Resolve(r.RemoteAddr, r.Header.Values("X-Forwarded-For"))
	}
	gate := hookkit.ClientGate(lim, addr, time.Minute, slog.DiscardHandler)
	f.handler = gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.reached.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	return f
}

func (f *gateFixture) get(remote string, forwarded ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.RemoteAddr = remote
	for _, v := range forwarded {
		req.Header.Add("X-Forwarded-For", v)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func noProxies(t *testing.T) *clientip.Resolver {
	r, err := clientip.NewResolver(nil)
	require.NoError(t, err)
	return r
}

func TestClientGateRefusesAnAddressOverItsLimit(t *testing.T) {
	f := newGateFixture(t, 2, 100, noProxies(t))
	assert.Equal(t, http.StatusNoContent, f.get("192.0.2.1:1").Code)
	assert.Equal(t, http.StatusNoContent, f.get("192.0.2.1:2").Code, "the port is not part of the address")

	rec := f.get("192.0.2.1:3")
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "60", rec.Header().Get("Retry-After"))
	assert.EqualValues(t, 2, f.reached.Load(), "the refused request never reached the handler")
}

func TestClientGateLeavesOtherAddressesAloneAndRefills(t *testing.T) {
	f := newGateFixture(t, 1, 100, noProxies(t))
	require.Equal(t, http.StatusNoContent, f.get("192.0.2.1:1").Code)
	require.Equal(t, http.StatusTooManyRequests, f.get("192.0.2.1:1").Code)

	assert.Equal(t, http.StatusNoContent, f.get("192.0.2.2:1").Code, "a second address has its own allowance")

	f.clock.advance(61 * time.Second)
	assert.Equal(t, http.StatusNoContent, f.get("192.0.2.1:1").Code, "the limit refills")
}

func TestClientGateTableStaysBoundedUnderManyAddresses(t *testing.T) {
	f := newGateFixture(t, 1, 3, noProxies(t))
	var refused int
	for i := 1; i <= 10; i++ {
		if f.get("192.0.2."+strconv.Itoa(i)+":1").Code == http.StatusTooManyRequests {
			refused++
		}
	}
	// Three addresses get a table entry; the rest share one overflow
	// bucket, so only one more is let through.
	assert.Equal(t, 6, refused)
}
