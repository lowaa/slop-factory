package agents

import (
	"context"
	"fmt"
	"sync"

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

	mu       sync.Mutex
	st       state.AgentState
	inTurn   bool
	decl     declared
	summary  string
	stopping bool
	// attaching is set while an attach stops the conversation: the turn's
	// end mustn't start another, and the exit isn't a crash.
	attaching bool
	// attachHolder is who is attaching, for a second attach's refusal.
	attachHolder string
	// holdsSlot carries an attached agent's slot into its first turn after
	// the hand-back, so it can't lose it to new work in between.
	holdsSlot bool
	// exited is closed when the conversation's events end.
	exited chan struct{}
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
				r.m.tellUser(ctx, r.id, fmt.Sprintf("agent %s (ticket %s) has gone quiet for %s mid-turn: `slop-bot watch %s`, or `slop-bot tell --interrupt %s ...`",
					r.id, r.ticket, stallAfter, r.id, r.id))
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
	// The declaration is consumed here, not cleared when a turn starts: the
	// agent's CLI call can land before this goroutine has even seen the
	// turn start.
	r.mu.Lock()
	decl, summary, stopping := r.decl, r.summary, r.stopping || r.attaching
	if !stopping {
		r.decl, r.summary = declNone, ""
	}
	r.mu.Unlock()
	// Draining for a daemon stop or an attach: start nothing new. The agent
	// keeps its state and what it declared, so a resume knows it was
	// mid-work and says so, and an ask still stands after the hand-back.
	if stopping && decl != declDone {
		return
	}
	if decl == declDone {
		r.m.finish(ctx, r, summary)
		return
	}
	// Messages that arrived during a turn the harness couldn't steer go now,
	// as the next turn.
	if r.m.deliverPending(ctx, r) {
		return
	}
	switch decl {
	case declAsk:
		r.setState(ctx, state.StateWaitingUser, "asked the user")
		return
	case declDiscuss:
		r.setState(ctx, state.StateDiscussion, "raised a discussion")
		return
	case declMerge:
		r.setState(ctx, state.StateWaitingMerge, "ready to merge")
		return
	}
	switch ev.Outcome {
	case harness.TurnInterrupted:
		// A person interrupted it: wait for them, don't nudge.
		r.setState(ctx, state.StateIdle, "interrupted")
	case harness.TurnFailed:
		r.setState(ctx, state.StateStalled, "turn failed: "+ev.Text)
		r.m.tellUser(ctx, r.id, fmt.Sprintf("agent %s (ticket %s): its turn failed: %s", r.id, r.ticket, ev.Text))
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
	r.m.tellUser(ctx, r.id, msg)
	r.m.forget(r.id)
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
