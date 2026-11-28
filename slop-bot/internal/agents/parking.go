package agents

import (
	"cmp"
	"context"
	"errors"
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

// parkGrace is how long a parked agent's harness gets to stop cleanly before
// it is killed. It is between turns, so a clean stop is quick; the bound is
// its own, not the start's, so a slow stop can't eat the time of the start
// it makes room for.
const parkGrace = 10 * time.Second

// parkableLocked reports whether r may be stopped to free a process: it is
// between turns waiting on a person (an ask, a discussion, a merge, an
// interrupt) or on its ticket's blockers. Never one in a turn, settling its
// turn's end (a nudge or a message may be on its way), starting, attached or
// being attached, finishing, or with a slot to take into its next turn; and
// not stalled, which can be set mid-turn and read before the turn's end is
// settled. Call it holding r.mu.
func (r *runner) parkableLocked() bool {
	if r.conv == nil || r.inTurn || r.settling || r.attaching || r.holdsSlot || r.stopping || r.ending || r.finishing {
		return false
	}
	switch r.st {
	case state.StateWaitingUser, state.StateDiscussion, state.StateWaitingMerge, state.StateIdle:
		return true
	}
	return false
}

func (r *runner) parkable() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.parkableLocked()
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
	return m.roomLocked("")
}

// roomLocked reports whether there is a live place for one more process,
// or an agent to park for one, on behalf of head (a queued agent being
// served, or "" for new work). A start counts only until its runner is
// adopted, so an agent being resumed or handed back isn't counted twice.
// Call it holding mu.
func (m *Manager) roomLocked(head ident.AgentID) (ok bool, victim *runner) {
	live := len(m.runners)
	for ticket := range m.starting {
		if !m.hasRunnerLocked(ticket) {
			live++
		}
	}
	if live < m.opts.Config.Concurrency.MaxLive {
		return true, nil
	}
	victim = m.victimLocked(head)
	return victim != nil, victim
}

func (m *Manager) hasRunnerLocked(ticket ident.TicketID) bool {
	for _, r := range m.runners {
		if r.ticket == ticket {
			return true
		}
	}
	return false
}

// victimLocked is the parkable agent that has waited longest, or nil. Not
// one a start is in flight for (a resume or hand-back still settling), nor
// one a CLI is streaming from. One queued for a slot has work to do, so it
// goes only when there is no other and head is ahead of it: parked, it
// keeps its place and is resumed when served, where sparing it would leave
// head, and everyone behind head, waiting for good. Ties go to the lower
// id, so the choice is the same on every run. Call it holding mu.
func (m *Manager) victimLocked(head ident.AgentID) *runner {
	var free, behind []*runner
	headAt := m.queuedAtLocked(head)
	for id, r := range m.runners {
		if _, starting := m.starting[r.ticket]; starting || m.streams[id] > 0 || !r.parkable() {
			continue
		}
		switch at := m.queuedAtLocked(id); {
		case at < 0:
			free = append(free, r)
		case head != "" && headAt >= 0 && at > headAt:
			behind = append(behind, r)
		}
	}
	candidates := free
	if len(candidates) == 0 {
		candidates = behind
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
// (takeSlot) and queues it for a slot instead. Its queue place, if it has
// one, is kept. Its conversation is stopped by park. It checks v is still
// parkable in the same critical section that marks it, so an attach can't
// slip in between, and returns nil, parking nothing, when it isn't. Call it
// holding mu.
func (m *Manager) parkLocked(v *runner) *parkedAgent {
	v.mu.Lock()
	if !v.parkableLocked() {
		v.mu.Unlock()
		return nil
	}
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
// clean stop loses nothing; one that outlives parkGrace is killed.
func (m *Manager) park(ctx context.Context, r *runner, p *parkedAgent) {
	keep := context.WithoutCancel(ctx)
	// Recorded first: a daemon that dies from here on leaves it parked, not
	// resumed at the next start into a process it gave up.
	m.setParked(keep, r.id, true)
	grace, cancel := context.WithTimeout(keep, parkGrace)
	defer cancel()
	r.stopConv(grace, false)
	select {
	case <-r.exited:
	case <-grace.Done():
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
	room, victim := m.roomLocked(id)
	if !room {
		return
	}
	var parkedVictim *parkedAgent
	if victim != nil {
		if parkedVictim = m.parkLocked(victim); parkedVictim == nil {
			return // it stopped being parkable: the next pass looks again
		}
	}
	delete(m.parked, id)
	m.goStartLocked(p.ticket, p.pool, false, func(ctx context.Context) {
		if victim != nil {
			m.park(ctx, victim, parkedVictim)
		}
		// Its own park, if still stopping its process: one conversation
		// never runs in two.
		select {
		case <-p.stopped:
		case <-ctx.Done():
			m.repark(p)
			return
		}
		m.unpark(ctx, p)
	})
}

// repark puts p back among the parked, still queued: a resume that couldn't
// go ahead is tried again when the queue is next served, rather than lost.
func (m *Manager) repark(p *parkedAgent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopping {
		return
	}
	m.parked[p.id] = p
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
// is delivered as it resumes, taking the slot the queue kept for it. A
// failed read leaves it parked, to try again.
func (m *Manager) unpark(ctx context.Context, p *parkedAgent) {
	id := p.id
	a, err := m.opts.State.Agents.Get(ctx, id)
	switch {
	case errors.Is(err, state.ErrNotFound) || (err == nil && a.EndedAt != nil):
		return
	case err != nil:
		m.opts.Logger.Warn("agents: reading a parked agent to resume", "agent", id, "err", err)
		m.repark(p)
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
			m.repark(p)
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
