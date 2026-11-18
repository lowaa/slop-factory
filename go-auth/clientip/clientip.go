// Package clientip works out which address a request came from when it has
// passed through proxies -- traefik, then the frontend's nginx -- each of
// which appends the address it received from to X-Forwarded-For.
//
// Only proxies named as trusted are believed. Anyone can send an
// X-Forwarded-For header, so the list is read from the right, skipping the
// trusted hops, and the first address that is not one is the client: a
// value the client wrote itself can only ever appear to the left of that.
package clientip

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// Resolver knows which proxies to believe.
type Resolver struct {
	trusted []netip.Prefix
}

// NewResolver parses trusted proxy ranges in CIDR form. None means no proxy
// is believed and the connection's own address is the client's.
func NewResolver(cidrs []string) (*Resolver, error) {
	r := &Resolver{}
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		p, err := netip.ParsePrefix(c)
		if err != nil {
			return nil, fmt.Errorf("clientip: trusted proxy %q: %w", c, err)
		}
		r.trusted = append(r.trusted, p.Masked())
	}
	return r, nil
}

func (r *Resolver) isTrusted(a netip.Addr) bool {
	for _, p := range r.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Resolve returns the client address for a connection from remoteAddr
// ("host:port") carrying the given X-Forwarded-For values.
func (r *Resolver) Resolve(remoteAddr string, forwardedFor []string) netip.Addr {
	peer := parseAddr(remoteAddr)
	if !peer.IsValid() || !r.isTrusted(peer) {
		return peer
	}

	var hops []netip.Addr
	for _, header := range forwardedFor {
		for _, part := range strings.Split(header, ",") {
			hops = append(hops, parseAddr(strings.TrimSpace(part)))
		}
	}
	for i := len(hops) - 1; i >= 0; i-- {
		if !hops[i].IsValid() {
			// Garbage in the chain: nothing to its left can be placed, so
			// the nearest believable hop is the answer.
			return peer
		}
		if !r.isTrusted(hops[i]) {
			return hops[i]
		}
		peer = hops[i]
	}
	// Every hop is a trusted proxy -- a request from inside the network.
	return peer
}

func parseAddr(s string) netip.Addr {
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}

// tailnetV6 is Tailscale's IPv6 range. Tailscale assigns each node its own
// address out of it, so its /64s are shared by unrelated tailnet machines
// rather than owned by one of them.
var tailnetV6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")

// RateKey is what a rate limit counts a client under: its IPv4 address,
// or the /64 an IPv6 address sits in. One IPv6 host is routinely handed a
// whole /64, so keying by the full address would give it 2^64 fresh
// allowances -- and let it fill a limiter's key table on its own.
// Tailscale's own addresses are the exception: see tailnetV6.
func RateKey(a netip.Addr) string {
	if !a.Is6() || tailnetV6.Contains(a) {
		return a.String()
	}
	p, err := a.Prefix(64)
	if err != nil {
		return a.String()
	}
	return p.String()
}

type contextKey struct{}

// FromContext returns the client address the middleware recorded, or the
// zero Addr when there is none.
func FromContext(ctx context.Context) netip.Addr {
	a, _ := ctx.Value(contextKey{}).(netip.Addr)
	return a
}

// NewContext records the client address on ctx.
func NewContext(ctx context.Context, a netip.Addr) context.Context {
	return context.WithValue(ctx, contextKey{}, a)
}

// Middleware records every request's client address on its context.
func (r *Resolver) Middleware() func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		var forwarded []string
		ctx.EachHeader(func(name, value string) {
			if strings.EqualFold(name, "X-Forwarded-For") {
				forwarded = append(forwarded, value)
			}
		})
		addr := r.Resolve(ctx.RemoteAddr(), forwarded)
		next(huma.WithContext(ctx, NewContext(ctx.Context(), addr)))
	}
}
