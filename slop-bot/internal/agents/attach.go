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

// Attach takes an agent's conversation over for a person: it takes the
// lease, stops the conversation once its turn settles (or interrupts it
// first), and returns the TUI command that resumes it. The agent keeps its
// slot until it's handed back.
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
	if err := m.opts.State.Agents.AcquireLease(ctx, a.ID, m.lease(req.Holder)); err != nil {
		return nil, err
	}
	att, err := m.takeOver(ctx, r, a, req)
	if err != nil {
		m.dropLease(ctx, a.ID, req.Holder)
		return nil, err
	}
	return att, nil
}

func (m *Manager) takeOver(ctx context.Context, r *runner, a *state.Agent, req *AttachRequest) (*Attachment, error) {
	plan, err := m.stopForAttach(ctx, r, a, req)
	if err != nil {
		return nil, err
	}
	// What follows outlives the request too: see stopForAttach.
	ctx = context.WithoutCancel(ctx)
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
	r.setState(ctx, state.StateAttached, "attached by "+req.Holder)
	r.setAttaching(false)
	token, err := m.rotateToken(ctx, a.ID)
	if err != nil {
		// Nobody will run its TUI: hand it straight back, lease first as the
		// expiry sweep does; Attach's own release then finds nothing to do.
		m.dropLease(ctx, a.ID, req.Holder)
		_, _ = m.handBack(ctx, a.ID, "the attach failed: "+err.Error())
		return nil, err
	}
	att.Command = withEnv(plan.Command, control.TokenEnv, token)
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
func (m *Manager) stopForAttach(ctx context.Context, r *runner, a *state.Agent, req *AttachRequest) (*harness.AttachPlan, error) {
	r.mu.Lock()
	switch {
	case r.stopping:
		r.mu.Unlock()
		return nil, fmt.Errorf("%w: agent %s is finishing", ErrNoLiveAgent, a.ID)
	case r.attaching || r.st == state.StateAttached || r.conv == nil:
		r.mu.Unlock()
		return nil, fmt.Errorf("%w: agent %s is already attached", state.ErrConflict, a.ID)
	}
	r.attaching = true
	r.mu.Unlock()
	m.event(ctx, EventAttaching, a.ID, a.Ticket, map[string]any{"holder": req.Holder, "now": req.Now})
	plan, err := r.conv.AttachPlan(ctx)
	if err == nil && (plan.Kind != harness.HandOver || plan.Command == nil) {
		err = fmt.Errorf("%w: %s attaches by joining its session, which isn't supported yet", ErrNoHandOver, a.Harness)
	}
	if err != nil {
		r.setAttaching(false)
		if !errors.Is(err, ErrNoHandOver) {
			err = fmt.Errorf("%w: %w", ErrNoHandOver, err)
		}
		return nil, err
	}
	// The stop outlives the request: a CLI that gives up mid-wait leaves an
	// attach its hang-up hands back, not a half-stopped conversation.
	stopCtx := context.WithoutCancel(ctx)
	if req.Now {
		if err := r.conv.Interrupt(stopCtx); err != nil {
			m.opts.Logger.Warn("agents: interrupting for an attach", "agent", a.ID, "err", err)
		}
	}
	_ = r.conv.Stop(stopCtx, false)
	<-r.exited
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
// renews the lease it still holds. One that has expired and been handed
// back is ErrNotAttached, and the CLI must end its TUI.
func (m *Manager) Reattach(ctx context.Context, target, holder string) (*Attachment, error) {
	a, err := m.resolveAgent(ctx, target)
	if err != nil {
		return nil, err
	}
	if a.Mode != state.ModeAttached || a.Lease == nil || a.Lease.Holder != holder {
		return nil, fmt.Errorf("%w: agent %s was handed back", ErrNotAttached, a.ID)
	}
	if err := m.opts.State.Agents.AcquireLease(ctx, a.ID, m.lease(holder)); err != nil {
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
	st, decl, summary := r.st, r.decl, r.summary
	r.mu.Unlock()
	if st != state.StateAttached {
		return Ended, nil
	}
	if decl == declDone {
		m.event(ctx, EventHandedBack, id, r.ticket, map[string]string{"why": why, "outcome": string(Finished)})
		m.finish(ctx, r, summary)
		return Finished, nil
	}
	a, err := m.opts.State.Agents.Get(ctx, id)
	if err != nil {
		return "", err
	}
	conv, caps, err := m.startConversation(ctx, a)
	if err != nil {
		r.setState(ctx, state.StateCrashed, "couldn't resume after an attach: "+err.Error())
		m.tellUser(ctx, id, fmt.Sprintf("agent %s (ticket %s) couldn't be resumed after its attach: %v", id, a.Ticket, err))
		m.forget(id)
		return "", err
	}
	payload := map[string]string{"why": why, "outcome": string(Resumed)}
	// Every supported harness keeps the id across the round trip; one that
	// didn't would have the next restart resume the wrong conversation.
	if got := conv.Ref().ID; got != a.ConversationID {
		m.opts.Logger.Warn("agents: the conversation id changed across an attach", "agent", id, "was", a.ConversationID, "now", got)
		payload["conversation_was"] = a.ConversationID
	}
	nr := m.adopt(ctx, &adoption{id: id, ticket: a.Ticket, pool: a.Pool, conv: conv, caps: caps,
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
		// A heartbeat or reconnect that beat this sweep wins.
		if err := m.opts.State.Agents.ReleaseExpiredLease(ctx, a.ID, a.Lease.Holder, now); err != nil {
			continue
		}
		if _, err := m.handBack(ctx, a.ID, "its attach lease expired"); err != nil {
			m.opts.Logger.Warn("agents: handing back an expired attach", "agent", a.ID, "err", err)
		}
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
