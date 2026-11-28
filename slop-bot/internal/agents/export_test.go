package agents

import "context"

// SweepLeases runs one expiry sweep now, so a test needn't wait for the
// sweeper's tick to know it has run.
func (m *Manager) SweepLeases(ctx context.Context) { m.sweepLeases(ctx) }

// HeldRetry is heldRetry, for a test to move the clock by.
const HeldRetry = heldRetry
