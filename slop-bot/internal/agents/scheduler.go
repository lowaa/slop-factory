package agents

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

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
	reconcile := m.opts.Clock.NewTicker(ReconcileEvery)
	defer reconcile.Stop()
	syncs := &syncState{pending: map[ident.TicketID]bool{}}
	for {
		select {
		case <-m.stop:
			return
		case <-m.kick:
		case <-m.wake:
			m.wake = nil
		case <-syncs.retry:
			syncs.retry = nil
			m.syncPending(ctx, syncs)
		case <-reconcile.C():
			m.reconcile(ctx, syncs)
		case ev, ok := <-feed:
			if !ok {
				feed = nil
				continue
			}
			syncs.pending[ev.Ticket] = true
			syncs.cursor, syncs.unsaved = ev.Cursor, true
			m.syncPending(ctx, syncs)
		}
		m.evaluate(ctx)
	}
}

// CursorKey is the ticket feed's entry in state.db's cursors.
const CursorKey = "tickets"

// How a failed sync is tried again, and how often every live agent is
// synced whatever the feed brought.
const (
	syncRetryFirst = time.Second
	// SyncRetryMax caps the backoff between a failed sync's retries.
	SyncRetryMax = time.Minute
	// ReconcileEvery is how often every live agent is synced with its
	// ticket: the feed is the fast path, this the catch-all for an event
	// a backend lost or never sent.
	ReconcileEvery = 5 * time.Minute
)

// EventSyncFailed is a ticket whose agent couldn't be brought in step with
// it, to be tried again.
const EventSyncFailed = "ticket.sync_failed"

// syncState is the scheduler goroutine's own record of the feed: tickets
// still to sync, and the newest cursor not yet saved. The cursor is saved
// only once nothing is pending, so a restart replays every event whose
// sync hadn't got through: the feed is at-least-once only if a failure
// doesn't move it on.
type syncState struct {
	pending map[ident.TicketID]bool
	cursor  tickets.Cursor
	unsaved bool
	backoff time.Duration
	retry   <-chan time.Time
}

// syncPending syncs every pending ticket, then saves the cursor if they all
// went through, or arms a retry with backoff if any didn't. The failures
// are recorded last, so whoever sees one sees the pass's outcome.
func (m *Manager) syncPending(ctx context.Context, s *syncState) {
	failed := map[ident.TicketID]error{}
	for id := range s.pending {
		if err := m.sync(ctx, id); err != nil {
			failed[id] = err
			continue
		}
		delete(s.pending, id)
	}
	if len(s.pending) == 0 {
		s.backoff, s.retry = 0, nil
		if s.unsaved {
			if err := m.opts.State.Cursors.Set(ctx, CursorKey, string(s.cursor)); err != nil {
				m.opts.Logger.Warn("agents: saving the ticket feed cursor", "err", err)
			} else {
				s.unsaved = false
			}
		}
		return
	}
	if ctx.Err() != nil {
		return // stopping: the cursor stays where it was, for the next start
	}
	s.backoff = min(max(2*s.backoff, syncRetryFirst), SyncRetryMax)
	if s.retry == nil {
		s.retry = m.opts.Clock.After(s.backoff)
	}
	for id, err := range failed {
		m.opts.Logger.Warn("agents: keeping an agent in step with its ticket", "ticket", id, "err", err, "retry_in", s.backoff)
		m.event(ctx, EventSyncFailed, "", id, map[string]string{"error": err.Error(), "retry_in": s.backoff.String()})
	}
}

// reconcile syncs every live agent with its ticket, as if each ticket had
// sent an event.
func (m *Manager) reconcile(ctx context.Context, s *syncState) {
	live, err := m.opts.State.Agents.ListLive(ctx)
	if err != nil {
		if ctx.Err() == nil {
			m.opts.Logger.Warn("agents: listing live agents to reconcile", "err", err)
		}
		return
	}
	for _, a := range live {
		s.pending[a.Ticket] = true
	}
	m.syncPending(ctx, s)
}

// sync keeps a ticket's live agent in step with the ticket as it now is,
// whatever event brought it here: the feed is at-least-once, and an event
// says only that something changed, so every step compares with what the
// agent already has. A finished ticket ends its agent, running or not; a
// running agent hears when its ticket's blockers change, gets worktrees for
// repos added since it started, and its work starts once the ticket is open
// and unblocked.
//
// It fails when it couldn't read what it compares, so the caller can try
// again: what goes wrong after that (a notice that didn't send) is logged.
func (m *Manager) sync(ctx context.Context, ticket ident.TicketID) error {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	if err := m.revokeMergeIfEnded(ctx, ticket); err != nil {
		return err
	}
	a, err := m.opts.State.Agents.LiveForTicket(ctx, ticket)
	switch {
	case errors.Is(err, state.ErrNotFound):
		return nil
	case err != nil:
		return err
	}
	t, err := m.opts.Tickets.Get(ctx, ticket)
	switch {
	case errors.Is(err, tickets.ErrNotFound):
		// Never there to compare with: a retry would only hold the feed
		// cursor back for good.
		m.opts.Logger.Warn("agents: a live agent's ticket isn't in the ticketing system", "agent", a.ID, "ticket", ticket)
		return nil
	case err != nil:
		return err
	}
	r, running := m.runner(a.ID)
	// One already finishing (its own done included) is left to it.
	if running && r.isEnding() {
		return nil
	}
	if t.Status.Terminal() {
		m.endForTicket(ctx, a, r, t.Status)
		return nil
	}
	// An agent being started or recovered is synced once that settles.
	if !running {
		m.syncParked(ctx, a, t)
		return nil
	}
	m.syncBlocked(ctx, r, a, t)
	m.syncWorktrees(ctx, r, a, t)
	if t.Status == tickets.StatusOpen && len(t.BlockedBy) == 0 {
		if err := m.advanceTo(ctx, ident.AgentPrincipal(a.ID, a.Ticket), t.ID, tickets.StatusInProgress); err != nil {
			m.opts.Logger.Debug("agents: starting a ticket's work", "ticket", t.ID, "err", err)
		}
	}
	return nil
}

// syncNow is sync for a start that has just brought an agent up: a failure
// is only logged, since the scheduler's periodic pass syncs it again.
func (m *Manager) syncNow(ctx context.Context, ticket ident.TicketID) {
	if err := m.sync(ctx, ticket); err != nil && ctx.Err() == nil {
		m.opts.Logger.Warn("agents: keeping a started agent in step with its ticket", "ticket", ticket, "err", err)
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
// so a new worktree is primary only for an agent that had none, and even
// then a resume runs where the conversation started (see workspace).
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
	waiting := make([]ident.AgentID, len(m.wantSlot))
	for i, w := range m.wantSlot {
		waiting[i] = w.agent
	}
	m.mu.Unlock()
	// Agents with an answer to act on go ahead of new work, parked ones
	// too.
	for _, id := range waiting {
		if r, ok := m.runner(id); ok {
			m.deliverPending(ctx, r)
			continue
		}
		m.unparkWhenServed(id)
		m.dropStray(id)
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
	return slices.DeleteFunc(ts, func(t *tickets.Ticket) bool { return !m.wantsAgent(ctx, t) })
}

// wantsAgent reports whether t, as read, should have an agent started:
// being prepared, or open and unblocked, with no live agent, and not taken
// over by a person.
func (m *Manager) wantsAgent(ctx context.Context, t *tickets.Ticket) bool {
	switch {
	case t.Status != tickets.StatusPreparing && t.Status != tickets.StatusOpen:
		return false
	case t.Status == tickets.StatusOpen && len(t.BlockedBy) > 0:
		return false
	}
	if _, err := m.opts.State.Agents.LiveForTicket(ctx, t.ID); err == nil {
		return false
	}
	// A person took the ticket over with `attach --no-resume`.
	released, err := m.opts.State.Agents.ReleasedForTicket(ctx, t.ID)
	return err == nil && !released
}

func (m *Manager) admits(pool state.Pool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.admitsLocked(pool)
}

// admitsLocked reports whether new work may start in pool now (see
// admitLocked): past max_live, only if an idle agent can be parked for it.
// Call it holding mu.
func (m *Manager) admitsLocked(pool state.Pool) bool {
	ok, _ := m.admitLocked(pool)
	return ok
}

// slotFreeLocked reports whether pool has room for one more agent in a
// turn, not counting except (an agent asking for a slot for itself), nor
// the start of exceptTicket (that agent's own resume, which holds a slot
// until it settles). An agent being started holds a slot. Call it holding
// mu.
func (m *Manager) slotFreeLocked(pool state.Pool, except ident.AgentID, exceptTicket ident.TicketID) bool {
	limit := m.opts.Config.Concurrency.MaxActive
	if pool == state.PoolUrgent {
		limit = m.opts.Config.Concurrency.Urgent.MaxActive
	}
	n := 0
	for ticket, p := range m.starting {
		if p == pool && (exceptTicket == "" || ticket != exceptTicket) {
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
// at the front waits even when a slot is free, and joins the queue. So
// does one being parked, which is resumed for its turn.
func (m *Manager) takeSlot(r *runner) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	ahead := m.aheadLocked(r.pool, r.id)
	r.mu.Lock()
	// An agent handed back from an attach brings the slot it held.
	held := r.holdsSlot
	r.holdsSlot = false
	parking := r.parking
	r.mu.Unlock()
	if !parking && (held || (!ahead && m.slotFreeLocked(r.pool, r.id, r.ticket))) {
		m.dequeueLocked(r.id)
		r.mu.Lock()
		// Counted as active from now, so a second taker sees the slot gone
		// before the harness reports the turn.
		r.inTurn = true
		r.mu.Unlock()
		return true
	}
	m.queueLocked(slotWait{agent: r.id, ticket: r.ticket, pool: r.pool})
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
	m.dequeueLocked(id)
}

func (m *Manager) autonomy(ctx context.Context) bool {
	on, ok, err := m.opts.State.Settings.Autonomy(ctx)
	if err != nil || !ok {
		return m.opts.Config.Autonomy.Enabled
	}
	return on
}
