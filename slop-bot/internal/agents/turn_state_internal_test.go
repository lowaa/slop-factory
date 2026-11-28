package agents

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
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

// storedRunner is a runner of a bare manager over a real state.db holding
// its agent, for the moves that read the store before they are made.
func storedRunner(t *testing.T) *runner {
	t.Helper()
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC))
	st, err := state.Open(ctx, &state.Options{Path: filepath.Join(t.TempDir(), "state.db"), Logger: slog.New(slog.DiscardHandler), Clock: clk})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	_, err = st.Agents.Create(ctx, &state.NewAgent{
		ID: "a1", Ticket: "1", Harness: config.ClaudeCode, Driver: "test", Pool: state.PoolNormal,
		State: state.StateWorking, Reason: "test", TokenHash: control.HashToken("token"),
	})
	require.NoError(t, err)
	m := bareManager(1)
	m.opts.State, m.opts.Clock, m.opts.Logger = st, clk, slog.New(slog.DiscardHandler)
	m.opts.Config.Agents.Nudges = 3
	m.kick = make(chan struct{}, 1)
	return &runner{m: m, id: "a1", ticket: "1", st: state.StateWorking}
}

// Likewise at a turn's end: what the agent settles into is read from its
// asks and its ticket, and a person joining meanwhile keeps it attached,
// unprodded.
func TestAnAttachLandingWhileATurnSettlesIsKept(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		decl declared
	}{
		{"ready to merge", declMerge},
		{"stopped without saying why (a nudge)", declNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := storedRunner(t)
			r.m.opts.Tickets = &landingTickets{during: func() {
				r.mu.Lock()
				r.st = state.StateAttached
				r.mu.Unlock()
			}}

			r.settle(context.Background(), tc.decl, &harness.Event{Kind: harness.TurnEnded, Outcome: harness.TurnCompleted})

			assert.Equal(t, state.StateAttached, r.state())
			queued, err := r.m.opts.State.Messages.Pending(context.Background(), state.AgentParty("a1"))
			require.NoError(t, err)
			assert.Empty(t, queued, "no nudge for a person's session")
		})
	}
}
