package control

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// Server serves the daemon's API on the control socket.
type Server struct {
	mux    *http.ServeMux
	srv    *http.Server
	logger *slog.Logger
	done   chan error
	// cancel ends every request's context when Stop begins. Shutdown alone
	// leaves request contexts live, so long-lived streams (events --follow)
	// would hold Stop until its deadline.
	cancel context.CancelFunc
	// stopping is closed when Stop begins, before request contexts end, so
	// a handler can tell the daemon stopping from its client going away.
	stopping chan struct{}

	stopOnce sync.Once
	stopErr  error
}

// NewServer makes a server whose routes are added with Handle before Start.
func NewServer(logger *slog.Logger) *Server {
	return &Server{mux: http.NewServeMux(), logger: logger, stopping: make(chan struct{})}
}

// Handle registers a route, e.g. "GET /v1/health".
func (s *Server) Handle(pattern string, h http.Handler) { s.mux.Handle(pattern, h) }

// HandleFunc registers a route handler function.
func (s *Server) HandleFunc(pattern string, h http.HandlerFunc) { s.mux.HandleFunc(pattern, h) }

// Start serves on l, authenticating every request with resolver.
func (s *Server) Start(l *Listener, resolver TokenResolver) {
	base, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.srv = &http.Server{
		Handler:           Authenticate(resolver, s.mux),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(s.logger.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return base },
	}
	s.done = make(chan error, 1)
	go func() {
		err := s.srv.Serve(l)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		s.done <- err
	}()
}

// Stopping is closed once Stop has begun.
func (s *Server) Stopping() <-chan struct{} { return s.stopping }

// Stop stops accepting, waits for in-flight requests until ctx ends, then
// closes what's left, and returns once serving ended. Calling it again
// returns the first call's result.
func (s *Server) Stop(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	s.stopOnce.Do(func() {
		close(s.stopping)
		s.cancel()
		err := s.srv.Shutdown(ctx)
		if err != nil {
			err = errors.Join(err, s.srv.Close())
		}
		s.stopErr = errors.Join(err, <-s.done)
	})
	return s.stopErr
}
