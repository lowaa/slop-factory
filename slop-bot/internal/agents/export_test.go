package agents

import (
	"context"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/worktree"
)

// SweepLeases runs one expiry sweep now, so a test needn't wait for the
// sweeper's tick to know it has run.
func (m *Manager) SweepLeases(ctx context.Context) { m.sweepLeases(ctx) }

// Render is what an agent reads for msgs.
func Render(msgs []state.Message) string { return render(msgs) }

// RemovalAdvice is how the user is told to remove a kept worktree.
func RemovalAdvice(ticket ident.TicketID, res *worktree.CleanupResult) string {
	return removalAdvice(ticket, res)
}

// EventPayload is what the event log records of a harness event.
func EventPayload(ev *harness.Event) map[string]any { return eventPayload(ev) }
