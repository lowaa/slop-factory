package agents

import (
	"context"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
	"github.com/lowaa/slop-factory/slop-bot/internal/worktree"
)

// SweepLeases runs one expiry sweep now, so a test needn't wait for the
// sweeper's tick to know it has run.
func (m *Manager) SweepLeases(ctx context.Context) { m.sweepLeases(ctx) }

// RemindAsks runs one ask-reminder sweep now, as SweepLeases does.
func (m *Manager) RemindAsks(ctx context.Context) { m.remindAsks(ctx) }

// IsParked reports whether the agent is parked to free a process.
func (m *Manager) IsParked(id ident.AgentID) bool { return m.isParked(id) }

// HeldRetry is heldRetry, for a test to move the clock by.
const HeldRetry = heldRetry

// Render is what an agent reads for msgs.
func Render(msgs []state.Message) string { return render(msgs) }

// RemovalAdvice is how the user is told to remove a kept worktree.
func RemovalAdvice(ticket ident.TicketID, res *worktree.CleanupResult) string {
	return removalAdvice(ticket, res)
}

// EventPayload is what the event log records of a harness event.
func EventPayload(ev *harness.Event) map[string]any { return eventPayload(ev) }

// OnStartBusy makes f run whenever a start on the caller's goroutine (a
// hand-back's resume) finds its ticket's start place taken. Set it before
// starting what it watches.
func (m *Manager) OnStartBusy(f func(ident.TicketID)) { m.startBusy = f }

// RevokeMerge is revokeMerge, so a test can make its write fail without a
// ticket's whole life around it.
func (m *Manager) RevokeMerge(ctx context.Context, ticket ident.TicketID) { m.revokeMerge(ctx, ticket) }

// EnqueueNotice queues a notice for the running agent id, reporting whether
// it was queued.
func (m *Manager) EnqueueNotice(ctx context.Context, id ident.AgentID, text string) bool {
	r, ok := m.runner(id)
	if !ok {
		return false
	}
	return m.enqueueNotice(ctx, r, text)
}

// Recover is recover: the harness of agent id died.
func (m *Manager) Recover(ctx context.Context, id ident.AgentID) { m.recover(ctx, id) }

// CancelBase is what Stop does first to the starts in flight, without
// draining anything.
func (m *Manager) CancelBase() { m.cancelBase() }

// AdvanceTo is advanceTo.
func (m *Manager) AdvanceTo(ctx context.Context, by ident.Principal, id ident.TicketID, target tickets.Status) error {
	return m.advanceTo(ctx, by, id, target)
}

// SpawnAgent is spawnAgent.
func (m *Manager) SpawnAgent(ctx context.Context, t *tickets.Ticket, pool state.Pool, spec tickets.AgentSpec) error {
	return m.spawnAgent(ctx, t, pool, spec)
}

// Sync is sync: it keeps the ticket's agent in step with it, now.
func (m *Manager) Sync(ctx context.Context, ticket ident.TicketID) error { return m.sync(ctx, ticket) }

// RecoverWhenStartSettles holds ticket's start place until hold is closed,
// and asks for agent id, whose harness just died, to be recovered meanwhile:
// the recovery has to wait for that start (recoverAfter) and runs when it
// settles. It reports whether the place was free to hold.
func (m *Manager) RecoverWhenStartSettles(id ident.AgentID, ticket ident.TicketID, hold <-chan struct{}) bool {
	held := m.goStart(ticket, state.PoolNormal, false, func(context.Context) { <-hold })
	m.goRecover(&runner{id: id, ticket: ticket, pool: state.PoolNormal})
	return held
}

// WaitStarts waits for every start in flight, and any a settling one began.
func (m *Manager) WaitStarts() { m.starts.Wait() }
