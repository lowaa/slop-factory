package agents

import (
	"context"
	"errors"
	"fmt"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

// What an agent whose TUI session outlived a daemon restart is told, when
// the restart caught it mid-work: its slop-bot calls failed meanwhile, and
// a turn that ended then went unseen.
const adoptedNote = "slop-bot restarted while you worked. Any slop-bot command that failed in that time " +
	"(done, ask, status set, ...) didn't reach it: run it again, then carry on."

// adoptSession takes a's session back, if its harness's sessions outlive the
// daemon and a's did: the conversation runs on as it was, and keeps its
// token (the session's environment has it), where a resume starts the
// conversation again. ok is false when there is nothing to adopt. A session
// that is there but can't be adopted is an error: a resume would start a
// second one under the same name.
func (m *Manager) adoptSession(ctx context.Context, a *state.Agent) (s *started, ok bool, err error) {
	h, found := m.opts.Harnesses.Harness(a.Harness)
	ad, isAdopter := h.(harness.Adopter)
	if !found || !isAdopter {
		return nil, false, nil
	}
	conv, err := ad.AdoptAgent(ctx, a.ID)
	switch {
	case errors.Is(err, harness.ErrNoSession):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("adopting its session: %w", err)
	}
	// Still running where it started.
	return &started{conv: conv, caps: h.Capabilities(), dir: a.ConversationDir}, true, nil
}

// joinForAttach is stopForAttach for a conversation a person joins (a TUI
// in tmux): nothing stops, and the agent stays the same session. The kit
// types nothing while the person is attached, and its messages are held
// (deliverPending) until the hand-back.
func (m *Manager) joinForAttach(ctx context.Context, r *runner, a *state.Agent, plan *harness.AttachPlan, req *AttachRequest) (*harness.AttachPlan, error) {
	if req.NoResume || plan.Command == nil {
		r.setAttaching(false)
		return nil, fmt.Errorf("%w: %s's session can't be released to you; attach to work in it, then detach to hand it back",
			ErrNoHandOver, a.Harness)
	}
	r.mu.Lock()
	r.joinable, r.joinedExit, r.rejoined = true, false, false
	r.mu.Unlock()
	if req.Now {
		if err := r.conv.Interrupt(ctx); err != nil {
			m.opts.Logger.Warn("agents: interrupting for an attach", "agent", a.ID, "err", err)
		}
	}
	return plan, nil
}

// joined says whether r's conversation is a session a person joined that
// is still running, claiming it for the hand-back: there is then nothing to
// resume. It decides, under r.mu, against the session's exit (exitedWith):
// once claimed, an exit is a crash to recover from; once exited, the
// hand-back resumes the conversation.
func (r *runner) joined() bool {
	if r.conv == nil {
		return false
	}
	select {
	case <-r.exited:
		return false // let go of, or gone
	default:
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.joinable || r.joinedExit {
		return false
	}
	r.rejoined = true
	return true
}

// rejoin hands back a joined session that is still running: the agent
// carries on in it, told a person was there, with the messages held
// meanwhile, and keeps its slot into that delivery.
func (m *Manager) rejoin(ctx context.Context, r *runner, decl declared, summary, why string) HandBackOutcome {
	r.declare(decl, summary)
	r.mu.Lock()
	inTurn := r.inTurn
	// Into the delivery of what was held, if it isn't in a turn: a turn
	// that runs on holds the slot by itself, and a slot carried past its
	// end would never be given back (only an idle delivery takes it).
	r.holdsSlot = !inTurn
	r.mu.Unlock()
	m.event(ctx, EventHandedBack, r.id, r.ticket, map[string]string{"why": why, "outcome": string(Resumed)})
	m.enqueueNotice(ctx, r, handBackNote)
	if inTurn {
		r.setState(ctx, r.workingState(ctx), "handed back: "+why)
	} else {
		r.setState(ctx, state.StateIdle, "handed back: "+why)
	}
	m.deliverPending(ctx, r)
	return Resumed
}
