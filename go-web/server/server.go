// Package server wraps *http.Server: explicit timeouts (never the zero
// value, which leaves connections unbounded and exposed to slowloris-style
// exhaustion) and a graceful Shutdown-based stop instead of a hard Close.
package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// DefaultShutdownTimeout is how long requests in flight get to finish once
// shutdown begins. It has to fit, with the preStop delay before it and the
// log flush after it, inside the pod's terminationGracePeriodSeconds (the
// service's own Deployment, or the cluster's default of 30s): past that the kubelet kills the process, and every
// connection still open is cut rather than finished.
const DefaultShutdownTimeout = 15 * time.Second

type Options struct {
	// ShutdownTimeout defaults to DefaultShutdownTimeout.
	ShutdownTimeout time.Duration
}

type Server struct {
	httpServer      *http.Server
	shutdownTimeout time.Duration
	// draining is cancelled as shutdown begins; streams end on it.
	draining    context.Context
	endStreams  context.CancelFunc
	onShutdowns []func()
}

func New(addr string, handler http.Handler, opts *Options) *Server {
	timeout := DefaultShutdownTimeout
	if opts != nil && opts.ShutdownTimeout > 0 {
		timeout = opts.ShutdownTimeout
	}
	draining, endStreams := context.WithCancel(context.Background())
	s := &Server{shutdownTimeout: timeout, draining: draining, endStreams: endStreams}
	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           s.endingStreamsOnShutdown(handler),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return s
}

// OnShutdown registers f to run as shutdown begins, alongside the in-flight
// requests being waited for: for whatever holds a request open until it is
// told to stop, and so would otherwise hold the shutdown up until its
// timeout. Call it before Run.
func (s *Server) OnShutdown(f func()) {
	s.onShutdowns = append(s.onShutdowns, f)
}

// Run listens on the server's address and serves until ctx is cancelled;
// see Serve.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		s.endStreams()
		return err
	}
	return s.Serve(ctx, ln)
}

// Serve serves on ln until ctx is cancelled, then shuts down gracefully:
// it stops accepting connections, ends the event streams it is serving,
// and waits for the other requests in flight to finish. What is still open
// when the shutdown timeout passes is closed.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	errCh := make(chan error, 1)
	go func() {
		if err := s.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		// Serving failed on its own (the listener broke): the requests
		// still in flight get the same shutdown as on a signal, so none is
		// left running once this returns -- the caller goes on to close
		// what they use.
		return errors.Join(err, s.shutdown())
	case <-ctx.Done():
		return errors.Join(s.shutdown(), <-errCh)
	}
}

// shutdown ends the event streams, runs the shutdown hooks, and waits for
// the other requests in flight, closing what is left once the timeout
// passes.
func (s *Server) shutdown() error {
	// Shutdown waits for every request in flight, and a stream is one that
	// never finishes by itself. Ended first, its client reconnects -- to
	// whichever server is taking new connections by then.
	s.endStreams()
	for _, f := range s.onShutdowns {
		f()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
	defer cancel()
	if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
		// Nothing may outlive the grace period the process is given, so
		// what is left is cut here, where it is logged, rather than by the
		// kill that would follow.
		return errors.Join(fmt.Errorf("server: shutdown: %w", err), s.httpServer.Close())
	}
	return nil
}

// endingStreamsOnShutdown ends each event stream (a GET asking for
// text/event-stream: MCP's standalone stream, the board's change stream) when
// shutdown begins, by cancelling its request's context. Only those: an
// ordinary request is left to finish, and an MCP tool call -- a POST, which
// may also answer as a stream -- is answered on the request that carried
// it, so cancelling that would lose the answer.
func (s *Server) endingStreamsOnShutdown(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isEventStream(r) {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		stop := context.AfterFunc(s.draining, cancel)
		defer stop()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func isEventStream(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	for _, accept := range r.Header.Values("Accept") {
		if strings.Contains(accept, "text/event-stream") {
			return true
		}
	}
	return false
}
