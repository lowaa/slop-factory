package agents

import "github.com/lowaa/slop-factory/slop-bot/internal/state"

// PoolUsage is one pool's agents in a turn against its limit.
type PoolUsage struct {
	Pool   state.Pool
	Active int
	Max    int
}

// PoolUsage reports every pool's use, normal first. It counts as slotFree does.
func (m *Manager) PoolUsage() []PoolUsage {
	out := []PoolUsage{
		{Pool: state.PoolNormal, Max: m.opts.Config.Concurrency.MaxActive},
		{Pool: state.PoolUrgent, Max: m.opts.Config.Concurrency.Urgent.MaxActive},
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.runners {
		if !r.active() {
			continue
		}
		for i := range out {
			if out[i].Pool == r.pool {
				out[i].Active++
			}
		}
	}
	return out
}
