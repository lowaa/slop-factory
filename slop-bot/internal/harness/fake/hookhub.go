package fake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness/tuidriver"
)

// HookHub is a tuidriver.HookHub for tests, standing in for the daemon's:
// hooks run `fakeharness --hook-relay <socket> <session> <event>`, which
// relays the invocation to it over a unix socket.
type HookHub struct {
	binary string
	l      net.Listener
	dir    string
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	routes map[string]*hubRoute
}

type hubRoute struct{ h tuidriver.HookHandler }

type hubRequest struct {
	Session string `json:"session"`
	Event   string `json:"event"`
	Payload []byte `json:"payload"`
}

type hubResponse struct {
	Reply []byte `json:"reply,omitempty"`
	Err   string `json:"err,omitempty"`
}

// NewHookHub listens on a fresh socket; binary is the fakeharness the
// relays run as.
func NewHookHub(binary string) (*HookHub, error) {
	dir, err := os.MkdirTemp("", "fakehub")
	if err != nil {
		return nil, err
	}
	l, err := net.Listen("unix", filepath.Join(dir, "h.sock"))
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &HookHub{binary: binary, l: l, dir: dir, ctx: ctx, cancel: cancel, routes: map[string]*hubRoute{}}
	h.wg.Add(1)
	go h.accept()
	return h, nil
}

// Route implements tuidriver.HookHub.
func (h *HookHub) Route(session string, fn tuidriver.HookHandler) (func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.routes[session]; ok {
		return nil, fmt.Errorf("fake: %s's hooks are already routed", session)
	}
	r := &hubRoute{h: fn}
	h.routes[session] = r
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.routes[session] == r {
			delete(h.routes, session)
		}
	}, nil
}

// Command implements tuidriver.HookHub.
func (h *HookHub) Command(session string) []string {
	return []string{h.binary, "--hook-relay", h.l.Addr().String(), session}
}

// Close stops listening and waits for the hooks in flight.
func (h *HookHub) Close() error {
	h.cancel()
	err := h.l.Close()
	h.wg.Wait()
	return errors.Join(err, os.RemoveAll(h.dir))
}

func (h *HookHub) accept() {
	defer h.wg.Done()
	for {
		conn, err := h.l.Accept()
		if err != nil {
			return
		}
		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			defer func() { _ = conn.Close() }()
			h.serve(conn)
		}()
	}
}

func (h *HookHub) serve(conn net.Conn) {
	var req hubRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return
	}
	h.mu.Lock()
	r := h.routes[req.Session]
	h.mu.Unlock()
	var resp hubResponse
	if r == nil {
		resp.Err = "no conversation for session " + req.Session
	} else if reply, err := r.h(h.ctx, req.Event, req.Payload); err != nil {
		resp.Err = err.Error()
	} else {
		resp.Reply = reply
	}
	_ = json.NewEncoder(conn).Encode(resp)
}

// relayHook is `fakeharness --hook-relay <socket> <session> <event>`.
func relayHook(args []string, stdin io.Reader, stdout io.Writer) int {
	if len(args) != 3 {
		_, _ = fmt.Fprintln(os.Stderr, "usage: fakeharness --hook-relay <socket> <session> <event>")
		return 2
	}
	payload, err := io.ReadAll(stdin)
	if err != nil {
		return fail(err)
	}
	conn, err := net.Dial("unix", args[0])
	if err != nil {
		return fail(err)
	}
	defer func() { _ = conn.Close() }()
	if err := json.NewEncoder(conn).Encode(hubRequest{Session: args[1], Event: args[2], Payload: payload}); err != nil {
		return fail(err)
	}
	var resp hubResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return fail(err)
	}
	if resp.Err != "" {
		return fail(errors.New(resp.Err))
	}
	if _, err := stdout.Write(resp.Reply); err != nil {
		return fail(err)
	}
	return 0
}

func fail(err error) int {
	_, _ = fmt.Fprintln(os.Stderr, "fakeharness: hook relay:", err)
	return 1
}
