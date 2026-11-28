package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

var attachErrors = append([]control.ErrorMapping{
	{Err: agents.ErrNotAttached, Status: http.StatusConflict},
	{Err: agents.ErrNoHandOver, Status: http.StatusConflict},
	{Err: state.ErrConflict, Status: http.StatusConflict},
	{Err: state.ErrEnded, Status: http.StatusConflict},
}, agentErrors...)

func attachFail(w http.ResponseWriter, err error) {
	control.WriteError(w, control.StatusFor(err, attachErrors), err)
}

// DefaultTUIGrace is how long a hung-up TUI gets to exit on SIGHUP before
// it is killed: the conversation mustn't resume while it may still write
// to it.
const DefaultTUIGrace = 5 * time.Second

// attachRoutes is attach, its hand-back and detach. The attaching CLI's
// stream is the lease: while it is open the daemon renews the lease, and
// when it drops without a hand-back, the person's terminal went away.
type attachRoutes struct {
	d *Daemon

	mu       sync.Mutex
	sessions map[ident.AgentID]*attachSession
}

// attachSession is one attached CLI's open stream.
type attachSession struct {
	holder string
	detach chan struct{}
	// done is closed once the attach has ended, with outcome and err set.
	done chan struct{}

	mu          sync.Mutex
	pid         int
	detachOnce  sync.Once
	ended       bool
	outcome     agents.HandBackOutcome
	err         error
	handingBack bool
}

// claim marks the session as being ended by the caller, reporting false
// when something else (a hand-back, a hang-up) already is.
func (s *attachSession) claim() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handingBack {
		return false
	}
	s.handingBack = true
	return true
}

func (s *attachSession) finish(out agents.HandBackOutcome, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	s.ended, s.outcome, s.err = true, out, err
	close(s.done)
}

func (ar *attachRoutes) register(s *control.Server) {
	s.HandleFunc("POST "+api.PathAttach, ar.attach)
	s.HandleFunc("POST "+api.PathLaunched, ar.launched)
	s.HandleFunc("POST "+api.PathHandBack, ar.handBack)
	s.HandleFunc("POST "+api.PathDetach, ar.detach)
}

func (ar *attachRoutes) open(id ident.AgentID, holder string) *attachSession {
	s := &attachSession{holder: holder, detach: make(chan struct{}), done: make(chan struct{})}
	ar.mu.Lock()
	defer ar.mu.Unlock()
	if ar.sessions == nil {
		ar.sessions = map[ident.AgentID]*attachSession{}
	}
	ar.sessions[id] = s
	return s
}

func (ar *attachRoutes) close(id ident.AgentID, s *attachSession) {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	if ar.sessions[id] == s {
		delete(ar.sessions, id)
	}
}

// session is the agent's open attach stream, if holder ("" for any) has it.
func (ar *attachRoutes) session(id ident.AgentID, holder string) *attachSession {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	s := ar.sessions[id]
	if s == nil || (holder != "" && s.holder != holder) {
		return nil
	}
	return s
}

func requireUser(w http.ResponseWriter, r *http.Request) bool {
	if !control.PrincipalFrom(r.Context()).IsUser() {
		attachFail(w, fmt.Errorf("%w: attach is the user's", agents.ErrUserOnly))
		return false
	}
	return true
}

func (ar *attachRoutes) attach(w http.ResponseWriter, r *http.Request) {
	if !requireUser(w, r) {
		return
	}
	var in api.AttachRequest
	if !decode(w, r, &in) {
		return
	}
	if in.Holder == "" {
		control.WriteError(w, http.StatusBadRequest, errors.New("attach needs a holder"))
		return
	}
	m := ar.d.agents
	var att *agents.Attachment
	var err error
	if in.Reattach {
		att, err = m.Reattach(r.Context(), r.PathValue("agent"), in.Holder)
	} else {
		att, err = m.Attach(r.Context(), &agents.AttachRequest{Target: r.PathValue("agent"), Holder: in.Holder, Now: in.Now, NoResume: in.NoResume})
	}
	if err != nil {
		attachFail(w, err)
		return
	}
	id := att.Agent.ID
	var sess *attachSession
	if !att.Released {
		// Registered before the first frame, so a hand-back the CLI sends
		// as soon as it reads it finds the session.
		sess = ar.open(id, in.Holder)
		defer ar.close(id, sess)
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	flusher, _ := w.(http.Flusher)
	send := func(f *api.AttachFrame) {
		if enc.Encode(f) == nil && flusher != nil {
			flusher.Flush()
		}
	}
	send(&api.AttachFrame{Type: api.FrameAttached, Attach: apiAttachment(att)})
	if sess == nil {
		return
	}
	ar.hold(r, sess, id, send)
}

// hold keeps the attach for as long as the CLI's stream is open, renewing
// its lease, until it hands back, is detached, or hangs up.
func (ar *attachRoutes) hold(r *http.Request, sess *attachSession, id ident.AgentID, send func(*api.AttachFrame)) {
	ctx := r.Context()
	defer ar.d.agents.StreamOpen(id)()
	tick := ar.d.opts.Clock.NewTicker(max(ar.d.opts.Config.Agents.AttachLease.D()/3, time.Millisecond))
	defer tick.Stop()
	detach := sess.detach
	for {
		select {
		case <-tick.C():
			if !ar.heartbeat(ctx, sess, id, send) {
				return
			}
		case <-detach:
			detach = nil
			send(&api.AttachFrame{Type: api.FrameDetach})
		case <-sess.done:
			return
		case <-ctx.Done():
			select {
			case <-ar.d.server.Stopping():
				// The daemon is stopping, not the CLI: the lease stays for
				// the CLI to reconnect to, or to expire.
			default:
				ar.hungUp(ctx, sess, id)
			}
			return
		}
	}
}

// heartbeat renews the lease, and reports whether the attach goes on.
func (ar *attachRoutes) heartbeat(ctx context.Context, sess *attachSession, id ident.AgentID, send func(*api.AttachFrame)) bool {
	err := ar.d.agents.Heartbeat(ctx, id, sess.holder)
	switch {
	case err == nil:
		send(&api.AttachFrame{Type: api.FrameHeartbeat})
		return true
	case ctx.Err() != nil:
		return true // the stream's end handles it
	case !attachOver(err):
		// A busy database, say: the next tick retries, and the lease
		// outlasts a few missed renewals. Ending here would kill the
		// person's TUI and leave the agent attached until the lease ran out.
		ar.d.opts.Logger.Warn("daemon: renewing an attach lease", "agent", id, "err", err)
		return true
	case !sess.claim():
		// A hand-back released the lease and is resuming the agent: its
		// outcome is the session's, not this.
		<-sess.done
		return false
	}
	send(&api.AttachFrame{Type: api.FrameEnded, Reason: err.Error()})
	sess.finish(agents.Ended, nil)
	return false
}

// attachOver reports whether a failed renewal means the attach is over:
// it was handed back, or the agent has ended. Anything else is retried.
func attachOver(err error) bool {
	return errors.Is(err, agents.ErrNotAttached) || errors.Is(err, state.ErrEnded) || errors.Is(err, state.ErrNotFound)
}

// hungUp hands back an attach whose CLI went away without a word.
func (ar *attachRoutes) hungUp(ctx context.Context, sess *attachSession, id ident.AgentID) {
	if !sess.claim() {
		<-sess.done
		return
	}
	sess.mu.Lock()
	pid := sess.pid
	sess.mu.Unlock()
	ar.endTUI(pid)
	out, err := ar.d.agents.HandBack(context.WithoutCancel(ctx), id, sess.holder, "the attached terminal hung up")
	if err != nil {
		ar.d.opts.Logger.Warn("daemon: handing back after a hang-up", "agent", id, "err", err)
	}
	sess.finish(out, err)
}

// endTUI makes sure a TUI whose CLI went away has exited, so the
// conversation isn't resumed while it is still open in a terminal. A
// terminal closing sends the TUI SIGHUP itself; this covers a CLI that was
// killed outright, leaving its TUI behind.
func (ar *attachRoutes) endTUI(pid int) {
	if pid <= 1 || pid == os.Getpid() || !alive(pid) {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGHUP)
	// It isn't our child, so there's no wait: poll for it to go.
	grace := ar.d.opts.TUIGrace
	if grace == 0 {
		grace = DefaultTUIGrace
	}
	deadline := ar.d.opts.Clock.After(grace)
	for alive(pid) {
		select {
		case <-deadline:
			_ = syscall.Kill(pid, syscall.SIGKILL)
			return
		case <-ar.d.opts.Clock.After(20 * time.Millisecond):
		}
	}
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func (ar *attachRoutes) launched(w http.ResponseWriter, r *http.Request) {
	if !requireUser(w, r) {
		return
	}
	var in api.Launched
	if !decode(w, r, &in) {
		return
	}
	id := ident.AgentID(r.PathValue("agent"))
	sess := ar.session(id, in.Holder)
	if sess == nil {
		attachFail(w, fmt.Errorf("%w: no attach of %s by %s", agents.ErrNotAttached, id, in.Holder))
		return
	}
	sess.mu.Lock()
	sess.pid = in.PID
	sess.mu.Unlock()
	ar.d.agents.Record(r.Context(), agents.EventTUILaunched, id, map[string]int{"pid": in.PID})
	ok(w)
}

func (ar *attachRoutes) handBack(w http.ResponseWriter, r *http.Request) {
	if !requireUser(w, r) {
		return
	}
	var in api.HandBack
	if !decode(w, r, &in) {
		return
	}
	id := ident.AgentID(r.PathValue("agent"))
	sess := ar.session(id, in.Holder)
	if sess != nil && !sess.claim() {
		// Already being handed back (a hang-up raced it): report that.
		<-sess.done
		ar.writeOutcome(w, sess.outcome, sess.err)
		return
	}
	out, err := ar.d.agents.HandBack(context.WithoutCancel(r.Context()), id, in.Holder, "the person quit the TUI")
	if sess != nil {
		sess.finish(out, err)
	}
	ar.writeOutcome(w, out, err)
}

func (ar *attachRoutes) writeOutcome(w http.ResponseWriter, out agents.HandBackOutcome, err error) {
	if err != nil {
		attachFail(w, err)
		return
	}
	control.WriteJSON(w, http.StatusOK, api.HandedBack{Outcome: string(out)})
}

// detach forces a hand-back from another terminal: the attached CLI is told
// to end its TUI, and this waits for its hand-back. With no CLI connected
// (after a daemon restart, before it reconnects) it hands back directly.
func (ar *attachRoutes) detach(w http.ResponseWriter, r *http.Request) {
	if !requireUser(w, r) {
		return
	}
	a, err := ar.d.agents.LiveAgent(r.Context(), r.PathValue("agent"))
	if err != nil {
		attachFail(w, err)
		return
	}
	if a.Lease == nil {
		attachFail(w, fmt.Errorf("%w: agent %s isn't attached", agents.ErrNotAttached, a.ID))
		return
	}
	if sess := ar.session(a.ID, ""); sess != nil {
		sess.detachOnce.Do(func() { close(sess.detach) })
		select {
		case <-sess.done:
			ar.writeOutcome(w, sess.outcome, sess.err)
		case <-r.Context().Done():
		}
		return
	}
	out, err := ar.d.agents.HandBack(context.WithoutCancel(r.Context()), a.ID, a.Lease.Holder, "detached")
	ar.writeOutcome(w, out, err)
}

func apiAttachment(att *agents.Attachment) *api.Attachment {
	out := &api.Attachment{
		Agent: string(att.Agent.ID), Ticket: string(att.Agent.Ticket), Title: att.Title,
		State: string(att.Agent.State), Note: att.Agent.ProgressNote, Held: apiMessages(att.Held), Released: att.Released,
		Join: att.Join,
	}
	if c := att.Command; c != nil {
		out.Command = &api.Command{Path: c.Path, Args: c.Args, Env: c.Env, Dir: c.Dir}
	}
	return out
}
