package server_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-web/server"
)

// serving runs srv on a free local port until the returned stop is called,
// and reports what Serve returned on done.
func serving(t *testing.T, srv *server.Server) (base string, stop context.CancelFunc, done <-chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() { errs <- srv.Serve(ctx, ln) }()
	// Every test reads done itself; this only stops a server a failed
	// assertion left running.
	t.Cleanup(cancel)
	return "http://" + ln.Addr().String(), cancel, errs
}

// stream is a handler that holds an event stream open until its request's
// context ends, the way the MCP SDK's standalone stream and the board's change stream
// do. It announces each stream once its headers are out.
func stream(started chan<- struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		started <- struct{}{}
		<-r.Context().Done()
	}
}

// fetch is the response body, or the error that stopped it.
func fetch(req *http.Request) string {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err.Error()
	}
	return strings.TrimSpace(string(body))
}

func openStream(t *testing.T, base, method string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, base, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// A client's event stream is ended, and ended cleanly, as shutdown begins:
// it would otherwise hold the shutdown up until its timeout and then be cut.
func TestShutdown_EndsEventStreams(t *testing.T) {
	started := make(chan struct{}, 1)
	// Only a stream the shutdown ended lets it finish inside this; one
	// left open is cut when it passes, and fails below.
	srv := server.New("", stream(started), &server.Options{ShutdownTimeout: 5 * time.Second})
	base, stop, done := serving(t, srv)

	resp := openStream(t, base, http.MethodGet)
	<-started
	stop()

	// A clean end of the body, not a reset connection: the client sees the
	// stream finish and reconnects.
	_, err := io.Copy(io.Discard, resp.Body)
	assert.NoError(t, err)
	assert.NoError(t, <-done)
}

// Only a GET is a stream to end. A POST that answers as one is an MCP tool
// call, answered on its own request: ending it would lose the answer.
func TestShutdown_LetsAToolCallFinish(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	srv := server.New("", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-release:
			_, _ = io.WriteString(w, "answered")
		case <-r.Context().Done():
		}
	}), &server.Options{ShutdownTimeout: time.Minute})
	shuttingDown := make(chan struct{})
	srv.OnShutdown(func() { close(shuttingDown) })
	base, stop, done := serving(t, srv)

	answer := make(chan string, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, base, nil)
		req.Header.Set("Accept", "application/json, text/event-stream")
		answer <- fetch(req)
	}()
	<-started
	stop()
	<-shuttingDown
	close(release)

	assert.Equal(t, "answered", <-answer)
	assert.NoError(t, <-done)
}

func TestShutdown_RunsItsHooksAsItBegins(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	srv := server.New("", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-release
	}), &server.Options{ShutdownTimeout: time.Minute})
	// The hook is what ends the request, as the waiter ends a ticket_wait:
	// the shutdown only finishes because the hook ran while it waited.
	srv.OnShutdown(func() { close(release) })
	base, stop, done := serving(t, srv)

	go func() {
		resp, err := http.Get(base)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-started
	stop()

	assert.NoError(t, <-done)
}

// Hooks run in the order they were registered, and all of them before Serve
// returns, so a hook that depends on an earlier one's work can rely on it.
func TestShutdown_RunsHooksInRegistrationOrder(t *testing.T) {
	srv := server.New("", http.NotFoundHandler(), nil)
	var ran []string
	srv.OnShutdown(func() { ran = append(ran, "first") })
	srv.OnShutdown(func() { ran = append(ran, "second") })
	_, stop, done := serving(t, srv)

	stop()

	require.NoError(t, <-done)
	assert.Equal(t, []string{"first", "second"}, ran)
}

// Nothing outlives the shutdown timeout: a request that will not finish is
// closed once it passes, and Serve says so.
func TestShutdown_ClosesWhatOutlivesTheTimeout(t *testing.T) {
	started := make(chan struct{}, 1)
	never := make(chan struct{})
	defer close(never)
	srv := server.New("", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		started <- struct{}{}
		<-never
	}), &server.Options{ShutdownTimeout: 50 * time.Millisecond})
	base, stop, done := serving(t, srv)

	// The client's own timeout only bounds a failing run: a connection left
	// open ends in it, and fails below.
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(base)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	<-started
	stop()

	assert.ErrorIs(t, <-done, context.DeadlineExceeded)
	_, err = bufio.NewReader(resp.Body).ReadString('\n')
	require.Error(t, err)
	assert.False(t, os.IsTimeout(err), "the connection is closed, not left open: %v", err)
}

// Ordinary requests are never ended early: shutdown waits for them.
func TestShutdown_WaitsForOrdinaryRequests(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	srv := server.New("", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-release:
			_, _ = io.WriteString(w, "done")
		case <-r.Context().Done():
			_, _ = io.WriteString(w, "cancelled")
		}
	}), &server.Options{ShutdownTimeout: time.Minute})
	shuttingDown := make(chan struct{})
	srv.OnShutdown(func() { close(shuttingDown) })
	base, stop, done := serving(t, srv)

	answer := make(chan string, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodGet, base, nil)
		answer <- fetch(req)
	}()
	<-started
	stop()
	<-shuttingDown
	close(release)

	assert.Equal(t, "done", <-answer)
	assert.NoError(t, <-done)
}

// A listener that breaks under the server is a shutdown too: the requests
// in flight are wound down as on a signal, not left running while the
// process goes on to close what they use.
func TestServe_WindsDownWhenTheListenerFails(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	srv := server.New("", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-release
		_, _ = io.WriteString(w, "answered")
	}), &server.Options{ShutdownTimeout: 5 * time.Second})
	srv.OnShutdown(func() { close(release) })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background(), ln) }()

	answer := make(chan string, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodGet, "http://"+ln.Addr().String(), nil)
		answer <- fetch(req)
	}()
	<-started
	require.NoError(t, ln.Close())

	// Bounded only so a broken run fails rather than hangs: without the
	// shutdown the hook never runs and the request never finishes.
	select {
	case got := <-answer:
		assert.Equal(t, "answered", got, "the request in flight was let finish")
	case <-time.After(10 * time.Second):
		t.Fatal("the request in flight was never wound down")
	}
	assert.Error(t, <-done, "the listener's failure is reported")
}
