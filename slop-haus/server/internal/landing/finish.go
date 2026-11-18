package landing

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Lander is the ticket side of a finish: the held ticket's hand-over to the
// queue, with the queue row written in the same transaction.
type Lander interface {
	Land(ctx context.Context, agentID uuid.UUID, params *ticket.LandParams) (*ticket.Ticket, error)
	ChangeGateFor(ctx context.Context, agentID uuid.UUID) (*ticket.ChangeGate, error)
}

// Kicker wakes the runner for a tenant; *Runner is one.
type Kicker interface{ Kick(tenant uuid.UUID) }

// Finisher is what ticket_finish does for a tenant on the merge queue: it puts
// the finished ticket in the landing queue and ends the agent's hold, so the
// server lands it (merge main, CI, merge) without an agent holding the lock.
type Finisher struct {
	tickets Lander
	host    Host
	mode    Mode
	kicker  atomic.Pointer[Kicker]
	guard   *EnqueueGuard
}

func NewFinisher(tickets Lander, host Host, mode Mode) *Finisher {
	return &Finisher{tickets: tickets, host: host, mode: mode}
}

// SetKicker wires the runner once it exists. Until then a landing waits for
// the runner's next sweep.
func (f *Finisher) SetKicker(k Kicker) { f.kicker.Store(&k) }

// SetGuard makes Land refuse to re-queue a head that already went red. Nil
// (the default) refuses nothing.
func (f *Finisher) SetGuard(g *EnqueueGuard) { f.guard = g }

// Land enqueues the held ticket's change. It reports false, having changed
// nothing, when the ticket is not landed by the server: the tenant is on the
// worker flow, or the attempt has no change slop-haus opened (the agent then
// lands it as before).
func (f *Finisher) Land(ctx context.Context, tenant, agentID uuid.UUID, handoff string, flakeTicket int64) (*ticket.Ticket, bool, error) {
	on, err := f.mode.Enabled(ctx)
	if err != nil || !on {
		return nil, false, err
	}
	gate, err := f.tickets.ChangeGateFor(ctx, agentID)
	if err != nil {
		return nil, false, err
	}
	state, err := f.host.State(ctx, tenant, gate.Attempt.TicketID, gate.Attempt.Number)
	if errors.Is(err, change.ErrNoChange) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if f.guard != nil {
		if err := f.guard.Check(ctx, tenant, agentID, gate.Attempt.TicketID, state.HeadSHA, flakeTicket); err != nil {
			return nil, false, err
		}
	}
	row := &LandingRow{TicketID: gate.Attempt.TicketID, AttemptNumber: gate.Attempt.Number, ChangeURL: state.URL, HeadSHA: state.HeadSHA}
	left, err := f.tickets.Land(ctx, agentID, &ticket.LandParams{
		Handoff: handoff,
		Within:  func(q db.Querier) error { return EnqueueIn(ctx, q, row) },
	})
	if err != nil {
		return nil, false, err
	}
	if k := f.kicker.Load(); k != nil {
		(*k).Kick(tenant)
	}
	return left, true, nil
}
