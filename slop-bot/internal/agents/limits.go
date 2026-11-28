package agents

import (
	"cmp"
	"context"
	"errors"
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

// NoResetHold is how long a window at its limit holds when the harness gave
// no reset time. Only an agent on the harness can report a lower figure,
// and a held harness starts none, so an open-ended hold could last forever.
const NoResetHold = 30 * time.Minute

// HarnessLimit is one of a harness's plan windows as it last reported it,
// for `slop-bot status`.
type HarnessLimit struct {
	Harness string
	// Window names it within the harness, e.g. "five_hour".
	Window      string
	UsedPercent float64
	// ResetsAt is when the window resets; zero when the harness didn't say.
	ResetsAt   time.Time
	ReportedAt time.Time
	// Threshold is the harness's limit_threshold, and Held says this window
	// is at it: no new agent starts on the harness until it resets.
	Threshold float64
	Held      bool
}

// limitKey is one window of one harness. Every agent on a harness draws on
// the same plan, so the latest report of a window, from any of them, is the
// harness's.
type limitKey struct{ harness, window string }

// limits tracks each harness's latest report of each plan window.
type limits struct {
	mu     sync.Mutex
	latest map[limitKey]state.HarnessLimit
	// toldHeld is the hold each harness was last reported to the user for,
	// by when it ends, so a hold is announced once, not on every pass.
	toldHeld map[string]time.Time
	// heldTickets is the hold each held ticket's event was recorded for.
	heldTickets map[ident.TicketID]time.Time
}

func newLimits() *limits {
	return &limits{latest: map[limitKey]state.HarnessLimit{}, toldHeld: map[string]time.Time{}, heldTickets: map[ident.TicketID]time.Time{}}
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
		m.limits.latest[limitKey{l.Harness, l.Window}] = l
	}
	return nil
}

// reportLimit records a harness's report of a window, and has the scheduler
// look again: a lower figure may release a hold.
func (m *Manager) reportLimit(ctx context.Context, r *runner, l *harness.Limit) {
	hl := state.HarnessLimit{Harness: r.harness, Window: l.Window, UsedPercent: l.UsedPercent, ResetsAt: l.ResetsAt, ReportedAt: m.opts.Clock.Now()}
	m.limits.mu.Lock()
	m.limits.latest[limitKey{hl.Harness, hl.Window}] = hl
	m.limits.mu.Unlock()
	if err := m.opts.State.Settings.SetHarnessLimit(ctx, &hl); err != nil {
		m.opts.Logger.Warn("agents: recording a harness limit", "harness", r.harness, "err", err)
	}
	m.Kick()
}

func (m *Manager) threshold(name string) float64 {
	if hc := m.opts.Config.Harnesses[name]; hc != nil {
		return hc.LimitThreshold
	}
	return 100
}

// holdsUntil is when l stops holding its harness; zero when it doesn't.
func (m *Manager) holdsUntil(l *state.HarnessLimit) time.Time {
	if l.UsedPercent < m.threshold(l.Harness) {
		return time.Time{}
	}
	until := l.ResetsAt
	if until.IsZero() {
		until = l.ReportedAt.Add(NoResetHold)
	}
	if !m.opts.Clock.Now().Before(until) {
		return time.Time{}
	}
	return until
}

// heldUntil reports whether name is at its limit in any window, and when
// the last of those resets: a 5-hour window resetting doesn't free a plan
// whose weekly window is still full.
func (m *Manager) heldUntil(name string) (time.Time, bool) {
	m.limits.mu.Lock()
	defer m.limits.mu.Unlock()
	var until time.Time
	for k, l := range m.limits.latest {
		if k.harness != name {
			continue
		}
		if u := m.holdsUntil(&l); u.After(until) {
			until = u
		}
	}
	return until, !until.IsZero()
}

// Limits is every harness's latest report of each window, by harness and
// window.
func (m *Manager) Limits() []HarnessLimit {
	m.limits.mu.Lock()
	defer m.limits.mu.Unlock()
	out := make([]HarnessLimit, 0, len(m.limits.latest))
	for _, l := range m.limits.latest {
		out = append(out, HarnessLimit{Harness: l.Harness, Window: l.Window, UsedPercent: l.UsedPercent, ResetsAt: l.ResetsAt,
			ReportedAt: l.ReportedAt, Threshold: m.threshold(l.Harness), Held: !m.holdsUntil(&l).IsZero()})
	}
	slices.SortFunc(out, func(a, b HarnessLimit) int {
		if a.Harness != b.Harness {
			return cmp.Compare(a.Harness, b.Harness)
		}
		return cmp.Compare(a.Window, b.Window)
	})
	return out
}

// errHeld is a ticket that can't start yet: its harness is at its limit
// until until.
type errHeld struct {
	harness string
	until   time.Time
}

func (e *errHeld) Error() string {
	return fmt.Sprintf("%s is at its plan limit until %s", e.harness, e.until.Format(time.RFC3339))
}

// specFor is the agent spec t starts with. When its harness is at its
// limit, a ticket that left the harness, model and effort blank and has
// never been started goes to another configured harness that isn't; any
// other ticket waits. A ticket that names a harness chose it, and a started
// ticket's conversation lives in its harness, so neither is moved. A model
// or effort is one harness's, and not every harness's efforts are checked
// before a spawn.
func (m *Manager) specFor(ctx context.Context, t *tickets.Ticket) (tickets.AgentSpec, error) {
	spec := agentspec.Resolve(t.Agent, m.opts.Config)
	until, held := m.heldUntil(spec.Harness)
	if !held {
		return spec, nil
	}
	hold := &errHeld{harness: spec.Harness, until: until}
	if t.Agent.Harness != "" || t.Agent.Model != "" || t.Agent.Effort != "" {
		return spec, hold
	}
	_, err := m.opts.State.Agents.LatestForTicket(ctx, t.ID)
	if !errors.Is(err, state.ErrNotFound) {
		if err != nil {
			m.opts.Logger.Warn("agents: checking whether a held ticket was started", "ticket", t.ID, "err", err)
		}
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
	ticketNoted = ticketNoted && prev.Equal(hold.until)
	m.limits.heldTickets[t.ID] = hold.until
	prev, userTold := m.limits.toldHeld[hold.harness]
	userTold = userTold && prev.Equal(hold.until)
	m.limits.toldHeld[hold.harness] = hold.until
	m.limits.mu.Unlock()
	if !ticketNoted {
		m.event(ctx, EventHeld, "", t.ID, map[string]any{"harness": hold.harness, "until": hold.until})
	}
	if !userTold {
		m.tellUser(ctx, "", fmt.Sprintf("%s: new agents on it wait (`slop-bot status` shows the allowance)", hold.Error()))
	}
}

// unheld forgets t's hold once it starts, so a later hold is noted again
// and the map doesn't grow with every ticket ever held.
func (m *Manager) unheld(t ident.TicketID) {
	m.limits.mu.Lock()
	delete(m.limits.heldTickets, t)
	m.limits.mu.Unlock()
}

// wakeAt has the scheduler look again at t, when a held harness's hold
// ends. An earlier wake already set stands: that pass sets the next. Only
// the scheduler goroutine calls it.
func (m *Manager) wakeAt(t time.Time) {
	if t.IsZero() || (m.wake != nil && !t.Before(m.wakeTime)) {
		return
	}
	m.wakeTime = t
	m.wake = m.opts.Clock.After(t.Sub(m.opts.Clock.Now()))
}
