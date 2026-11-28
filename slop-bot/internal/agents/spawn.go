package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/lowaa/slop-factory/slop-bot/internal/agentspec"
	"github.com/lowaa/slop-factory/slop-bot/internal/briefing"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
	"github.com/lowaa/slop-factory/slop-bot/internal/worktree"
)

// errMisconfigured is a ticket whose agent spec can't run here.
var errMisconfigured = errors.New("agents: misconfigured")

// recoveryLimit is how many times in a row an agent whose harness died is
// resumed before slop-bot gives up on it: enough for a crash or two, not
// for a harness that dies on every start.
const recoveryLimit = 3

// spawn starts an agent for t, which the scheduler found eligible, with
// spec: t's own, resolved, or the fallback specFor chose. It runs
// on its own goroutine (goStart), under a context Stop cancels.
func (m *Manager) spawn(ctx context.Context, t *tickets.Ticket, pool state.Pool, spec tickets.AgentSpec) {
	if err := m.spawnAgent(ctx, t, pool, spec); err != nil && !errors.Is(err, errMisconfigured) {
		m.opts.Logger.Warn("agents: spawning", "ticket", t.ID, "err", err)
	}
}

func (m *Manager) spawnAgent(ctx context.Context, t *tickets.Ticket, pool state.Pool, spec tickets.AgentSpec) error {
	h, ok := m.opts.Harnesses.Harness(spec.Harness)
	if err := agentspec.Validate(spec, m.opts.Config); err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("%w: harness %q has no implementation in this build", agentspec.ErrInvalid, spec.Harness)
		}
		return m.misconfigure(ctx, t, err)
	}
	id, err := newAgentID(t.ID)
	if err != nil {
		return err
	}
	token, err := control.NewToken()
	if err != nil {
		return err
	}
	var wts []*worktree.Worktree
	if len(t.Repos) > 0 {
		if wts, err = m.opts.Worktrees.Create(ctx, t.ID, t.Repos, t.Repos[0]); err != nil {
			// What Create made before failing is nobody's yet: clean it up
			// rather than leave it for a cleanup that has no record of it.
			m.reportCleanup(ctx, "", t.ID, m.opts.Worktrees.Cleanup(context.WithoutCancel(ctx), t.ID, wts))
			return m.misconfigure(ctx, t, fmt.Errorf("creating worktrees: %w", err))
		}
	}
	if _, err := m.opts.State.Agents.Create(ctx, &state.NewAgent{
		// The configured name, not the implementation's: resume looks the
		// harness up by it.
		ID: id, Ticket: t.ID, Harness: spec.Harness, Driver: h.Driver(), Model: spec.Model, Effort: spec.Effort,
		Pool: pool, State: state.StateStarting, Reason: "spawned", TokenHash: control.HashToken(token),
		Worktrees: stateWorktrees(wts),
		// The kick-off says so.
		BlockedTold: len(t.BlockedBy) > 0,
	}); err != nil {
		return err
	}
	ws, err := m.workspace(id, t, spec, wts, token)
	if err != nil {
		m.failStart(ctx, id, t, err)
		return err
	}
	conv, err := h.Start(ctx, ws, nil)
	if err != nil {
		m.failStart(ctx, id, t, err)
		return fmt.Errorf("agents: starting %s for ticket %s: %w", h.Name(), t.ID, err)
	}
	r := m.adopt(ctx, &adoption{id: id, ticket: t.ID, harness: spec.Harness, pool: pool, conv: conv, caps: h.Capabilities(), st: state.StateStarting})
	m.event(ctx, EventSpawned, id, t.ID, map[string]string{"harness": spec.Harness, "driver": h.Driver()})
	if err := m.kickOff(ctx, r, t); err != nil {
		m.abortStart(ctx, r, t, err)
		return err
	}
	// In step with whatever the ticket did while the agent started, and an
	// open ticket's work starts now: no event may come to say so.
	m.sync(ctx, t.ID)
	return nil
}

// kickOff gives a new agent its ticket. It goes through the inbox like any
// message, so a TUI agent that is pointed there finds it; and it is sent
// at once, on the slot the start holds. A person already in the agent's
// TUI session holds it, as any message, for the next delivery.
func (m *Manager) kickOff(ctx context.Context, r *runner, t *tickets.Ticket) error {
	r.deliverMu.Lock()
	defer r.deliverMu.Unlock()
	kick, err := m.opts.State.Messages.Enqueue(ctx, &state.NewMessage{
		From: state.UserParty, To: state.AgentParty(r.id), Kind: state.KindSystem, Body: kickoff(t),
	})
	if err != nil {
		return err
	}
	err = m.send(ctx, r, []state.Message{*kick}, state.DeliveryTurn)
	if errors.Is(err, harness.ErrAttached) || errors.Is(err, harness.ErrUnsubmitted) {
		m.opts.Logger.Info("agents: the kick-off waits: a person is in the session", "agent", r.id, "err", err)
		if r.state() != state.StateAttached {
			r.setState(ctx, state.StateIdle, "a person is in its session")
		}
		return nil
	}
	return err
}

// misconfigure parks a ticket whose spec can't run, and tells the user how
// to fix it: never a crashed agent.
func (m *Manager) misconfigure(ctx context.Context, t *tickets.Ticket, cause error) error {
	ctx = context.WithoutCancel(ctx)
	st := tickets.StatusMisconfigured
	if _, err := m.opts.Tickets.Update(ctx, ident.UserPrincipal(), t.ID, &tickets.TicketChange{Status: &st}); err != nil {
		m.opts.Logger.Warn("agents: marking a ticket misconfigured", "ticket", t.ID, "err", err)
	}
	m.event(ctx, EventMisconfigure, "", t.ID, map[string]string{"error": cause.Error()})
	m.tellUser(ctx, "", fmt.Sprintf("ticket %s won't be scheduled: %v. Fix it with `slop-bot ticket set %s ...`, "+
		"then `slop-bot ticket set %s --status preparing`.", t.ID, cause, t.ID, t.ID))
	return fmt.Errorf("%w: ticket %s: %w", errMisconfigured, t.ID, cause)
}

// failStart ends an agent that never got going: it is released and its
// worktrees cleaned up, so the ticket isn't wedged behind a live agent
// nothing runs. A harness that won't start is a spec that can't run here,
// so the ticket is parked as misconfigured rather than retried in a loop;
// a start cut short by the daemon stopping is retried on the next start.
func (m *Manager) failStart(ctx context.Context, id ident.AgentID, t *tickets.Ticket, cause error) {
	stopped := m.base.Err() != nil
	ctx = context.WithoutCancel(ctx)
	m.event(ctx, EventSpawnFailed, id, t.ID, map[string]string{"error": cause.Error()})
	a, err := m.opts.State.Agents.Get(ctx, id)
	if err != nil {
		m.opts.Logger.Warn("agents: reading an agent that failed to start", "agent", id, "err", err)
		return
	}
	if stopped {
		m.endRecord(ctx, a, state.StateReleased, "the daemon stopped while it started")
		return
	}
	m.endRecord(ctx, a, state.StateReleased, "failed to start: "+cause.Error())
	_ = m.misconfigure(ctx, t, fmt.Errorf("its agent failed to start: %w", cause))
}

// abortStart is failStart for an agent whose harness started but couldn't
// be given its first message: the harness is stopped first.
func (m *Manager) abortStart(ctx context.Context, r *runner, t *tickets.Ticket, cause error) {
	r.mu.Lock()
	r.stopping, r.ending = true, true
	r.mu.Unlock()
	stopCtx := context.WithoutCancel(ctx)
	_ = r.conv.Stop(stopCtx, true)
	<-r.exited
	m.forget(r.id)
	m.failStart(ctx, r.id, t, cause)
}

// recover resumes an agent whose harness died, up to recoveryLimit times in
// a row; past that, or when the resume fails, slop-bot gives up on it.
func (m *Manager) recover(ctx context.Context, id ident.AgentID) {
	a, err := m.opts.State.Agents.Get(ctx, id)
	if err != nil || a.EndedAt != nil {
		return
	}
	t, err := m.opts.Tickets.Get(ctx, a.Ticket)
	if err == nil && t.Status.Terminal() {
		m.endRecord(ctx, a, state.StateDone, endReason(t.Status))
		return
	}
	n, err := m.opts.State.Agents.AddRecovery(ctx, id)
	if err != nil {
		m.opts.Logger.Warn("agents: counting a recovery", "agent", id, "err", err)
		return
	}
	if n > recoveryLimit {
		m.giveUp(ctx, a, fmt.Sprintf("its harness died %d times in a row", n))
		return
	}
	if err := m.resume(ctx, a, recoveredNote); err != nil {
		if m.base.Err() != nil {
			return // the daemon is stopping: the next start tries again
		}
		m.giveUp(ctx, a, fmt.Sprintf("it couldn't be resumed: %v", err))
	}
}

// giveUp releases an agent slop-bot can't run any more, cleaning up its
// worktrees (any holding work are kept, and the user told), and parks its
// ticket so a new agent isn't started straight into the same failure. The
// user decides what next.
func (m *Manager) giveUp(ctx context.Context, a *state.Agent, why string) {
	ctx = context.WithoutCancel(ctx)
	m.endRecord(ctx, a, state.StateReleased, why)
	t, err := m.opts.Tickets.Get(ctx, a.Ticket)
	if err != nil {
		m.opts.Logger.Warn("agents: reading the ticket of an agent given up on", "agent", a.ID, "err", err)
		return
	}
	if t.Status == tickets.StatusPreparing || t.Status == tickets.StatusOpen {
		_ = m.misconfigure(ctx, t, fmt.Errorf("its agent %s was released: %s", a.ID, why))
		return
	}
	m.tellUser(ctx, a.ID, fmt.Sprintf("agent %s (ticket %s) was released: %s. Ticket %s is %s with no agent: "+
		"`slop-bot ticket set %s --status open` starts a new one.", a.ID, t.ID, why, t.ID, t.Status, t.ID))
}

// endRecord ends an agent that has no runner, from its record: it moves to
// st (an ending state), its worktrees are cleaned up and its slot queue
// entry goes.
func (m *Manager) endRecord(ctx context.Context, a *state.Agent, st state.AgentState, reason string) {
	ctx = context.WithoutCancel(ctx)
	m.cleanup(ctx, a.ID, a.Ticket)
	if _, err := m.opts.State.Agents.SetState(ctx, &state.Transition{Agent: a.ID, To: st, Reason: reason, GivenUp: true}); err != nil {
		m.opts.Logger.Warn("agents: ending an agent", "agent", a.ID, "err", err)
	}
	m.retain(ctx, a.ID)
	typ := EventFinished
	if st == state.StateReleased {
		typ = EventReleased
	}
	m.event(ctx, typ, a.ID, a.Ticket, map[string]string{"reason": reason})
	m.forget(a.ID)
}

// adoption is a conversation the Manager is taking on, new or resumed.
type adoption struct {
	id     ident.AgentID
	ticket ident.TicketID
	// harness is the configured name, whose plan limits the agent reports.
	harness string
	pool    state.Pool
	conv    harness.Conversation
	caps    harness.Capabilities
	st      state.AgentState
	// decl and summary carry what the agent declared from inside a TUI
	// across the hand-back.
	decl      declared
	summary   string
	holdsSlot bool
}

func (m *Manager) adopt(ctx context.Context, a *adoption) *runner {
	id := a.id
	conv := a.conv
	r := &runner{m: m, id: id, ticket: a.ticket, harness: a.harness, pool: a.pool, conv: conv, caps: a.caps, st: a.st,
		decl: a.decl, summary: a.summary, holdsSlot: a.holdsSlot, exited: make(chan struct{})}
	m.mu.Lock()
	r.stopping = m.stopping
	m.runners[id] = r
	m.mu.Unlock()
	if err := m.opts.State.Agents.SetConversationID(ctx, id, conv.Ref().ID); err != nil {
		m.opts.Logger.Warn("agents: recording the conversation id", "agent", id, "err", err)
	}
	go r.run(context.WithoutCancel(ctx))
	return r
}

// workspace is everything the harness needs: the primary worktree as cwd,
// the briefing rendered outside the worktree, and the agent's token.
func (m *Manager) workspace(id ident.AgentID, t *tickets.Ticket, spec tickets.AgentSpec, wts []*worktree.Worktree, token string) (*harness.Workspace, error) {
	dir := m.opts.Paths.AgentDir(string(id))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	data := &briefing.Data{
		Ticket:             briefing.Ticket{ID: string(t.ID), Title: t.Title, AutoMerge: t.AutoMerge},
		CLI:                m.opts.CLI,
		Nudges:             m.opts.Config.Agents.Nudges,
		Autonomy:           m.autonomy(context.Background()),
		AgentsMaySetUrgent: *m.opts.Config.AgentsMaySetUrgent,
		CLIReference:       m.opts.CLIReference,
	}
	for _, r := range m.opts.Config.Repos {
		data.Repos = append(data.Repos, briefing.Repo{Name: r.Name, Path: r.Path})
	}
	ws := &harness.Workspace{
		Agent: id, Ticket: t.ID, Dir: dir, Model: spec.Model, Effort: spec.Effort,
		Env: map[string]string{control.TokenEnv: token},
	}
	if hc := m.opts.Config.Harnesses[spec.Harness]; hc != nil {
		ws.Permissive = *hc.Permissive
		ws.APIKeyOptIn = hc.APIKeyOptIn
	}
	for _, w := range wts {
		data.Worktrees = append(data.Worktrees, briefing.Worktree{Repo: w.Repo, Path: w.Path, Branch: w.Branch, Primary: w.Primary})
		if w.Primary {
			ws.Dir = w.Path
		} else {
			ws.ExtraDirs = append(ws.ExtraDirs, w.Path)
		}
	}
	path, text, err := briefing.Write(dir, data, m.opts.Config.BriefingPath)
	if err != nil {
		return nil, err
	}
	ws.Briefing, ws.BriefingFile = text, path
	return ws, nil
}

func kickoff(t *tickets.Ticket) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You've been assigned ticket %s: %q.\n\n", t.ID, t.Title)
	if t.Body != "" {
		fmt.Fprintf(&b, "%s\n\n", strings.TrimSpace(t.Body))
	}
	switch {
	case t.Status == tickets.StatusPreparing && len(t.BlockedBy) > 0:
		fmt.Fprintf(&b, "It is being prepared, and is blocked by %s. Refine it now; don't start the work "+
			"until slop-bot tells you its blockers are done.", refs(t.BlockedBy))
	case t.Status == tickets.StatusPreparing:
		b.WriteString("It is being prepared: refine it first (see your briefing), then do the work.")
	default:
		b.WriteString("It is open: do the work (see your briefing).")
	}
	return b.String()
}

func refs(rs []tickets.TicketRef) string {
	ids := make([]string, len(rs))
	for i, r := range rs {
		ids[i] = string(r.ID)
	}
	return strings.Join(ids, ", ")
}

// worktreesOf rebuilds an agent's recorded worktrees. state.db keeps the
// repo's name, not its path: the path comes from this machine's repo
// lookup, which the worktree manager needs to run git in the repo.
func (m *Manager) worktreesOf(a *state.Agent) []*worktree.Worktree {
	out := make([]*worktree.Worktree, 0, len(a.Worktrees))
	for _, w := range a.Worktrees {
		wt := &worktree.Worktree{Ticket: a.Ticket, Repo: w.Repo, Path: w.Path, Branch: w.Branch, Primary: w.Primary}
		if r, ok := m.opts.Config.Repo(w.Repo); ok {
			wt.RepoPath = r.Path
		}
		out = append(out, wt)
	}
	return out
}

func stateWorktrees(wts []*worktree.Worktree) []state.Worktree {
	out := make([]state.Worktree, 0, len(wts))
	for _, w := range wts {
		out = append(out, state.Worktree{Repo: w.Repo, Path: w.Path, Branch: w.Branch, Primary: w.Primary})
	}
	return out
}

// What a resumed agent is told, by why it was resumed.
const (
	restartedNote = "slop-bot restarted, so your last turn may have been cut short. Check where you got to, then carry on."
	recoveredNote = "Your session ended unexpectedly and slop-bot resumed it, so your last turn may have been cut short. " +
		"Check where you got to, then carry on."
)

// resumeAll restarts, by conversation id, every agent that was running when
// the daemon last stopped, each on its own goroutine. An agent whose ticket
// finished meanwhile is ended instead; one whose harness had died is
// recovered; one that never got a conversation is released, and its ticket
// gets a new agent.
func (m *Manager) resumeAll(ctx context.Context) error {
	live, err := m.opts.State.Agents.ListLive(ctx)
	if err != nil {
		return err
	}
	for _, a := range live {
		if a.Mode == state.ModeAttached {
			m.adoptAttached(ctx, a)
			continue
		}
		t, err := m.opts.Tickets.Get(ctx, a.Ticket)
		switch {
		case err == nil && t.Status.Terminal():
			m.endForTicket(ctx, a, nil, t.Status)
		case a.ConversationID == "":
			m.endRecord(ctx, a, state.StateReleased, "it never started a conversation to resume")
		case a.State == state.StateCrashed || a.State == state.StateExited:
			m.goStart(a.Ticket, a.Pool, false, func(ctx context.Context) { m.recover(ctx, a.ID) })
		default:
			m.goStart(a.Ticket, a.Pool, false, func(ctx context.Context) {
				if err := m.resume(ctx, a, restartedNote); err != nil && m.base.Err() == nil {
					m.opts.Logger.Warn("agents: resuming", "agent", a.ID, "err", err)
					m.giveUp(ctx, a, fmt.Sprintf("it couldn't be resumed after a restart: %v", err))
				}
			})
		}
	}
	return nil
}

// adoptAttached takes on an agent a person had attached when the daemon
// last stopped. It has no conversation of ours: it holds its slot and
// takes its verbs from the TUI until its CLI reconnects and hands it back,
// or its lease expires.
func (m *Manager) adoptAttached(ctx context.Context, a *state.Agent) {
	if a.State != state.StateAttached {
		// The daemon stopped between taking the lease and moving the state.
		if _, err := m.opts.State.Agents.SetState(ctx, &state.Transition{Agent: a.ID, To: state.StateAttached, Reason: "attached at restart"}); err != nil {
			m.opts.Logger.Warn("agents: recording an attach", "agent", a.ID, "err", err)
		}
	}
	r := &runner{m: m, id: a.ID, ticket: a.Ticket, harness: a.Harness, pool: a.Pool, st: state.StateAttached,
		decl: declared(a.Declared), summary: a.DeclaredSummary, exited: make(chan struct{})}
	close(r.exited)
	m.mu.Lock()
	m.runners[a.ID] = r
	m.mu.Unlock()
}

// resume restarts a's conversation by id. A waiting agent stays waiting;
// one that was mid-work, or whose harness died, is told why its last turn
// may have been cut short. Messages that were waiting for it go now.
func (m *Manager) resume(ctx context.Context, a *state.Agent, note string) error {
	if a.State == state.StateAttached {
		// Attached but holding no lease: the daemon stopped mid hand-back.
		// It resumes like any agent cut off mid-work.
		if _, err := m.opts.State.Agents.SetState(ctx, &state.Transition{Agent: a.ID, To: state.StateStarting, Reason: "an interrupted hand-back"}); err != nil {
			return err
		}
		a.State = state.StateStarting
	}
	conv, caps, err := m.startConversation(ctx, a)
	if err != nil {
		return err
	}
	r := m.adopt(ctx, &adoption{id: a.ID, ticket: a.Ticket, harness: a.Harness, pool: a.Pool, conv: conv, caps: caps, st: a.State})
	st, cutShort := resumedState(a.State)
	r.setState(ctx, st, "resumed")
	m.event(ctx, EventResumed, a.ID, a.Ticket, nil)
	if cutShort || note == recoveredNote {
		r.m.enqueueNotice(ctx, r, note)
	}
	m.sync(ctx, a.Ticket)
	// Whatever was queued before the restart: an answer waiting for a slot,
	// a nudge. The slot queue lived in memory, so this rebuilds it.
	m.deliverPending(ctx, r)
	return nil
}

// resumedState is the state a resumed agent starts in, and whether its
// last turn may have been cut short. A waiting agent goes on waiting; a
// dead one is idle until it is told it was resumed.
func resumedState(was state.AgentState) (state.AgentState, bool) {
	switch was {
	case state.StateWaitingUser, state.StateDiscussion, state.StateWaitingMerge, state.StateIdle:
		return was, false
	case state.StateCrashed, state.StateExited:
		return state.StateIdle, true
	}
	return was, true
}

// startConversation resumes a's conversation by id, with a fresh token:
// state.db holds only token hashes, so the old one can't be handed on.
func (m *Manager) startConversation(ctx context.Context, a *state.Agent) (harness.Conversation, harness.Capabilities, error) {
	h, ok := m.opts.Harnesses.Harness(a.Harness)
	if !ok {
		return nil, harness.Capabilities{}, fmt.Errorf("harness %q isn't available", a.Harness)
	}
	t, err := m.opts.Tickets.Get(ctx, a.Ticket)
	if err != nil {
		return nil, harness.Capabilities{}, err
	}
	token, err := m.rotateToken(ctx, a.ID)
	if err != nil {
		return nil, harness.Capabilities{}, err
	}
	ws, err := m.workspace(a.ID, t, tickets.AgentSpec{Harness: a.Harness, Model: a.Model, Effort: a.Effort}, m.worktreesOf(a), token)
	if err != nil {
		return nil, harness.Capabilities{}, err
	}
	conv, err := h.Start(ctx, ws, &harness.ConversationRef{Harness: a.Harness, Driver: a.Driver, ID: a.ConversationID})
	if err != nil {
		return nil, harness.Capabilities{}, err
	}
	return conv, h.Capabilities(), nil
}

// rotateToken gives the agent a new token, retiring the old one.
func (m *Manager) rotateToken(ctx context.Context, id ident.AgentID) (string, error) {
	token, err := control.NewToken()
	if err != nil {
		return "", err
	}
	if err := m.opts.State.Agents.RotateTokenHash(ctx, id, control.HashToken(token)); err != nil {
		return "", err
	}
	return token, nil
}
