package server_test

import (
	"context"
	"net"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-web/server"
)

// One server failing to listen stops the others: RunAll returns, which it
// only does once every server it started has stopped.
func TestRunAll_OneFailingToListenStopsTheRest(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = taken.Close() }()

	err = server.RunAll(context.Background(),
		server.New("127.0.0.1:0", http.NotFoundHandler(), nil),
		server.New(taken.Addr().String(), http.NotFoundHandler(), nil))

	assert.ErrorContains(t, err, "address already in use")
}

func TestRunAll_StopsEveryServerWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := server.RunAll(ctx,
		server.New("127.0.0.1:0", http.NotFoundHandler(), nil),
		server.New("127.0.0.1:0", http.NotFoundHandler(), nil))

	assert.NoError(t, err)
}
