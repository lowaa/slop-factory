package agents

import (
	"context"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/worktree"
)

// SweepLeases runs one expiry sweep now, so a test needn't wait for the
// sweeper's tick to know it has run.
func (m *Manager) SweepLeases(ctx context.Context) { m.sweepLeases(ctx) }

// RemindAsks runs one ask-reminder sweep now, as SweepLeases does.
func (m *Manager) RemindAsks(ctx context.Context) { m.remindAsks(ctx) }

// IsParked reports whether the agent is parked to free a process.
func (m *Manager) IsParked(id ident.AgentID) bool { return m.isParked(id) }

// Render is what an agent reads for msgs.
func Render(msgs []state.Message) string { return render(msgs) }

// RemovalAdvice is how the user is told to remove a kept worktree.
func RemovalAdvice(res *worktree.CleanupResult) string { return removalAdvice(res) }
