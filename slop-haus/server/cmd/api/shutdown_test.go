package main

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/authn/authntest"

	"github.com/lowaa/slop-factory/go-web/server"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/wakeup"
)

// The board's change stream never finishes by itself, so the shutdown has
// to end it: the server is down inside its grace period
// and the browser sees the stream close, to reconnect to whichever server
// is up by then.
func TestShutdown_EndsAnOpenBoardStream(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.UserMiddleware(api, func(context.Context, string) (*authn.User, error) {
		return &authn.User{ID: uuid.UUID{3}, TenantID: uuid.UUID{2}, IssuedAt: time.Now()}, nil
	}, authntest.Guard(), slog.New(slog.DiscardHandler)))
	ticket.RegisterBoardStream(api, ticket.NewBoardStream(fixedRevision(1), nil, slog.DiscardHandler))
	waiter := wakeup.New(wakeup.Deps{}, nil, slog.DiscardHandler)

	const grace = 10 * time.Second
	srv := boardServer("", mux, waiter, &server.Options{ShutdownTimeout: grace})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, sigterm := context.WithCancel(context.Background())
	defer sigterm()
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ctx, ln) }()

	req, err := http.NewRequest(http.MethodGet, "http://"+ln.Addr().String()+"/api/tickets/stream", nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer token")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	stream := bufio.NewReader(resp.Body)
	first, err := stream.ReadString('\n')
	require.NoError(t, err)
	require.Contains(t, first, "id: 1", "the stream is open and has sent the revision")

	sigterm()

	_, err = io.Copy(io.Discard, stream)
	assert.NoError(t, err, "the stream was ended, not cut")
	assert.NoError(t, <-served, "shut down inside the grace period, with nothing left to cut")
}

type fixedRevision int64

func (r fixedRevision) BoardRevision(context.Context) (int64, error) { return int64(r), nil }

func (r fixedRevision) BoardDelta(context.Context, int64, int64) (*ticket.BoardDelta, error) {
	return &ticket.BoardDelta{}, nil
}

type closeCounter struct{ closed int }

func (c *closeCounter) Close() { c.closed++ }

// The board's shutdown answers the conductors' polls and streams along with
// the worker polls: a poll left open would hold the deploy up.
func TestDrainClosesEveryWaitTheBoardHoldsOpen(t *testing.T) {
	t.Parallel()
	a, b := &closeCounter{}, &closeCounter{}

	drain{a, b}.Close()

	assert.Equal(t, 1, a.closed)
	assert.Equal(t, 1, b.closed)
}
