package agents

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

// Attach errors.
var (
	// ErrNotAttached is a hand-back, heartbeat or reconnect from a holder
	// whose attach has already been handed back.
	ErrNotAttached = errors.New("agents: the agent isn't attached by you")
	// ErrNoHandOver is a conversation whose harness has no TUI to hand over to.
	ErrNoHandOver = errors.New("agents: this agent can't be handed over to a TUI")
)

// handBackNote is what the agent reads first after a person had its
// conversation.
const handBackNote = "A person worked with you directly in your session. Continue the ticket from here."

// AttachRequest is `slop-bot attach`.
type AttachRequest struct {
	// Target is an agent id, or a ticket id for its live agent.
	Target string
	// Holder names the attached CLI: it is who the lease is held by, and
	// what a second attach is told.
	Holder string
	// Now interrupts the running turn instead of waiting for it to end.
	Now bool
	// NoResume releases the agent to the person: slop-bot stops running it.
	NoResume bool
}

// Attachment is what the attaching person needs.
type Attachment struct {
	// Agent is the agent as it was before the attach: its state and note.
	Agent *state.Agent
	Title string
	// Held are the messages waiting for the hand-back.
	Held []state.Message
	// Command runs the harness's TUI on the conversation, with a fresh agent
	// token so `slop-bot ask/done` work from inside it. Nil on a reattach.
	Command *harness.LaunchSpec
	// Released is set for NoResume: there is no hand-back.
	Released bool
	// Join says the person joins the agent's running session (Command is
	// a tmux attach): detaching hands it back, and quitting the harness
	// ends the session, which slop-bot then resumes.
	Join bool
}

// HandBackOutcome is what became of an agent when its attach ended.
type HandBackOutcome string

// Hand-back outcomes.
const (
	// Resumed: slop-bot is running the conversation again.
	Resumed HandBackOutcome = "resumed"
	// Finished: the agent called done from the TUI.
	Finished HandBackOutcome = "finished"
	// Ended: the agent ended during the attach (its ticket was cancelled).
	Ended HandBackOutcome = "ended"
)

// Event types for attach.
const (
	// EventAttaching: the attach is waiting for the agent's turn to settle.
	EventAttaching  = "agent.attaching"
	EventAttached   = "agent.attached"
	EventReattached = "agent.reattached"
	EventHandedBack = "agent.handed_back"
	// EventTUILaunched: the attached CLI started the TUI.
	EventTUILaunched = "agent.tui_launched"
	// EventJoinedExit: the harness exited in a session a person had joined.
	EventJoinedExit = "agent.joined_exit"
)

// Record logs an attach event the daemon's routes observe, on the agent's
// ticket.
func (m *Manager) Record(ctx context.Context, typ string, id ident.AgentID, payload any) {
	ticket := ident.TicketID("")
	if r, ok := m.runner(id); ok {
		ticket = r.ticket
	}
	m.event(ctx, typ, id, ticket, payload)
}

func (m *Manager) lease(holder string) *state.Lease {
	return &state.Lease{Holder: holder, ExpiresAt: m.opts.Clock.Now().Add(m.opts.Config.Agents.AttachLease.D())}
}

// Attach takes an agent's conversation over for a person: it stops the
// conversation once its turn settles (or interrupts it first), takes the
// lease, and returns the TUI command that resumes it. The agent keeps its
// slot until it's handed back.
//
// The lease is taken only once the conversation has stopped: until then the
// runner's attaching flag refuses a second attach and holds the slot, so a
// long turn can't outlast a lease nothing is renewing yet, and the expiry
// sweep and detach have nothing to act on.
func (m *Manager) Attach(ctx context.Context, req *AttachRequest) (*Attachment, error) {
	a, err := m.resolveAgent(ctx, req.Target)
	if err != nil {
		return nil, err
	}
	// One attach per agent, even for the same holder: a reconnect is Reattach.
	if a.Lease != nil {
		return nil, &state.LeaseHeldError{Holder: a.Lease.Holder, ExpiresAt: a.Lease.ExpiresAt}
	}
	r, ok := m.runner(a.ID)
	if !ok {
		return nil, fmt.Errorf("%w: agent %s isn't running", ErrNoLiveAgent, a.ID)
	}
	plan, err := m.stopForAttach(ctx, r, a, req)
	if err != nil {
		return nil, err
	}
	// From here the work outlives the request: the conversation is stopped,
	// and must end attached or resumed, not half-way.
	ctx = context.WithoutCancel(ctx)
	if err := m.opts.State.Agents.AcquireLease(ctx, a.ID, m.lease(req.Holder)); err != nil {
		m.abandonAttach(ctx, r, "the attach couldn't take its lease: "+err.Error())
		return nil, err
	}
	return m.takeOver(ctx, r, a, plan, req)
}

func (m *Manager) takeOver(ctx context.Context, r *runner, a *state.Agent, plan *harness.AttachPlan, req *AttachRequest) (*Attachment, error) {
	att := &Attachment{Agent: a}
	if t, err := m.opts.Tickets.Get(ctx, a.Ticket); err == nil {
		att.Title = t.Title
	}
	if req.NoResume {
		r.setAttaching(false)
		r.setState(ctx, state.StateReleased, "released to "+req.Holder)
		m.dropLease(ctx, a.ID, req.Holder)
		m.forget(a.ID)
		att.Released, att.Command = true, plan.Command
		m.event(ctx, EventAttached, a.ID, a.Ticket, map[string]any{"holder": req.Holder, "released": true})
		return att, nil
	}
	if plan.Kind == harness.Join {
		// The person joins the session as it runs: its environment keeps
		// the agent's token, and the attach command (tmux) needs none.
		att.Command, att.Join = plan.Command, true
	} else {
		token, err := m.rotateToken(ctx, a.ID)
		if err != nil {
			m.dropLease(ctx, a.ID, req.Holder)
			m.abandonAttach(ctx, r, "the attach failed: "+err.Error())
			return nil, err
		}
		att.Command = withEnv(plan.Command, control.TokenEnv, token)
	}
	r.setState(ctx, state.StateAttached, "attached by "+req.Holder)
	r.setAttaching(false)
	var err error
	att.Held, err = m.opts.State.Messages.Pending(ctx, state.AgentParty(a.ID))
	if err != nil {
		m.opts.Logger.Warn("agents: reading held messages", "agent", a.ID, "err", err)
	}
	m.event(ctx, EventAttached, a.ID, a.Ticket, map[string]any{"holder": req.Holder, "now": req.Now})
	return att, nil
}

// stopForAttach stops r's conversation for an attach, once its turn has
// settled (or, with Now, after interrupting it), and returns how to hand it
// over. r is left attaching: it holds its slot and its messages.
//
// If ctx ends first (the CLI gave up, or the daemon is stopping), it returns
// at once; the stop carries on, and the conversation is resumed once it has
// (see abandonAttach), so a person's Ctrl-C or a shutdown never waits on a
// long turn.
func (m *Manager) stopForAttach(ctx context.Context, r *runner, a *state.Agent, req *AttachRequest) (*harness.AttachPlan, error) {
	if err := m.beginAttach(r, a, req.Holder); err != nil {
		return nil, err
	}
	m.event(ctx, EventAttaching, a.ID, a.Ticket, map[string]any{"holder": req.Holder, "now": req.Now})
	plan, err := r.conv.AttachPlan(ctx)
	if err == nil && plan.Kind == harness.Join {
		return m.joinForAttach(ctx, r, a, plan, req)
	}
	if err == nil && (plan.Kind != harness.HandOver || plan.Command == nil) {
		err = fmt.Errorf("%w: %s has no TUI to hand over to", ErrNoHandOver, a.Harness)
	}
	if err != nil {
		r.setAttaching(false)
		if !errors.Is(err, ErrNoHandOver) {
			err = fmt.Errorf("%w: %w", ErrNoHandOver, err)
		}
		return nil, err
	}
	stopCtx := context.WithoutCancel(ctx)
	if req.Now {
		if err := r.conv.Interrupt(stopCtx); err != nil {
			m.opts.Logger.Warn("agents: interrupting for an attach", "agent", a.ID, "err", err)
		}
	}
	stopped := make(chan struct{})
	go func() {
		_ = r.conv.Stop(stopCtx, false)
		<-r.exited
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-ctx.Done():
		go func() {
			<-stopped
			m.abandonAttach(stopCtx, r, "the attach was given up before its TUI started")
		}()
		return nil, fmt.Errorf("agents: attaching %s: %w", a.ID, ctx.Err())
	}
	r.mu.Lock()
	finishing := r.stopping
	r.inTurn = false
	r.mu.Unlock()
	if finishing {
		// It called done in the turn the attach waited for, or the daemon
		// is stopping: either way there's nothing to attach to.
		return nil, fmt.Errorf("%w: agent %s finished while its turn settled", ErrNoLiveAgent, a.ID)
	}
	return plan, nil
}

// beginAttach marks r attaching for holder, refusing a runner that is
// finishing, attached, or already being attached by someone.
//
// It runs under deliverMu, so a delivery in flight either finishes first,
// and the slot queue entry it made is dropped below, or sees the attach
// and holds its messages. Between the two, it would queue the attached
// agent after the drop: at the front of its pool, for a slot it never
// takes, stalling every agent behind it.
func (m *Manager) beginAttach(r *runner, a *state.Agent, holder string) error {
	r.deliverMu.Lock()
	defer r.deliverMu.Unlock()
	r.mu.Lock()
	switch {
	case r.stopping:
		r.mu.Unlock()
		return fmt.Errorf("%w: agent %s is finishing", ErrNoLiveAgent, a.ID)
	case r.attaching:
		pending := r.attachHolder
		r.mu.Unlock()
		return &state.LeaseHeldError{Holder: pending, ExpiresAt: m.opts.Clock.Now().Add(m.opts.Config.Agents.AttachLease.D())}
	case r.st == state.StateAttached || r.conv == nil:
		r.mu.Unlock()
		return fmt.Errorf("%w: agent %s is already attached", state.ErrConflict, a.ID)
	}
	r.attaching, r.attachHolder = true, holder
	r.mu.Unlock()
	// An agent waiting for a slot gives up its place: attached, it holds a
	// slot of its own, and left at the front it would stall its pool.
	m.mu.Lock()
	m.wantSlot = slices.DeleteFunc(m.wantSlot, func(id ident.AgentID) bool { return id == r.id })
	m.mu.Unlock()
	return nil
}

// abandonAttach resumes a conversation an attach stopped but never handed
// over. A daemon that is stopping leaves it for the next start to resume.
func (m *Manager) abandonAttach(ctx context.Context, r *runner, why string) {
	m.mu.Lock()
	stopping := m.stopping
	m.mu.Unlock()
	r.mu.Lock()
	finishing := r.stopping
	r.mu.Unlock()
	if stopping || finishing {
		r.setAttaching(false)
		return
	}
	r.setState(ctx, state.StateAttached, why)
	r.setAttaching(false)
	if _, err := m.handBack(ctx, r.id, why); err != nil {
		m.opts.Logger.Warn("agents: resuming after an abandoned attach", "agent", r.id, "err", err)
	}
}

// LiveAgent is the live agent target names: an agent id, or a ticket id.
func (m *Manager) LiveAgent(ctx context.Context, target string) (*state.Agent, error) {
	return m.resolveAgent(ctx, target)
}

// dropLease releases a lease the caller holds, where failing to is only
// worth a log: it would expire anyway. One already released is fine.
func (m *Manager) dropLease(ctx context.Context, id ident.AgentID, holder string) {
	if err := m.opts.State.Agents.ReleaseLease(ctx, id, holder); err != nil && !errors.Is(err, state.ErrConflict) {
		m.opts.Logger.Warn("agents: releasing an attach lease", "agent", id, "err", err)
	}
}

// Reattach is an attached CLI reconnecting after a daemon restart: it
// renews the lease it still holds, in one conditional write, so it can't
// re-take one the sweep or a detach handed back meanwhile; that is
// ErrNotAttached, and the CLI must end its TUI.
func (m *Manager) Reattach(ctx context.Context, target, holder string) (*Attachment, error) {
	a, err := m.resolveAgent(ctx, target)
	if err != nil {
		return nil, err
	}
	if err := m.Heartbeat(ctx, a.ID, holder); err != nil {
		return nil, err
	}
	m.event(ctx, EventReattached, a.ID, a.Ticket, map[string]string{"holder": holder})
	return &Attachment{Agent: a}, nil
}

// Heartbeat renews holder's lease on the agent.
func (m *Manager) Heartbeat(ctx context.Context, id ident.AgentID, holder string) error {
	if err := m.opts.State.Agents.RenewLease(ctx, id, m.lease(holder)); err != nil {
		if errors.Is(err, state.ErrConflict) {
			return fmt.Errorf("%w: %w", ErrNotAttached, err)
		}
		return err
	}
	return nil
}

// StreamOpen marks an attached CLI's stream as open in this daemon until
// the returned func is called: the expiry sweep leaves such an agent
// alone, since its heartbeat (late after a suspend, say) is still coming.
func (m *Manager) StreamOpen(id ident.AgentID) func() {
	m.mu.Lock()
	m.streams[id]++
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.streams[id]--; m.streams[id] <= 0 {
			delete(m.streams, id)
		}
	}
}

// HandBack ends holder's attach: the person quit the TUI, was detached, or
// hung up. why is recorded with it.
func (m *Manager) HandBack(ctx context.Context, id ident.AgentID, holder, why string) (HandBackOutcome, error) {
	if err := m.opts.State.Agents.ReleaseLease(ctx, id, holder); err != nil {
		if errors.Is(err, state.ErrConflict) {
			return "", fmt.Errorf("%w: %w", ErrNotAttached, err)
		}
		return "", err
	}
	return m.handBack(ctx, id, why)
}

// handBack runs an agent whose lease is gone again: it resumes the
// conversation with a note and the held messages, or finishes the agent if
// it called done from the TUI.
func (m *Manager) handBack(ctx context.Context, id ident.AgentID, why string) (HandBackOutcome, error) {
	r, ok := m.runner(id)
	if !ok {
		return Ended, nil
	}
	r.mu.Lock()
	st, decl, summary, finishing := r.st, r.decl, r.summary, r.stopping
	r.mu.Unlock()
	if st != state.StateAttached || finishing {
		return Ended, nil
	}
	if err := m.opts.State.Agents.SetDeclared(ctx, id, "", ""); err != nil {
		m.opts.Logger.Warn("agents: clearing an attach's declaration", "agent", id, "err", err)
	}
	if decl == declDone {
		err := m.finish(ctx, r, summary)
		if err == nil {
			m.event(ctx, EventHandedBack, id, r.ticket, map[string]string{"why": why, "outcome": string(Finished)})
			return Finished, nil
		}
		// The ticket couldn't be finished: the agent is resumed, and told.
		decl, summary = declNone, ""
		r.consume(declDone)
		m.enqueueNotice(ctx, r, fmt.Sprintf("`done` didn't go through: %v. The ticket isn't finished: carry on, "+
			"or say what you're waiting for.", err))
	}
	if decl == declNone {
		// An ask still open from before the attach is still waiting on the
		// user: the resumed turn ends in waiting_user, not a nudge.
		if open, err := m.opts.State.Messages.OpenAsksFrom(ctx, id); err == nil && len(open) > 0 {
			decl = declAsk
		}
	}
	if r.joined() {
		return m.rejoin(ctx, r, decl, summary, why), nil
	}
	a, err := m.opts.State.Agents.Get(ctx, id)
	if err != nil {
		return "", err
	}
	conv, caps, err := m.startConversation(ctx, a)
	if err != nil {
		r.setState(ctx, state.StateCrashed, "couldn't resume after an attach: "+err.Error())
		m.tellUser(ctx, id, fmt.Sprintf("agent %s (ticket %s) couldn't be resumed after its attach: %v; slop-bot is retrying", id, a.Ticket, err))
		m.forget(id)
		// Like a harness that died: retried, then given up on, never left
		// live with nothing running it.
		m.goRecover(r)
		return "", err
	}
	payload := map[string]string{"why": why, "outcome": string(Resumed)}
	// A harness whose protocol reports the id (Codex, pi) could hand back a
	// different one; the next restart must resume the one now running.
	if got := conv.Ref().ID; got != a.ConversationID {
		m.opts.Logger.Warn("agents: the conversation id changed across an attach", "agent", id, "was", a.ConversationID, "now", got)
		payload["conversation_was"] = a.ConversationID
	}
	nr := m.adopt(ctx, &adoption{id: id, ticket: a.Ticket, harness: a.Harness, pool: a.Pool, conv: conv, caps: caps,
		st: state.StateAttached, decl: decl, summary: summary, holdsSlot: true})
	m.event(ctx, EventHandedBack, id, a.Ticket, payload)
	if _, err := m.opts.State.Messages.Enqueue(ctx, &state.NewMessage{
		From: state.UserParty, To: state.AgentParty(id), Kind: state.KindSystem, Body: handBackNote,
	}); err != nil {
		m.opts.Logger.Warn("agents: queueing the hand-back note", "agent", id, "err", err)
	}
	nr.setState(ctx, state.StateStarting, "handed back: "+why)
	m.deliverPending(ctx, nr)
	return Resumed, nil
}

// sweepLeases hands back every attach whose lease has expired: its CLI
// stopped heartbeating, and didn't reconnect after a daemon restart.
func (m *Manager) sweepLeases(ctx context.Context) {
	now := m.opts.Clock.Now()
	expired, err := m.opts.State.Agents.ExpiredLeases(ctx, now)
	if err != nil {
		m.opts.Logger.Warn("agents: listing expired attach leases", "err", err)
		return
	}
	for _, a := range expired {
		m.mu.Lock()
		open := m.streams[a.ID] > 0
		m.mu.Unlock()
		if open {
			continue
		}
		// A heartbeat or reconnect that beat this sweep wins.
		if err := m.opts.State.Agents.ReleaseExpiredLease(ctx, a.ID, a.Lease.Holder, now); err != nil {
			continue
		}
		if _, err := m.handBack(ctx, a.ID, "its attach lease expired"); err != nil {
			m.opts.Logger.Warn("agents: handing back an expired attach", "agent", a.ID, "err", err)
		}
	}
}

// declare records what the agent says it is doing. Declared from inside an
// attached TUI, it is also kept in state.db: the hand-back acts on it, and
// may come after a daemon restart.
func (m *Manager) declare(ctx context.Context, r *runner, d declared, summary string) {
	r.declare(d, summary)
	if r.state() != state.StateAttached {
		return
	}
	if err := m.opts.State.Agents.SetDeclared(ctx, r.id, string(d), summary); err != nil {
		m.opts.Logger.Warn("agents: recording an attached agent's declaration", "agent", r.id, "err", err)
	}
}

// sweeper runs sweepLeases on the clock until Stop.
func (m *Manager) sweeper() {
	defer close(m.swept)
	ctx := context.Background()
	every := max(m.opts.Config.Agents.AttachLease.D()/2, 1)
	tick := m.opts.Clock.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-tick.C():
			m.sweepLeases(ctx)
		}
	}
}

// withEnv is spec with key set in its environment, replacing any value.
func withEnv(spec *harness.LaunchSpec, key, value string) *harness.LaunchSpec {
	out := *spec
	out.Args = slices.Clone(spec.Args)
	out.Env = slices.DeleteFunc(slices.Clone(spec.Env), func(kv string) bool { return strings.HasPrefix(kv, key+"=") })
	out.Env = append(out.Env, key+"="+value)
	return &out
}
