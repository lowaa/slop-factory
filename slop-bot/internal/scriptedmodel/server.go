package scriptedmodel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// ControlPrefix is where the test-facing control endpoints live, the same
// paths as the Python server's:
//
//	PUT    /_scripted/scenario           load a scenario (resets the recording)
//	DELETE /_scripted/scenario           reset
//	GET    /_scripted/requests           every request received, in order
//	GET    /_scripted/errors             the unexpected-request failures so far
//	GET    /_scripted/remaining          how many scripted turns are unplayed
//	PUT    /_scripted/gates/{name}       release a turn that waits on name
//	GET    /_scripted/gates/{name}/held  block until a request waits on name
//	GET    /healthz
const ControlPrefix = "/_scripted"

// A conversation is large, but not this large: a bound so a bad client cannot
// have the server buffer without limit.
const maxBody = 64 << 20

// DefaultGateTimeout is how long a turn waits at a gate nobody opens before
// it is an error.
const DefaultGateTimeout = 60 * time.Second

// DefaultModels is the model list served when Options.Models is empty.
var DefaultModels = []string{"scripted-model-1", "scripted-model-2"}

// Options configures a Server. The zero value serves on a free localhost port.
type Options struct {
	// Addr is the listen address; empty means 127.0.0.1:0.
	Addr string
	// Logger defaults to discarding.
	Logger      *slog.Logger
	GateTimeout time.Duration
	// Models is what the model-list endpoints list.
	Models []string
}

// A Server is the scripted model behind an HTTP listener: harnesses talk to
// URL, and a Go test drives it directly (Load, Release, Recording) or through
// the control endpoints (Client).
type Server struct {
	script      *script
	adapters    map[string]adapter
	log         *slog.Logger
	gateTimeout time.Duration
	models      []string
	ids         atomic.Uint64
	listener    net.Listener
	http        *http.Server
	url         string
	// closed releases every request held at a gate when the server stops.
	closed    chan struct{}
	closeOnce sync.Once
	closeErr  error
	served    chan error
}

// New starts a Server listening on opts.Addr. Close stops it.
func New(opts *Options) (*Server, error) {
	if opts == nil {
		opts = &Options{}
	}
	s := &Server{
		log:         opts.Logger,
		gateTimeout: opts.GateTimeout,
		models:      opts.Models,
		closed:      make(chan struct{}),
		served:      make(chan error, 1),
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.gateTimeout <= 0 {
		s.gateTimeout = DefaultGateTimeout
	}
	if len(s.models) == 0 {
		s.models = DefaultModels
	}
	s.adapters = map[string]adapter{}
	for _, a := range []adapter{anthropicMessages{}, openAIChat{}, openAIResponses{}, gemini{}} {
		s.adapters[a.name()] = a
	}
	s.script = newScript(func(req *Request) string { return s.adapters[req.API].summarise(req) })

	addr := opts.Addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", addr, err)
	}
	s.listener = listener
	s.url = "http://" + listener.Addr().String()
	s.http = &http.Server{Handler: s.routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() { s.served <- s.http.Serve(listener) }()
	return s, nil
}

// URL is the base URL harnesses are pointed at, with no trailing slash.
func (s *Server) URL() string { return s.url }

// Load replaces the scenario and resets the recording.
func (s *Server) Load(scenario *Scenario) error {
	if err := scenario.Validate(); err != nil {
		return err
	}
	s.script.replace(scenario)
	return nil
}

// Reset unloads the scenario and resets the recording.
func (s *Server) Reset() { s.script.replace(nil) }

// Release opens a gate: the turn waiting on it (now or later) is answered.
func (s *Server) Release(gate string) { s.script.gate(gate).open() }

// Held is closed once a request is waiting at the gate: the point at which a
// test knows the harness has asked for the gated turn.
func (s *Server) Held(gate string) <-chan struct{} { return s.script.gate(gate).held }

// Recording is a copy of what was received since the scenario was loaded.
func (s *Server) Recording() *Recording { return s.script.snapshot() }

// Remaining is how many scripted turns are unplayed.
func (s *Server) Remaining() int { return s.script.remaining() }

// Close stops the server, releasing any request held at a gate.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.closeErr = s.http.Close()
		if served := <-s.served; !errors.Is(served, http.ErrServerClosed) {
			s.closeErr = errors.Join(s.closeErr, served)
		}
	})
	return s.closeErr
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	harness := map[string]string{
		"POST /v1/messages":          "anthropic",
		"POST /v1/chat/completions":  "openai",
		"POST /v1/responses":         "responses",
		"POST /v1beta/models/{call}": "gemini",
		"POST /v1/models/{call}":     "gemini",
	}
	for pattern, name := range harness {
		a := s.adapters[name]
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) { s.serveModel(w, r, a) })
	}
	mux.HandleFunc("GET /v1beta/models", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBytes(w, http.StatusOK, geminiModelList(s.models, false))
	})
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBytes(w, http.StatusOK, geminiModelList(s.models, true))
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, ok) })
	s.controlRoutes(mux)
	mux.HandleFunc("/", s.serveUnknown)
	return s.logged(mux)
}

func (s *Server) logged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.log.Info("request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
		next.ServeHTTP(w, r)
	})
}

// serveUnknown fails loudly on a POST: an API this server does not speak is
// a harness doing something the scenario did not plan for.
func (s *Server) serveUnknown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.log.Warn("no such route", "method", r.Method, "path", r.URL.Path)
		writeJSON(w, http.StatusNotFound, errorBody(fmt.Sprintf("no such route %s %s", r.Method, r.URL.Path)))
		return
	}
	err := s.script.fail(fmt.Sprintf("POST %s is not an API this server speaks", r.URL.Path), nil)
	s.log.Error("unexpected request", "error", err)
	writeJSON(w, http.StatusNotFound, errorBody(err.Error()))
}

func (s *Server) serveModel(w http.ResponseWriter, r *http.Request, a adapter) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		s.writeModelError(w, a, http.StatusBadRequest, s.script.fail(fmt.Sprintf("reading %s: %v", r.URL.Path, err), nil))
		return
	}
	req, err := a.parse(r, body)
	if err != nil {
		s.writeModelError(w, a, http.StatusBadRequest, s.script.fail(fmt.Sprintf("%s %s: %v", a.name(), r.URL.Path, err), nil))
		return
	}
	turn, g, err := s.script.answer(req)
	if err != nil {
		s.writeModelError(w, a, http.StatusInternalServerError, err)
		return
	}
	if g != nil && !s.wait(r.Context(), g, turn, req) {
		s.writeModelError(w, a, http.StatusInternalServerError,
			s.script.fail(fmt.Sprintf("gate %q was not opened within %s", turn.Gate, s.gateTimeout), req))
		return
	}
	if turn == nil {
		turn = auxiliaryTurn
	}
	responseID := fmt.Sprintf("msg_scripted_%d", s.ids.Add(1))
	s.writeTurn(w, a.encode(turn, req, responseID), req.Stream)
}

// wait holds a gated turn until its gate opens, reporting false if it never
// did. A harness that hangs up, or the server closing, also ends the wait:
// neither is the scenario's failure.
func (s *Server) wait(ctx context.Context, g *gate, turn *Turn, req *Request) bool {
	g.markHeld()
	timer := time.NewTimer(s.gateTimeout)
	defer timer.Stop()
	select {
	case <-g.opened:
		return true
	case <-ctx.Done():
		return true
	case <-s.closed:
		return true
	case <-timer.C:
		s.log.Error("gate timed out", "gate", turn.Gate, "api", req.API)
		return false
	}
}

func (s *Server) writeTurn(w http.ResponseWriter, chunks [][]byte, stream bool) {
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, chunk := range chunks {
		// #nosec G705 -- JSON or SSE, served as such with nosniff, never HTML.
		if _, err := w.Write(chunk); err != nil {
			s.log.Warn("writing a turn", "error", err)
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func (s *Server) writeModelError(w http.ResponseWriter, a adapter, status int, err error) {
	s.log.Error("unexpected request", "error", err)
	writeJSONBytes(w, status, a.encodeError(status, err.Error()))
}

var ok = map[string]bool{"ok": true}

func errorBody(message string) map[string]string { return map[string]string{"error": message} }

func writeJSON(w http.ResponseWriter, status int, v any) { writeJSONBytes(w, status, mustJSON(v)) }

func writeJSONBytes(w http.ResponseWriter, status int, data []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(data) // #nosec G705 -- JSON, served as such with nosniff, never HTML.
}

func (s *Server) controlRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT "+ControlPrefix+"/scenario", func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
			return
		}
		scenario, err := ParseScenario(data)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
			return
		}
		s.script.replace(scenario)
		writeJSON(w, http.StatusOK, ok)
	})
	mux.HandleFunc("DELETE "+ControlPrefix+"/scenario", func(w http.ResponseWriter, _ *http.Request) {
		s.Reset()
		writeJSON(w, http.StatusOK, ok)
	})
	mux.HandleFunc("GET "+ControlPrefix+"/requests", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, nonNil(s.Recording().Requests))
	})
	mux.HandleFunc("GET "+ControlPrefix+"/errors", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, nonNil(s.Recording().Errors))
	})
	mux.HandleFunc("GET "+ControlPrefix+"/remaining", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]int{"remaining": s.Remaining()})
	})
	mux.HandleFunc("PUT "+ControlPrefix+"/gates/{name}", func(w http.ResponseWriter, r *http.Request) {
		s.Release(r.PathValue("name"))
		writeJSON(w, http.StatusOK, ok)
	})
	mux.HandleFunc("GET "+ControlPrefix+"/gates/{name}/held", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-s.Held(r.PathValue("name")):
			writeJSON(w, http.StatusOK, ok)
		case <-r.Context().Done():
		case <-s.closed:
			writeJSON(w, http.StatusServiceUnavailable, errorBody("the server is closing"))
		}
	})
}

func nonNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}
