package clientip_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/clientip"
)

func TestResolve(t *testing.T) {
	// The cluster's shape: traefik and nginx both in the pod network.
	r, err := clientip.NewResolver([]string{"10.42.0.0/16"})
	require.NoError(t, err)

	cases := []struct {
		name      string
		remote    string
		forwarded []string
		want      string
	}{
		{"direct, no header", "203.0.113.7:5555", nil, "203.0.113.7"},
		// An untrusted peer's header is its own say-so.
		{"untrusted peer's header is ignored", "203.0.113.7:5555", []string{"198.51.100.1"}, "203.0.113.7"},
		{"through traefik and nginx", "10.42.1.9:40000", []string{"203.0.113.7, 10.42.0.5"}, "203.0.113.7"},
		// Whatever a client writes lands left of the address traefik saw.
		{"a spoofed hop left of the real client", "10.42.1.9:40000", []string{"198.51.100.1, 203.0.113.7, 10.42.0.5"}, "203.0.113.7"},
		{"the header split across lines", "10.42.1.9:40000", []string{"203.0.113.7", "10.42.0.5"}, "203.0.113.7"},
		{"garbage in the chain", "10.42.1.9:40000", []string{"not-an-ip, 10.42.0.5"}, "10.42.0.5"},
		{"every hop trusted", "10.42.1.9:40000", []string{"10.42.0.5"}, "10.42.0.5"},
		{"ipv6 client", "10.42.1.9:40000", []string{"2001:db8::1, 10.42.0.5"}, "2001:db8::1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, netip.MustParseAddr(tc.want), r.Resolve(tc.remote, tc.forwarded))
		})
	}
}

func TestNoTrustedProxiesMeansTheConnectionsOwnAddress(t *testing.T) {
	r, err := clientip.NewResolver(nil)
	require.NoError(t, err)
	assert.Equal(t, netip.MustParseAddr("10.42.1.9"), r.Resolve("10.42.1.9:40000", []string{"203.0.113.7"}))
}

func TestABadRangeIsRefused(t *testing.T) {
	_, err := clientip.NewResolver([]string{"10.42.0.0/99"})
	require.Error(t, err)
}

func TestAnIPv6PeerIsParsed(t *testing.T) {
	r, err := clientip.NewResolver([]string{"fd00::/8"})
	require.NoError(t, err)
	assert.Equal(t, netip.MustParseAddr("2001:db8::7"), r.Resolve("[fd00::1]:4000", []string{"2001:db8::7"}))
	assert.Equal(t, netip.MustParseAddr("2001:db8::9"), r.Resolve("[2001:db8::9]:4000", nil))
}

// The middleware is how a handler learns the address: resolved from the
// request, on the context.
func TestTheMiddlewarePutsTheClientOnTheContext(t *testing.T) {
	r, err := clientip.NewResolver([]string{"192.0.2.0/24"})
	require.NoError(t, err)
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(r.Middleware())
	var seen netip.Addr
	huma.Register(api, huma.Operation{OperationID: "x", Method: http.MethodGet, Path: "/x"},
		func(ctx context.Context, _ *struct{}) (*struct{}, error) {
			seen = clientip.FromContext(ctx)
			return &struct{}{}, nil
		})

	req := httptest.NewRequest(http.MethodGet, "/x", nil) // RemoteAddr 192.0.2.1
	req.Header.Add("X-Forwarded-For", "203.0.113.7")
	mux.ServeHTTP(httptest.NewRecorder(), req)
	assert.Equal(t, netip.MustParseAddr("203.0.113.7"), seen)
}

func TestRateKey(t *testing.T) {
	for _, tc := range []struct{ name, addr, want string }{
		{"ipv4 is itself", "203.0.113.7", "203.0.113.7"},
		{"ipv6 is its /64", "2001:db8:1:2:aaaa:bbbb:cccc:dddd", "2001:db8:1:2::/64"},
		{"a tailnet ipv6 address is itself", "fd7a:115c:a1e0::1234:5678", "fd7a:115c:a1e0::1234:5678"},
		{"...in any /64 of tailscale's /48", "fd7a:115c:a1e0:ab12:4843:cd96:626b:430b", "fd7a:115c:a1e0:ab12:4843:cd96:626b:430b"},
		{"just outside it is grouped", "fd7a:115c:a1e1::1", "fd7a:115c:a1e1::/64"},
		{"no address", "", "invalid IP"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var a netip.Addr
			if tc.addr != "" {
				a = netip.MustParseAddr(tc.addr)
			}
			assert.Equal(t, tc.want, clientip.RateKey(a))
		})
	}
	assert.Equal(t,
		clientip.RateKey(netip.MustParseAddr("2001:db8:1:2::1")),
		clientip.RateKey(netip.MustParseAddr("2001:db8:1:2:ffff::9")),
		"every address in one /64 shares a key")
}
