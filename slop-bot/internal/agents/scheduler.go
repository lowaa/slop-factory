package agents

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

// schedule runs until Stop: on every kick and every ticket event it gives
// waiting agents their slots, starts agents for eligible tickets, and keeps
// tickets' statuses in step with their agents.
func (m *Manager) schedule() {
	defer close(m.done)
	ctx := context.Background()
	feed := m.opts.Tickets.Events()
	for {
		select {
		case <-m.stop:
			return
		case <-m.kick:
		case <-m.wake:
			m.wake = nil
		case ev, ok := <-feed:
			if !ok {
				feed = nil
				continue
			}
			m.onTicketEvent(ctx, &ev)
			if err := m.opts.State.Cursors.Set(ctx, CursorKey, string(ev.Cursor)); err != nil {
				m.opts.Logger.Warn("agents: saving the ticket feed cursor", "err", err)
			}
		}
		m.evaluate(ctx)
	}
}

// CursorKey is the ticket feed's entry in state.db's cursors.
const CursorKey = "tickets"

// onTicketEvent keeps a live agent in step with its ticket: work starts
// once the ticket is open and unblocked.
func (m *Manager) onTicketEvent(ctx context.Context, ev *tickets.TicketEvent) {
	a, err := m.opts.State.Agents.LiveForTicket(ctx, ev.Ticket)
	if err != nil {
		return
	}
	t, err := m.opts.Tickets.Get(ctx, ev.Ticket)
	if err != nil {
		return
	}
	switch {
	case t.Status == tickets.StatusOpen && len(t.BlockedBy) == 0:
		m.advanceTo(ctx, ident.AgentPrincipal(a.ID, a.Ticket), t.ID, tickets.StatusInProgress)
		if ev.Kind == tickets.EventUnblocked {
			_, _ = m.Tell(ctx, ident.UserPrincipal(), string(a.ID), "Your ticket's blockers are done: start the work.", false)
		}
	case t.Status == tickets.StatusOpen && ev.Kind == tickets.EventUpdated:
		_, _ = m.Tell(ctx, ident.UserPrincipal(), string(a.ID),
			fmt.Sprintf("Your ticket is still blocked by %s: don't start the work yet. End your turn; you'll be told when the blockers are done.", refs(t.BlockedBy)), false)
	case t.Status == tickets.StatusCancelled:
		if r, ok := m.runner(a.ID); ok {
			m.finish(ctx, r, "")
		}
	}
}

func (m *Manager) evaluate(ctx context.Context) {
	m.mu.Lock()
	if m.stopping {
		m.mu.Unlock()
		return
	}
	waiting := append([]ident.AgentID(nil), m.wantSlot...)
	m.mu.Unlock()
	// Agents with an answer to act on go ahead of new work.
	for _, id := range waiting {
		if r, ok := m.runner(id); ok {
			m.deliverPending(ctx, r)
		}
	}
	for _, t := range m.eligible(ctx) {
		pool := state.PoolNormal
		if t.Urgent {
			pool = state.PoolUrgent
		}
		// New work never jumps an agent already queued for a slot, even
		// when a slot has freed since the queue was served.
		if m.queued(pool) || !m.slotFree(pool, "") || m.liveCount() >= m.opts.Config.Concurrency.MaxLive {
			continue
		}
		spec, err := m.specFor(ctx, t)
		var hold *errHeld
		if errors.As(err, &hold) {
			m.wakeAt(hold.until)
			m.noteHeld(ctx, t, hold)
			continue
		}
		m.unheld(t.ID)
		if err := m.spawn(ctx, t, pool, spec); err != nil && !errors.Is(err, errMisconfigured) {
			m.opts.Logger.Warn("agents: spawning", "ticket", t.ID, "err", err)
		}
	}
}

// eligible is every ticket an agent should be started for, oldest first:
// assigned to slop-bot's user, being prepared or open, with no live agent.
// Blockers gate the work, not the refinement: a blocked open ticket waits.
func (m *Manager) eligible(ctx context.Context) []*tickets.Ticket {
	user := m.opts.User
	ts, err := m.opts.Tickets.List(ctx, &tickets.Query{
		Statuses: []tickets.Status{tickets.StatusPreparing, tickets.StatusOpen},
		Assignee: &user,
	})
	if err != nil {
		m.opts.Logger.Warn("agents: listing tickets", "err", err)
		return nil
	}
	return slices.DeleteFunc(ts, func(t *tickets.Ticket) bool {
		if t.Status == tickets.StatusOpen && len(t.BlockedBy) > 0 {
			return true
		}
		_, err := m.opts.State.Agents.LiveForTicket(ctx, t.ID)
		return err == nil
	})
}

// slotFree reports whether pool has room for one more agent in a turn,
// not counting except (an agent asking for a slot for itself).
func (m *Manager) slotFree(pool state.Pool, except ident.AgentID) bool {
	limit := m.opts.Config.Concurrency.MaxActive
	if pool == state.PoolUrgent {
		limit = m.opts.Config.Concurrency.Urgent.MaxActive
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, r := range m.runners {
		if id != except && r.pool == pool && r.active() {
			n++
		}
	}
	return n < limit
}

// takeSlot claims a slot for an idle agent about to take a turn. Agents
// queued for a slot are served in order, ahead of new work: one that isn't
// at the front waits even when a slot is free, and joins the queue.
func (m *Manager) takeSlot(r *runner) bool {
	m.mu.Lock()
	ahead := false
	for _, id := range m.wantSlot {
		if id == r.id {
			break
		}
		if other, ok := m.runners[id]; ok && other.pool == r.pool {
			ahead = true
			break
		}
	}
	m.mu.Unlock()
	if !ahead && m.slotFree(r.pool, r.id) {
		m.mu.Lock()
		m.wantSlot = slices.DeleteFunc(m.wantSlot, func(id ident.AgentID) bool { return id == r.id })
		m.mu.Unlock()
		r.mu.Lock()
		// Counted as active from now, so a second taker sees the slot gone
		// before the harness reports the turn.
		r.inTurn = true
		r.mu.Unlock()
		return true
	}
	m.mu.Lock()
	if !slices.Contains(m.wantSlot, r.id) {
		m.wantSlot = append(m.wantSlot, r.id)
	}
	m.mu.Unlock()
	return false
}

// queued reports whether an agent in pool is waiting for a slot.
func (m *Manager) queued(pool state.Pool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.wantSlot {
		if r, ok := m.runners[id]; ok && r.pool == pool {
			return true
		}
	}
	return false
}

func (m *Manager) liveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.runners)
}

func (m *Manager) autonomy(ctx context.Context) bool {
	on, ok, err := m.opts.State.Settings.Autonomy(ctx)
	if err != nil || !ok {
		return m.opts.Config.Autonomy.Enabled
	}
	return on
}
