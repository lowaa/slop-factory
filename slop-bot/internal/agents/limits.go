package agents

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/lowaa/slop-factory/slop-bot/internal/agentspec"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

// HarnessLimit is a harness's plan allowance as it last reported it, for
// `slop-bot status`.
type HarnessLimit struct {
	Harness     string
	UsedPercent float64
	// ResetsAt is when the window resets; zero when the harness didn't say.
	ResetsAt   time.Time
	ReportedAt time.Time
	// Threshold is the configured limit_threshold, and Held says the
	// harness is at it: no new agent starts on it until ResetsAt.
	Threshold float64
	Held      bool
}

// limits tracks each harness's latest plan allowance. Every agent on a
// harness draws on the same plan, so the latest report from any of them is
// the harness's.
type limits struct {
	mu     sync.Mutex
	latest map[string]state.HarnessLimit
	// toldHeld is the reset each held harness was last reported to the
	// user for, so a hold is announced once, not on every scheduling pass.
	toldHeld map[string]time.Time
	// heldTickets is the reset each held ticket's hold event was recorded
	// for.
	heldTickets map[ident.TicketID]time.Time
}

func newLimits() *limits {
	return &limits{latest: map[string]state.HarnessLimit{}, toldHeld: map[string]time.Time{}, heldTickets: map[ident.TicketID]time.Time{}}
}

// loadLimits restores the limits a previous run recorded, so a restarted
// daemon doesn't start agents on a harness that is still at its limit.
func (m *Manager) loadLimits(ctx context.Context) error {
	saved, err := m.opts.State.Settings.HarnessLimits(ctx)
	if err != nil {
		return err
	}
	m.limits.mu.Lock()
	defer m.limits.mu.Unlock()
	for _, l := range saved {
		m.limits.latest[l.Harness] = l
	}
	return nil
}

// reportLimit records a harness's report, and has the scheduler look again:
// a lower figure may release a hold.
func (m *Manager) reportLimit(ctx context.Context, r *runner, l *harness.Limit) {
	hl := state.HarnessLimit{Harness: r.harness, UsedPercent: l.UsedPercent, ResetsAt: l.ResetsAt, ReportedAt: m.opts.Clock.Now()}
	m.limits.mu.Lock()
	m.limits.latest[r.harness] = hl
	m.limits.mu.Unlock()
	if err := m.opts.State.Settings.SetHarnessLimit(ctx, &hl); err != nil {
		m.opts.Logger.Warn("agents: recording a harness limit", "harness", r.harness, "err", err)
	}
	m.Kick()
}

// heldUntil reports whether name is at its limit, and when that ends. A
// window with no reset time holds until a lower report arrives.
func (m *Manager) heldUntil(name string) (time.Time, bool) {
	m.limits.mu.Lock()
	l, ok := m.limits.latest[name]
	m.limits.mu.Unlock()
	if !ok {
		return time.Time{}, false
	}
	threshold := float64(100)
	if hc := m.opts.Config.Harnesses[name]; hc != nil {
		threshold = hc.LimitThreshold
	}
	if l.UsedPercent < threshold || (!l.ResetsAt.IsZero() && !m.opts.Clock.Now().Before(l.ResetsAt)) {
		return time.Time{}, false
	}
	return l.ResetsAt, true
}

// Limits is every harness's latest reported allowance, by name.
func (m *Manager) Limits() []HarnessLimit {
	m.limits.mu.Lock()
	names := make([]string, 0, len(m.limits.latest))
	for n := range m.limits.latest {
		names = append(names, n)
	}
	m.limits.mu.Unlock()
	slices.Sort(names)
	out := make([]HarnessLimit, 0, len(names))
	for _, n := range names {
		m.limits.mu.Lock()
		l := m.limits.latest[n]
		m.limits.mu.Unlock()
		hl := HarnessLimit{Harness: n, UsedPercent: l.UsedPercent, ResetsAt: l.ResetsAt, ReportedAt: l.ReportedAt}
		if hc := m.opts.Config.Harnesses[n]; hc != nil {
			hl.Threshold = hc.LimitThreshold
		}
		_, hl.Held = m.heldUntil(n)
		out = append(out, hl)
	}
	return out
}

// errHeld is a ticket that can't start yet: its harness is at its limit.
type errHeld struct {
	harness string
	resets  time.Time
}

func (e *errHeld) Error() string {
	if e.resets.IsZero() {
		return fmt.Sprintf("%s is at its plan limit", e.harness)
	}
	return fmt.Sprintf("%s is at its plan limit until %s", e.harness, e.resets.Format(time.RFC3339))
}

// specFor is the agent spec t starts with. When its harness is at its
// limit, a ticket that left the harness (and model) blank and has never
// been started goes to another configured harness that isn't; any other
// ticket waits. A ticket that names a harness chose it, and a started
// ticket's conversation lives in its harness, so neither is moved. The
// model must be blank too: a model is one harness's.
func (m *Manager) specFor(ctx context.Context, t *tickets.Ticket) (tickets.AgentSpec, error) {
	spec := agentspec.Resolve(t.Agent, m.opts.Config)
	resets, held := m.heldUntil(spec.Harness)
	if !held {
		return spec, nil
	}
	hold := &errHeld{harness: spec.Harness, resets: resets}
	if t.Agent.Harness != "" || t.Agent.Model != "" {
		return spec, hold
	}
	if _, err := m.opts.State.Agents.LatestForTicket(ctx, t.ID); err == nil {
		return spec, hold
	}
	names := make([]string, 0, len(m.opts.Config.Harnesses))
	for n := range m.opts.Config.Harnesses {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		if n == spec.Harness {
			continue
		}
		if _, ok := m.opts.Harnesses.Harness(n); !ok {
			continue
		}
		if _, held := m.heldUntil(n); held {
			continue
		}
		alt := spec
		alt.Harness = n
		if agentspec.Validate(alt, m.opts.Config) == nil {
			return alt, nil
		}
	}
	return spec, hold
}

// noteHeld records, once per hold, that t is waiting on its harness's
// limit, and tells the user once per harness.
func (m *Manager) noteHeld(ctx context.Context, t *tickets.Ticket, hold *errHeld) {
	m.limits.mu.Lock()
	prev, ticketNoted := m.limits.heldTickets[t.ID]
	ticketNoted = ticketNoted && prev.Equal(hold.resets)
	m.limits.heldTickets[t.ID] = hold.resets
	prev, userTold := m.limits.toldHeld[hold.harness]
	userTold = userTold && prev.Equal(hold.resets)
	m.limits.toldHeld[hold.harness] = hold.resets
	m.limits.mu.Unlock()
	if !ticketNoted {
		m.event(ctx, EventHeld, "", t.ID, map[string]any{"harness": hold.harness, "resets_at": hold.resets})
	}
	if !userTold {
		m.tellUser(ctx, "", fmt.Sprintf("%s: new agents on it wait (`slop-bot status` shows the allowance)", hold.Error()))
	}
}

// wakeAt has the scheduler look again at t, when a held harness's window
// resets. An earlier wake already set stands: that pass sets the next.
// Only the scheduler goroutine calls it.
func (m *Manager) wakeAt(t time.Time) {
	if t.IsZero() || (m.wake != nil && !t.Before(m.wakeTime)) {
		return
	}
	m.wakeTime = t
	m.wake = m.opts.Clock.After(t.Sub(m.opts.Clock.Now()))
}
