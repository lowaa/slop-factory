package agents

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

// Past concurrency.max_live, a start that needs a process stops the
// longest-waiting idle agent first: it is parked. A parked agent keeps its
// state (what it waits on) and its record, has no runner, and holds no
// live place; state.db marks it (agents.parked_at), so a restart leaves it
// stopped. Its messages queue as usual, and the first one, or a change to
// its ticket it must hear of, queues it for a slot, to be resumed by
// conversation id when it gets one.

// EventParked is an agent stopped to free a process.
const EventParked = "agent.parked"

// parkedAgent is a live agent with no process, waiting for work.
type parkedAgent struct {
	id     ident.AgentID
	ticket ident.TicketID
	pool   state.Pool
	// stopped is closed once its process has exited: a resume waits for
	// it, so one conversation never runs in two processes.
	stopped chan struct{}
}

// newParked is the entry for an agent that already has no process.
func newParked(a *state.Agent) *parkedAgent {
	p := &parkedAgent{id: a.ID, ticket: a.Ticket, pool: a.Pool, stopped: make(chan struct{})}
	close(p.stopped)
	return p
}

// parkable reports whether r may be stopped to free a process: it is
// between turns waiting on a person (an ask, a discussion, a merge, an
// interrupt) or on its ticket's blockers. Never one in a turn, starting,
// attached or being attached, finishing, or with a slot to take into its
// next turn; and not stalled, which can be set mid-turn and read before
// the turn's end is settled.
func (r *runner) parkable() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conv == nil || r.inTurn || r.attaching || r.holdsSlot || r.stopping || r.ending || r.finishing {
		return false
	}
	switch r.st {
	case state.StateWaitingUser, state.StateDiscussion, state.StateWaitingMerge, state.StateIdle:
		return true
	}
	return false
}

func (r *runner) waitingSince() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.since
}

// admitLocked reports whether a start may take a place in pool now: none
// of the pool's agents is queued for a slot (new work never jumps one),
// the pool has a free slot, and there is a live place, or an agent that
// can be parked for one (victim). Call it holding mu.
func (m *Manager) admitLocked(pool state.Pool) (ok bool, victim *runner) {
	if m.aheadLocked(pool, "") || !m.slotFreeLocked(pool, "", "") {
		return false, nil
	}
	return m.roomLocked()
}

// roomLocked reports whether there is a live place for one more process,
// or an agent to park for one. Call it holding mu.
func (m *Manager) roomLocked() (ok bool, victim *runner) {
	if len(m.runners)+len(m.starting) < m.opts.Config.Concurrency.MaxLive {
		return true, nil
	}
	victim = m.victimLocked()
	return victim != nil, victim
}

// victimLocked is the parkable agent that has waited longest, or nil. One
// queued for a slot has work to do, and one a CLI is streaming from is
// being watched over an attach. Ties go to the lower id, so the choice is
// the same on every run. Call it holding mu.
func (m *Manager) victimLocked() *runner {
	candidates := make([]*runner, 0, len(m.runners))
	for id, r := range m.runners {
		if m.queuedAtLocked(id) < 0 && m.streams[id] == 0 && r.parkable() {
			candidates = append(candidates, r)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	return slices.MinFunc(candidates, func(a, b *runner) int {
		if c := a.waitingSince().Compare(b.waitingSince()); c != 0 {
			return c
		}
		return cmp.Compare(a.id, b.id)
	})
}

// parkLocked takes v out of the runners, so it holds no live place and is
// sent nothing more: a delivery already past this point finds it parking
// (takeSlot) and queues it for a slot instead. Its conversation is stopped
// by park. Call it holding mu.
func (m *Manager) parkLocked(v *runner) *parkedAgent {
	v.mu.Lock()
	// stopping makes its exit expected, and its turn's end start nothing.
	v.parking, v.stopping = true, true
	v.mu.Unlock()
	delete(m.runners, v.id)
	p := &parkedAgent{id: v.id, ticket: v.ticket, pool: v.pool, stopped: make(chan struct{})}
	m.parked[v.id] = p
	return p
}

// park stops a parked agent's conversation and waits for it to exit, on
// the goroutine of the start it makes room for. It is between turns, so a
// clean stop loses nothing; one that outlives the start's context is
// killed.
func (m *Manager) park(ctx context.Context, r *runner, p *parkedAgent) {
	keep := context.WithoutCancel(ctx)
	// Recorded first: a daemon that dies from here on leaves it parked, not
	// resumed at the next start into a process it gave up.
	m.setParked(keep, r.id, true)
	r.stopConv(ctx, false)
	select {
	case <-r.exited:
	case <-ctx.Done():
		r.stopConv(keep, true)
		<-r.exited
	}
	close(p.stopped)
	m.event(keep, EventParked, r.id, r.ticket, map[string]string{"state": string(r.state())})
}

// adoptParked takes on an agent that was parked when the daemon last
// stopped: it stays stopped, and is resumed when it next has work, as if
// the daemon had never stopped.
func (m *Manager) adoptParked(ctx context.Context, a *state.Agent) {
	m.mu.Lock()
	m.parked[a.ID] = newParked(a)
	m.mu.Unlock()
	// A message whose queue place was lost (the daemon died between the
	// two writes) still wakes it.
	if pending, err := m.opts.State.Messages.Pending(ctx, state.AgentParty(a.ID)); err == nil && len(pending) > 0 {
		m.wakeParked(a.ID)
	}
}

// wakeParked queues a parked agent for a slot, to be resumed: something came for
// it. It reports whether id is parked.
func (m *Manager) wakeParked(id ident.AgentID) bool {
	m.mu.Lock()
	p, ok := m.parked[id]
	if ok {
		m.queueLocked(slotWait{agent: p.id, ticket: p.ticket, pool: p.pool})
	}
	m.mu.Unlock()
	if ok {
		m.Kick()
	}
	return ok
}

func (m *Manager) isParked(id ident.AgentID) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.parked[id]
	return ok
}

// unparkWhenServed resumes a parked agent queued for a slot once it is
// its turn: first of its pool in the queue, with a free slot and a live
// place (parking another agent for one if need be). It holds a slot and
// the place while it resumes, like any start.
func (m *Manager) unparkWhenServed(id ident.AgentID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.parked[id]
	if !ok || m.stopping {
		return
	}
	if _, busy := m.starting[p.ticket]; busy || m.aheadLocked(p.pool, id) || !m.slotFreeLocked(p.pool, "", "") {
		return
	}
	room, victim := m.roomLocked()
	if !room {
		return
	}
	var parkedVictim *parkedAgent
	if victim != nil {
		parkedVictim = m.parkLocked(victim)
	}
	delete(m.parked, id)
	m.goStartLocked(p.ticket, p.pool, false, func(ctx context.Context) {
		if victim != nil {
			m.park(ctx, victim, parkedVictim)
		}
		select {
		case <-p.stopped:
		case <-ctx.Done():
			// Its park outlived this start: parked still, for the next try.
			m.mu.Lock()
			m.parked[id] = p
			m.mu.Unlock()
			return
		}
		m.unpark(ctx, id)
	})
}

// dropStray takes out of the slot queue an agent that is neither running,
// parked nor being started: nothing would ever serve it, and its pool would
// wait behind it for good.
func (m *Manager) dropStray(id ident.AgentID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.queuedAtLocked(id)
	if i < 0 {
		return
	}
	_, running := m.runners[id]
	_, parked := m.parked[id]
	_, starting := m.starting[m.wantSlot[i].ticket]
	if !running && !parked && !starting {
		m.dequeueLocked(id)
	}
}

// unpark resumes a parked agent by conversation id. What was waiting for it
// is delivered as it resumes, taking the slot the queue kept for it.
func (m *Manager) unpark(ctx context.Context, id ident.AgentID) {
	a, err := m.opts.State.Agents.Get(ctx, id)
	if err != nil || a.EndedAt != nil {
		return
	}
	keep := context.WithoutCancel(ctx)
	// Cleared before the resume, so the record is right by the time the
	// agent is given anything.
	m.setParked(keep, id, false)
	if err := m.resume(ctx, a, ""); err != nil {
		if m.base.Err() != nil {
			// The daemon is stopping: parked again, for the next start.
			m.setParked(keep, id, true)
			return
		}
		m.giveUp(ctx, a, fmt.Sprintf("it couldn't be resumed after it was parked: %v", err))
	}
}

func (m *Manager) setParked(ctx context.Context, id ident.AgentID, parked bool) {
	if err := m.opts.State.Agents.SetParked(ctx, id, parked); err != nil {
		m.opts.Logger.Warn("agents: recording whether an agent is parked", "agent", id, "parked", parked, "err", err)
	}
}

// syncParked keeps a parked agent in step with its ticket. A change it must
// be told of (its blockers, new repos) wakes it, and resume's own sync tells
// it; otherwise an open, unblocked ticket's work is started for it.
func (m *Manager) syncParked(ctx context.Context, a *state.Agent, t *tickets.Ticket) {
	if !m.isParked(a.ID) {
		return
	}
	have := map[string]bool{}
	for _, w := range a.Worktrees {
		have[w.Repo] = true
	}
	newRepo := slices.ContainsFunc(t.Repos, func(repo string) bool { return !have[repo] })
	if newRepo || (len(t.BlockedBy) > 0) != a.BlockedTold {
		m.wakeParked(a.ID)
		return
	}
	if t.Status == tickets.StatusOpen && len(t.BlockedBy) == 0 {
		if err := m.advanceTo(ctx, ident.AgentPrincipal(a.ID, a.Ticket), t.ID, tickets.StatusInProgress); err != nil {
			m.opts.Logger.Debug("agents: starting a parked agent's ticket's work", "ticket", t.ID, "err", err)
		}
	}
}
