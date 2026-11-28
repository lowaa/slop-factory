package agents

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

// landingTickets answers a ticket read, first running during: what else
// happens to the agent while the runner waits on the read.
type landingTickets struct {
	tickets.TicketingSystem
	during func()
}

func (l *landingTickets) Get(_ context.Context, id ident.TicketID) (*tickets.Ticket, error) {
	l.during()
	return &tickets.Ticket{ID: id, Status: tickets.StatusInProgress}, nil
}

// A turn's start works out the agent's new state from its ticket; an attach
// that lands during that read wins. Otherwise the agent would be marked
// working while a person holds it, and its hand-back find it not attached.
// Nothing is recorded, so the bare manager has no store to write to.
func TestAnAttachLandingDuringATurnsStartIsKept(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		from  state.AgentState
		event func(r *runner, ctx context.Context)
	}{
		{"a turn starting", state.StateIdle, (*runner).turnStarted},
		{"a stalled agent active again", state.StateStalled, (*runner).activeAgain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := bareManager(1)
			r := &runner{m: m, id: "a1", ticket: "1", st: tc.from}
			m.opts.Tickets = &landingTickets{during: func() {
				r.mu.Lock()
				r.st = state.StateAttached
				r.mu.Unlock()
			}}

			tc.event(r, context.Background())

			assert.Equal(t, state.StateAttached, r.state())
		})
	}
}
