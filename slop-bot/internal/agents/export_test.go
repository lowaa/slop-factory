package agents

import (
	"context"

	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

// SweepLeases runs one expiry sweep now, so a test needn't wait for the
// sweeper's tick to know it has run.
func (m *Manager) SweepLeases(ctx context.Context) { m.sweepLeases(ctx) }

// Render is what an agent reads for msgs.
func Render(msgs []state.Message) string { return render(msgs) }
