package agents

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
	"github.com/lowaa/slop-factory/slop-bot/internal/worktree"
)

// schedule runs until Stop: on every kick and every ticket event it gives
// waiting agents their slots, starts agents for eligible tickets, and keeps
// tickets' statuses in step with their agents.
func (m *Manager) schedule() {
	defer close(m.done)
	ctx := m.base
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
			m.sync(ctx, ev.Ticket)
			if err := m.opts.State.Cursors.Set(ctx, CursorKey, string(ev.Cursor)); err != nil {
				m.opts.Logger.Warn("agents: saving the ticket feed cursor", "err", err)
			}
		}
		m.evaluate(ctx)
	}
}

// CursorKey is the ticket feed's entry in state.db's cursors.
const CursorKey = "tickets"

// sync keeps a ticket's live agent in step with the ticket as it now is,
// whatever event brought it here: the feed is at-least-once, and an event
// says only that something changed, so every step compares with what the
// agent already has. A finished ticket ends its agent, running or not; a
// running agent hears when its ticket's blockers change, gets worktrees for
// repos added since it started, and its work starts once the ticket is open
// and unblocked.
func (m *Manager) sync(ctx context.Context, ticket ident.TicketID) {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	a, err := m.opts.State.Agents.LiveForTicket(ctx, ticket)
	if err != nil {
		return
	}
	t, err := m.opts.Tickets.Get(ctx, ticket)
	if err != nil {
		return
	}
	r, running := m.runner(a.ID)
	// One already finishing (its own done included) is left to it.
	if running && r.isEnding() {
		return
	}
	if t.Status.Terminal() {
		m.endForTicket(ctx, a, r, t.Status)
		return
	}
	// An agent being started or recovered is synced once that settles.
	if !running {
		return
	}
	m.syncBlocked(ctx, r, a, t)
	m.syncWorktrees(ctx, r, a, t)
	if t.Status == tickets.StatusOpen && len(t.BlockedBy) == 0 {
		if err := m.advanceTo(ctx, ident.AgentPrincipal(a.ID, a.Ticket), t.ID, tickets.StatusInProgress); err != nil {
			m.opts.Logger.Debug("agents: starting a ticket's work", "ticket", t.ID, "err", err)
		}
	}
}

// endForTicket ends the agent of a ticket that is done or cancelled, by
// whoever: its turn is cut off, since the work is no longer wanted. One
// with no runner (dead, or never resumed) is ended from its record.
func (m *Manager) endForTicket(ctx context.Context, a *state.Agent, r *runner, st tickets.Status) {
	reason := endReason(st)
	switch {
	case r != nil:
		m.end(ctx, r, true, reason)
	case !m.isStarting(a.Ticket):
		m.endRecord(ctx, a, state.StateDone, reason)
	}
}

func endReason(st tickets.Status) string {
	if st == tickets.StatusDone {
		return "the ticket was marked done"
	}
	return "the ticket was cancelled"
}

// syncBlocked tells the agent when its ticket's blockers change between
// some and none, as a notice from slop-bot, not the user: it mustn't carry
// the user's authority, or answer the agent's questions.
func (m *Manager) syncBlocked(ctx context.Context, r *runner, a *state.Agent, t *tickets.Ticket) {
	blocked := len(t.BlockedBy) > 0
	if blocked == a.BlockedTold {
		return
	}
	var text string
	switch {
	case blocked && t.Status == tickets.StatusPreparing:
		text = fmt.Sprintf("Your ticket is now blocked by %s. Carry on refining it, but don't start the work: "+
			"end your turn when the refining is done, and slop-bot tells you when the blockers are.", refs(t.BlockedBy))
	case blocked:
		text = fmt.Sprintf("Your ticket is now blocked by %s: stop the work at a safe point and end your turn. "+
			"slop-bot tells you when the blockers are done.", refs(t.BlockedBy))
	case t.Status == tickets.StatusPreparing:
		text = "Your ticket's blockers are done: finish refining it, then do the work."
	default:
		text = "Your ticket's blockers are done: carry on with the work."
	}
	// The notice first, then the record: a lost record repeats a notice,
	// where the other order could lose one.
	m.notify(ctx, r, text)
	if err := m.opts.State.Agents.SetBlockedTold(ctx, a.ID, blocked); err != nil {
		m.opts.Logger.Warn("agents: recording a blocker notice", "agent", a.ID, "err", err)
	}
}

// syncWorktrees gives a running agent a worktree for each repo added to its
// ticket since it started, and tells it where they are. Its cwd can't move,
// so a new worktree is primary only for an agent that had none; a resume
// then starts it there.
func (m *Manager) syncWorktrees(ctx context.Context, r *runner, a *state.Agent, t *tickets.Ticket) {
	have := map[string]bool{}
	primary := false
	for _, w := range a.Worktrees {
		have[w.Repo] = true
		primary = primary || w.Primary
	}
	if !slices.ContainsFunc(t.Repos, func(repo string) bool { return !have[repo] }) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, m.startTimeout())
	defer cancel()
	// Create is idempotent, so it is handed every repo, in the ticket's
	// order, and only the new ones are recorded.
	wts, err := m.opts.Worktrees.Create(ctx, t.ID, t.Repos, t.Repos[0])
	var added []*worktree.Worktree
	for _, w := range wts {
		if have[w.Repo] {
			continue
		}
		w.Primary = w.Primary && !primary
		primary = primary || w.Primary
		if err := m.opts.State.Agents.AddWorktree(ctx, a.ID, &state.Worktree{Repo: w.Repo, Path: w.Path, Branch: w.Branch, Primary: w.Primary}); err != nil {
			m.opts.Logger.Warn("agents: recording a worktree", "agent", a.ID, "repo", w.Repo, "err", err)
			continue
		}
		added = append(added, w)
	}
	if err != nil {
		m.tellUser(ctx, a.ID, fmt.Sprintf("couldn't make the worktrees for ticket %s's new repos: %v", t.ID, err))
	}
	if len(added) == 0 {
		return
	}
	text := "Your ticket now also touches these repos. Work in their worktrees, never in the main checkouts:"
	for _, w := range added {
		text += fmt.Sprintf("\n- %s: %s (branch %s)", w.Repo, w.Path, w.Branch)
	}
	m.notify(ctx, r, text)
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
		// when a slot has freed since the queue was served (goStart checks
		// again, as it claims the place).
		if !m.admits(pool) {
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
		// Off the scheduler goroutine: git and a harness's handshake can be
		// slow, and must not hold up every other ticket's events.
		m.goStart(t.ID, pool, true, func(ctx context.Context) { m.spawn(ctx, t, pool, spec) })
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
		if ctx.Err() == nil {
			m.opts.Logger.Warn("agents: listing tickets", "err", err)
		}
		return nil
	}
	return slices.DeleteFunc(ts, func(t *tickets.Ticket) bool {
		if t.Status == tickets.StatusOpen && len(t.BlockedBy) > 0 {
			return true
		}
		if _, err := m.opts.State.Agents.LiveForTicket(ctx, t.ID); err == nil {
			return true
		}
		// A person took the ticket over with `attach --no-resume`.
		released, err := m.opts.State.Agents.ReleasedForTicket(ctx, t.ID)
		return err != nil || released
	})
}

func (m *Manager) admits(pool state.Pool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.admitsLocked(pool)
}

// admitsLocked reports whether new work may start in pool now: no agent in
// it is queued for a slot (new work never jumps one, even when a slot has
// freed since the queue was served), it has a free slot, and max_live has
// room. Call it holding mu.
func (m *Manager) admitsLocked(pool state.Pool) bool {
	return !m.queuedLocked(pool) && m.slotFreeLocked(pool, "") &&
		len(m.runners)+len(m.starting) < m.opts.Config.Concurrency.MaxLive
}

// slotFreeLocked reports whether pool has room for one more agent in a
// turn, not counting except (an agent asking for a slot for itself). An
// agent being started holds a slot. Call it holding mu.
func (m *Manager) slotFreeLocked(pool state.Pool, except ident.AgentID) bool {
	limit := m.opts.Config.Concurrency.MaxActive
	if pool == state.PoolUrgent {
		limit = m.opts.Config.Concurrency.Urgent.MaxActive
	}
	n := 0
	for _, p := range m.starting {
		if p == pool {
			n++
		}
	}
	for id, r := range m.runners {
		if id != except && r.pool == pool && r.active() {
			n++
		}
	}
	return n < limit
}

// takeSlot claims a slot for an idle agent about to take a turn, as one
// step under mu, so two takers can't both claim the last slot. Agents
// queued for a slot are served in order, ahead of new work: one that isn't
// at the front waits even when a slot is free, and joins the queue.
func (m *Manager) takeSlot(r *runner) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	r.mu.Lock()
	// An agent handed back from an attach brings the slot it held.
	held := r.holdsSlot
	r.holdsSlot = false
	r.mu.Unlock()
	if held || (!ahead && m.slotFreeLocked(r.pool, r.id)) {
		m.wantSlot = slices.DeleteFunc(m.wantSlot, func(id ident.AgentID) bool { return id == r.id })
		r.mu.Lock()
		// Counted as active from now, so a second taker sees the slot gone
		// before the harness reports the turn.
		r.inTurn = true
		r.mu.Unlock()
		return true
	}
	if !slices.Contains(m.wantSlot, r.id) {
		m.wantSlot = append(m.wantSlot, r.id)
	}
	return false
}

// releaseSlot gives back a slot takeSlot claimed for a turn that never
// started.
func (m *Manager) releaseSlot(r *runner) {
	r.mu.Lock()
	r.inTurn = false
	r.mu.Unlock()
	m.Kick()
}

// unqueue takes an agent with nothing left to deliver out of the slot
// queue, so it can't hold up its pool.
func (m *Manager) unqueue(id ident.AgentID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wantSlot = slices.DeleteFunc(m.wantSlot, func(w ident.AgentID) bool { return w == id })
}

// queuedLocked reports whether an agent in pool is waiting for a slot.
// Call it holding mu.
func (m *Manager) queuedLocked(pool state.Pool) bool {
	for _, id := range m.wantSlot {
		if r, ok := m.runners[id]; ok && r.pool == pool {
			return true
		}
	}
	return false
}

func (m *Manager) autonomy(ctx context.Context) bool {
	on, ok, err := m.opts.State.Settings.Autonomy(ctx)
	if err != nil || !ok {
		return m.opts.Config.Autonomy.Enabled
	}
	return on
}
