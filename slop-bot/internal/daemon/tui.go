package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/tuidriver"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/lifecycle"
)

// Hook relay errors.
var (
	// errNoHookRoute is a hook for a session no conversation drives: one
	// that outlived its daemon, before the next adopts it.
	errNoHookRoute = errors.New("daemon: no conversation for the session")
	// errNotYourSession is a hook relayed by an agent for another's session.
	errNotYourSession = errors.New("daemon: the session isn't the calling agent's")
	errHubClosed      = errors.New("daemon: the hook hub has stopped")
)

var hookErrors = append([]control.ErrorMapping{
	{Err: errNoHookRoute, Status: http.StatusNotFound},
	{Err: errNotYourSession, Status: http.StatusForbidden},
	{Err: errHubClosed, Status: http.StatusServiceUnavailable},
}, agentErrors...)

// hookHub is the daemon's tuidriver.HookHub: a session's hooks run
// `slop-bot hook tui <session> <event>`, which relays them over the control
// socket (PathHookTUI) to the conversation routed for that session.
type hookHub struct {
	cli string

	mu     sync.Mutex
	closed bool
	routes map[string]*hookRoute
}

type hookRoute struct{ h tuidriver.HookHandler }

func newHookHub(cli string) *hookHub {
	return &hookHub{cli: cli, routes: map[string]*hookRoute{}}
}

// Route implements tuidriver.HookHub.
func (h *hookHub) Route(session string, fn tuidriver.HookHandler) (func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, errHubClosed
	}
	if _, ok := h.routes[session]; ok {
		return nil, fmt.Errorf("daemon: %s's hooks are already routed", session)
	}
	r := &hookRoute{h: fn}
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
func (h *hookHub) Command(session string) []string {
	return []string{h.cli, "hook", "tui", session}
}

// relay hands one hook to its session's conversation.
func (h *hookHub) relay(ctx context.Context, in *api.HookTUI) ([]byte, error) {
	h.mu.Lock()
	r, closed := h.routes[in.Session], h.closed
	h.mu.Unlock()
	switch {
	case closed:
		return nil, errHubClosed
	case r == nil:
		return nil, fmt.Errorf("%w %s", errNoHookRoute, in.Session)
	}
	return r.h(ctx, in.Event, in.Payload)
}

// close refuses hooks from here on: the daemon is stopping, and the hook's
// own fallback answers for it.
func (h *hookHub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
}

// hookTUI is POST PathHookTUI: an agent's harness's hook.
func (d *Daemon) hookTUI(w http.ResponseWriter, r *http.Request) {
	by := control.PrincipalFrom(r.Context())
	if by.IsUser() || !by.Valid() {
		control.WriteError(w, http.StatusForbidden, errors.New("daemon: hooks are relayed with the agent's token"))
		return
	}
	var in api.HookTUI
	if !decode(w, r, &in) {
		return
	}
	// Not a security boundary (see control.Authenticate): it keeps a
	// confused relay from driving another agent's conversation.
	if !tuidriver.OwnsSession(in.Session, by.Agent) {
		control.WriteError(w, control.StatusFor(errNotYourSession, hookErrors), fmt.Errorf("%w: %s", errNotYourSession, in.Session))
		return
	}
	if d.hub == nil {
		control.WriteError(w, control.StatusFor(errNoHookRoute, hookErrors), errNoHookRoute)
		return
	}
	reply, err := d.hub.relay(r.Context(), &in)
	if err != nil {
		control.WriteError(w, control.StatusFor(err, hookErrors), err)
		return
	}
	control.WriteJSON(w, http.StatusOK, api.HookTUIReply{Reply: reply})
}

// tuiKit is what the TUI harnesses share: slop-bot's own tmux server and
// the hook hub. Nil when no harness runs under the tui driver.
func (d *Daemon) tuiKit(cliPath string) (*tuidriver.Sessions, error) {
	socket := d.opts.TmuxSocket
	if socket == "" {
		socket = sessionhost.DefaultSocket
	}
	host, err := sessionhost.NewTmux(&sessionhost.TmuxOptions{
		Socket: socket, Scope: sessionhost.SystemdScope(socket), Logger: d.opts.Logger,
	})
	if err != nil {
		return nil, err
	}
	d.host, d.hub = host, newHookHub(cliPath)
	return &tuidriver.Sessions{
		Host: host, Hub: d.hub, Logger: d.opts.Logger, Clock: d.opts.Clock,
		// The manager exists by the time a conversation types a pointer.
		Pointer: func(msgs []*harness.Message) string { return d.agents.Pointer(msgs) },
	}, nil
}

// tuiSessions is the TUI kit's lifecycle. The tmux server is never ended
// with the daemon: its sessions outlive it, for the next one to adopt (it
// runs in a scope of its own under systemd, see sessionhost.SystemdScope).
// Stopped once the agents have let go of their sessions, it refuses hooks,
// which then answer for themselves.
func (d *Daemon) tuiSessions() lifecycle.Component {
	return &lifecycle.Func{
		N:         "tui sessions",
		StartFunc: func(context.Context) error { return nil },
		StopFunc: func(context.Context) error {
			if d.hub != nil {
				d.hub.close()
			}
			return nil
		},
	}
}

// orphanedSessions ends, once the agents have started and taken back their
// sessions, the sessions none of them owns (endOrphans).
func (d *Daemon) orphanedSessions() lifecycle.Component {
	return &lifecycle.Func{
		N: "orphaned tui sessions",
		StartFunc: func(ctx context.Context) error {
			if d.host != nil {
				d.endOrphans(ctx)
			}
			return nil
		},
	}
}

// endOrphans kills the sessions on slop-bot's server whose agents have
// ended (their ticket ended while the daemon was away, say): left running,
// they would work on with nobody to hear them. It runs once the agents
// have taken back theirs.
func (d *Daemon) endOrphans(ctx context.Context) {
	names, err := d.host.List(ctx)
	if err != nil {
		d.opts.Logger.Warn("daemon: listing tmux sessions", "err", err)
		return
	}
	for _, name := range names {
		// Only an agent this daemon knows, and has ended: another slop-bot
		// (its own state dir, the same default socket) owns the rest.
		_, id, ok := strings.Cut(name, "-")
		if !ok {
			continue
		}
		a, err := d.state.Agents.Get(ctx, ident.AgentID(id))
		if err != nil || a.EndedAt == nil || !tuidriver.OwnsSession(name, a.ID) {
			continue
		}
		d.opts.Logger.Info("daemon: ending the session of an agent that has ended", "session", name, "agent", a.ID)
		if err := d.host.Kill(ctx, name); err != nil && !errors.Is(err, sessionhost.ErrNoSession) {
			d.opts.Logger.Warn("daemon: ending an orphaned session", "session", name, "err", err)
		}
	}
}
