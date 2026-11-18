package main

import (
	"context"
	"net"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-web/server"
)

func okEndpoint(addr string) *endpoint {
	return &endpoint{addr, server.New(addr, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), nil)}
}

// A bound listener is served as it is, never re-bound: its port stays taken
// from before the server starts until it stops, so nothing can be handed it
// in between.
func TestRunEndpoints_ServesABoundListenerWithoutRebindingIt(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	addr := ln.Addr().String()

	// Taken before the server runs: a pick-then-release would fail here.
	_, rebind := net.Listen("tcp", addr)
	require.Error(t, rebind)

	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runEndpoints(ctx, []*endpoint{okEndpoint(addr)}, map[string]net.Listener{addr: ln})
	}()

	resp, err := http.Get("http://" + addr)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	stop()
	assert.NoError(t, <-done)
}

// An endpoint with no bound listener still binds its own address, as in
// production, and a failure to do so stops the bound ones too.
func TestRunEndpoints_UnboundEndpointBindsItselfAndItsFailureStopsTheRest(t *testing.T) {
	t.Parallel()
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = taken.Close() })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	addr := ln.Addr().String()

	err = runEndpoints(context.Background(),
		[]*endpoint{okEndpoint(addr), okEndpoint(taken.Addr().String())},
		map[string]net.Listener{addr: ln})
	require.Error(t, err)
	assert.ErrorContains(t, err, "address already in use")
	assert.True(t, refused(addr), "the bound endpoint was stopped before runEndpoints returned")
}
