package agents

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
	"github.com/lowaa/slop-factory/slop-bot/internal/transcript"
)

// declared is what an agent said, during a turn, it is doing at the turn's
// end: the explicit states that matter most come from the agent, not from
// inference.
type declared string

const (
	declNone     declared = ""
	declAsk      declared = "ask"
	declDiscuss  declared = "discuss"
	declMerge    declared = "ready_to_merge"
	declDone     declared = "done"
	declContinue declared = "continue"
)

// runner owns one live agent's conversation. An attach stops the
// conversation and leaves the runner in place, holding the agent's slot and
// taking its verbs from inside the TUI; the hand-back replaces it with a
// runner on the resumed conversation.
type runner struct {
	m       *Manager
	id      ident.AgentID
	ticket  ident.TicketID
	harness string
	pool    state.Pool
	// conv is nil for an agent that was attached when the daemon started:
	// its conversation is the person's TUI, not ours.
	conv harness.Conversation
	caps harness.Capabilities

	mu sync.Mutex
	st state.AgentState
	// since is when the agent entered st: who has waited longest, for
	// parking.
	since   time.Time
	inTurn  bool
	decl    declared
	summary string
	// stopping is a stop under way: no new turn starts, and an exit is
	// expected, not a crash.
	stopping bool
	// ending is the agent being finished for good, so it is ended once.
	ending bool
	// finishing is the agent's own done being carried out: the ticket's
	// move to done is its, not a person's, so sync leaves it be.
	finishing bool
	// deliverMu makes a delivery (read the pending messages, send them,
	// mark them delivered) one step per agent: concurrent tells, the
	// scheduler and the runner's own loop would otherwise each send the
	// same queued messages.
	deliverMu sync.Mutex
	// attaching is set while an attach stops the conversation: the turn's
	// end mustn't start another, and the exit isn't a crash.
	attaching bool
	// attachHolder is who is attaching, for a second attach's refusal.
	attachHolder string
	// holdsSlot carries an attached agent's slot into its first turn after
	// the hand-back, so it can't lose it to new work in between.
	holdsSlot bool
	// parking is the agent being stopped to free a process: it takes no
	// slot, and a message for it queues it to be resumed.
	parking bool
	// exited is closed when the conversation's events end.
	exited chan struct{}
}

func (r *runner) isEnding() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ending || r.finishing
}

func (r *runner) setFinishing(on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishing = on
}

// active reports whether the runner holds a slot: in a turn, starting, or
// attached (a person is working it). Waiting agents keep their process but
// not a slot.
func (r *runner) active() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inTurn || r.attaching || r.holdsSlot || r.st == state.StateStarting || r.st == state.StateAttached
}

func (r *runner) setAttaching(on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attaching = on
}

// stopConv stops the conversation, if this runner has one.
func (r *runner) stopConv(ctx context.Context, force bool) {
	if r.conv != nil {
		_ = r.conv.Stop(ctx, force)
	}
}

func (r *runner) state() state.AgentState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.st
}

// setState moves the agent and records it. A no-op when it's already there.
func (r *runner) setState(ctx context.Context, to state.AgentState, reason string) {
	r.mu.Lock()
	from := r.st
	r.st = to
	if from != to {
		r.since = r.m.opts.Clock.Now()
	}
	r.mu.Unlock()
	if from == to {
		return
	}
	if _, err := r.m.opts.State.Agents.SetState(ctx, &state.Transition{Agent: r.id, To: to, Reason: reason}); err != nil {
		r.m.opts.Logger.Warn("agents: recording a state", "agent", r.id, "to", to, "err", err)
	}
	r.m.event(ctx, EventState, r.id, r.ticket, map[string]string{"from": string(from), "to": string(to), "reason": reason})
	if to.Ends() {
		r.m.retain(ctx, r.id)
	}
	r.m.Kick()
}

// declare records what the agent says it is doing at the end of this turn.
func (r *runner) declare(d declared, summary string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decl, r.summary = d, summary
}

// consume clears the declaration a turn end acted on, unless the agent has
// made a new one since it was read.
func (r *runner) consume(d declared) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.decl == d {
		r.decl, r.summary = declNone, ""
	}
}

// run consumes the conversation's events until it exits. Stall detection
// runs on the injected clock, re-armed by every event.
func (r *runner) run(ctx context.Context) {
	defer close(r.exited)
	stallAfter := r.m.opts.Config.Agents.StallAfter.D()
	stall := r.m.opts.Clock.After(stallAfter)
	for {
		select {
		case ev, ok := <-r.conv.Events():
			if !ok {
				return
			}
			stall = r.m.opts.Clock.After(stallAfter)
			r.handle(ctx, &ev)
		case <-stall:
			stall = r.m.opts.Clock.After(stallAfter)
			if r.active() && r.state() != state.StateStalled {
				r.setState(ctx, state.StateStalled, fmt.Sprintf("no activity for %s", stallAfter))
				r.m.tellUser(ctx, r.id, fmt.Sprintf("agent %s (ticket %s) has gone quiet for %s mid-turn: `slop-bot events` shows its last activity, "+
					"and `slop-bot tell --interrupt %s ...` stops the turn and steers it.", r.id, r.ticket, stallAfter, r.id))
			}
		}
	}
}

func (r *runner) handle(ctx context.Context, ev *harness.Event) {
	// A limit is recorded before its event, so whoever sees the event (a
	// test, `status`) finds it in effect.
	if ev.Kind == harness.LimitReport && ev.Limit != nil {
		r.m.reportLimit(ctx, r, ev.Limit)
	}
	// Likewise a completed turn resets the recovery count before its event:
	// it shows the harness works again after a recovery. A failed one
	// doesn't: a resume that can't find its session reports one (Claude Code
	// does) just before it exits, and counting that as working would resume
	// the agent for ever.
	if ev.Kind == harness.TurnEnded && ev.Outcome == harness.TurnCompleted {
		if err := r.m.opts.State.Agents.ResetRecoveries(ctx, r.id); err != nil {
			r.m.opts.Logger.Debug("agents: resetting recoveries", "agent", r.id, "err", err)
		}
	}
	// The conversation's own events go in the log first, so `slop-bot watch`
	// shows a turn ending before the state it leads to. Ready and Exited
	// aren't the conversation: the states say them.
	if ev.Kind != harness.Ready && ev.Kind != harness.Exited {
		r.m.event(ctx, EventHarness+string(ev.Kind), r.id, r.ticket, eventPayload(ev))
	}
	switch ev.Kind {
	case harness.TurnStarted:
		r.mu.Lock()
		r.inTurn = true
		r.mu.Unlock()
		r.setState(ctx, r.workingState(ctx), "turn started")
	case harness.ToolStarted, harness.ToolEnded, harness.Text:
		if r.state() == state.StateStalled {
			r.setState(ctx, r.workingState(ctx), "active again")
		}
	case harness.TurnEnded:
		r.mu.Lock()
		r.inTurn = false
		r.mu.Unlock()
		r.turnEnded(ctx, ev)
	case harness.Exited:
		r.exitedWith(ctx, ev)
	}
}

// workingState is refining while the ticket is still being prepared, and
// working once it's open.
func (r *runner) workingState(ctx context.Context) state.AgentState {
	t, err := r.m.opts.Tickets.Get(ctx, r.ticket)
	if err == nil && (t.Status == tickets.StatusPreparing || t.Status == tickets.StatusDiscussion) {
		return state.StateRefining
	}
	return state.StateWorking
}

func (r *runner) turnEnded(ctx context.Context, ev *harness.Event) {
	// The declaration is read here, not cleared when a turn starts: the
	// agent's CLI call can land before this goroutine has even seen the
	// turn start. It is cleared only once acted on, so messages that
	// pre-empt it with another turn don't lose it.
	r.mu.Lock()
	decl, summary, stopping := r.decl, r.summary, r.stopping || r.attaching
	r.mu.Unlock()
	if decl == declDone {
		err := r.m.finish(ctx, r, summary)
		if err == nil {
			return
		}
		r.consume(declDone)
		decl = declNone
		r.m.enqueueNotice(ctx, r, fmt.Sprintf("`done` didn't go through: %v. The ticket isn't finished: carry on, "+
			"or say what you're waiting for.", err))
	}
	// Draining for a daemon stop or an attach: start nothing new. The agent
	// keeps its state and what it declared, so a resume knows it was
	// mid-work and says so, and an ask still stands after the hand-back.
	if stopping {
		return
	}
	// Messages that arrived during a turn the harness couldn't steer go now,
	// as the next turn.
	if r.m.deliverPending(ctx, r) {
		return
	}
	r.consume(decl)
	r.settle(ctx, decl, ev)
}

// settle is the state a turn leaves the agent in when nothing is waiting
// to be delivered. What it waits on is read from where that is recorded
// (its open asks, its ticket), not only from this turn's declaration: a
// declaration made in an earlier turn still holds, and one already
// answered doesn't.
func (r *runner) settle(ctx context.Context, decl declared, ev *harness.Event) {
	asks, err := r.m.opts.State.Messages.OpenAsksFrom(ctx, r.id)
	if err == nil && len(asks) > 0 {
		r.setState(ctx, state.StateWaitingUser, "asked the user")
		return
	}
	t, err := r.m.opts.Tickets.Get(ctx, r.ticket)
	if err == nil && t.Status == tickets.StatusDiscussion {
		r.setState(ctx, state.StateDiscussion, "raised a discussion")
		return
	}
	if decl == declMerge {
		r.setState(ctx, state.StateWaitingMerge, "ready to merge")
		return
	}
	switch {
	case ev.Outcome == harness.TurnInterrupted:
		// A person interrupted it: wait for them, don't nudge.
		r.setState(ctx, state.StateIdle, "interrupted")
	case ev.Outcome == harness.TurnFailed:
		r.setState(ctx, state.StateStalled, "turn failed: "+ev.Text)
		r.m.tellUser(ctx, r.id, fmt.Sprintf("agent %s (ticket %s): its turn failed: %s", r.id, r.ticket, ev.Text))
	case err == nil && len(t.BlockedBy) > 0:
		// Told to wait for its blockers, it does: slop-bot wakes it when
		// they are done.
		r.setState(ctx, state.StateIdle, "waiting on its ticket's blockers")
	default:
		r.m.nudge(ctx, r)
	}
}

func (r *runner) exitedWith(ctx context.Context, ev *harness.Event) {
	r.mu.Lock()
	stopping := r.stopping || r.attaching
	r.mu.Unlock()
	if stopping {
		return
	}
	to := state.StateExited
	if ev.ExitCode != 0 {
		to = state.StateCrashed
	}
	r.setState(ctx, to, fmt.Sprintf("exit %d", ev.ExitCode))
	msg := fmt.Sprintf("agent %s (ticket %s) exited with status %d", r.id, r.ticket, ev.ExitCode)
	if ev.Err != nil {
		msg += ": " + ev.Err.Error()
	}
	r.m.tellUser(ctx, r.id, msg+"; slop-bot is resuming it")
	r.m.forget(r.id)
	r.m.goRecover(r)
}

// shutdown stops the conversation for a daemon stop: it finishes the turn
// and stays resumable.
func (r *runner) shutdown(ctx context.Context) {
	r.mu.Lock()
	r.stopping = true
	r.mu.Unlock()
	r.stopConv(ctx, false)
	<-r.exited
}

func eventPayload(ev *harness.Event) map[string]any {
	p := map[string]any{}
	if ev.Tool != "" {
		p["tool"] = ev.Tool
	}
	if ev.Outcome != "" {
		p["outcome"] = string(ev.Outcome)
	}
	if ev.Usage != nil {
		p["input_tokens"], p["output_tokens"] = ev.Usage.InputTokens, ev.Usage.OutputTokens
	}
	if ev.Text != "" {
		// The log is for following what an agent does, not its full
		// output: that is the transcript's.
		text, cut := transcript.Cut(ev.Text, eventTextLimit)
		p["text"] = text
		if cut > 0 {
			p["truncated"] = cut
		}
	}
	if ev.Limit != nil {
		p["used_percent"] = ev.Limit.UsedPercent
		if ev.Limit.Window != "" {
			p["window"] = ev.Limit.Window
		}
		if !ev.Limit.ResetsAt.IsZero() {
			p["resets_at"] = ev.Limit.ResetsAt
		}
	}
	return p
}

// eventTextLimit bounds the text an event carries.
const eventTextLimit = 2000
