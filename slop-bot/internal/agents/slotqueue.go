package agents

import (
	"context"
	"errors"
	"slices"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

// slotWait is one agent in the slot queue: an agent with something to act
// on and no free slot. It may be live, parked, or (after a restart) still
// being resumed, so it carries its own ticket and pool.
type slotWait struct {
	agent  ident.AgentID
	ticket ident.TicketID
	pool   state.Pool
}

// queueLocked puts w at the back of the slot queue unless it is already
// queued, and records it in state.db. The write is made holding mu, so
// state.db's order is always the order here: two agents queueing at once
// can't land in it the other way round. Call it holding mu.
func (m *Manager) queueLocked(w slotWait) {
	if m.queuedAtLocked(w.agent) >= 0 {
		return
	}
	m.wantSlot = append(m.wantSlot, w)
	// Not the base context: Stop cancels it, and a place taken while the
	// daemon drains must still be there when it starts again.
	if err := m.opts.State.Queue.PushFront(context.Background(), w.pool, w.ticket); err != nil {
		m.opts.Logger.Warn("agents: recording a slot queue place", "agent", w.agent, "err", err)
	}
}

// dequeueLocked takes id out of the slot queue, and state.db's. Call it
// holding mu.
func (m *Manager) dequeueLocked(id ident.AgentID) {
	i := m.queuedAtLocked(id)
	if i < 0 {
		return
	}
	w := m.wantSlot[i]
	m.wantSlot = slices.Delete(m.wantSlot, i, i+1)
	if _, err := m.opts.State.Queue.Remove(context.Background(), w.ticket); err != nil {
		m.opts.Logger.Warn("agents: removing a slot queue place", "agent", id, "err", err)
	}
}

func (m *Manager) queuedAtLocked(id ident.AgentID) int {
	return slices.IndexFunc(m.wantSlot, func(w slotWait) bool { return w.agent == id })
}

// aheadLocked reports whether an agent of pool is queued ahead of id, or,
// for id "", queued at all. Call it holding mu.
func (m *Manager) aheadLocked(pool state.Pool, id ident.AgentID) bool {
	for _, w := range m.wantSlot {
		if w.agent == id {
			return false
		}
		if w.pool == pool {
			return true
		}
	}
	return false
}

// loadQueue rebuilds the slot queue from state.db, before any agent is
// resumed: resumes run at once, and without it the first to finish would
// take the slot, whatever its place. A place whose ticket has no live
// agent, or one a person has attached (an attached agent holds a slot of
// its own), is dropped.
func (m *Manager) loadQueue(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, pool := range []state.Pool{state.PoolNormal, state.PoolUrgent} {
		places, err := m.opts.State.Queue.List(ctx, pool)
		if err != nil {
			return err
		}
		for _, q := range places {
			a, err := m.opts.State.Agents.LiveForTicket(ctx, q.Ticket)
			switch {
			case errors.Is(err, state.ErrNotFound) || (err == nil && a.Mode == state.ModeAttached):
				if _, err := m.opts.State.Queue.Remove(ctx, q.Ticket); err != nil {
					return err
				}
				continue
			case err != nil:
				return err
			}
			m.wantSlot = append(m.wantSlot, slotWait{agent: a.ID, ticket: a.Ticket, pool: a.Pool})
		}
	}
	return nil
}
