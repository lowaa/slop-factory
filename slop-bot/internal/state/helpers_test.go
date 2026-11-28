package state_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	clock "github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/sqlitex"
	"github.com/lowaa/slop-factory/slop-bot/internal/sqlitex/sqlitextest"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

var t0 = time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)

type fixture struct {
	*state.State
	db    *sqlitex.DB
	clock *clock.Fake
}

// newFixture is a fresh state.db of the test's own, on a fake clock at t0.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := sqlitextest.Open(t, state.Migrations())
	clk := clock.NewFake(t0)
	s := state.New(db, clk)
	t.Cleanup(func() { _ = s.Close() })
	return &fixture{State: s, db: db, clock: clk}
}

// newAgent is a minimal valid NewAgent; the token is derived from the id.
func newAgent(id ident.AgentID, ticket ident.TicketID) *state.NewAgent {
	return &state.NewAgent{
		ID: id, Ticket: ticket, Harness: "claude-code", Driver: "rpc",
		Pool: state.PoolNormal, State: state.StateStarting, TokenHash: control.HashToken("token-" + string(id)),
	}
}

func (f *fixture) createAgent(t *testing.T, id ident.AgentID, ticket ident.TicketID) *state.Agent {
	t.Helper()
	a, err := f.Agents.Create(context.Background(), newAgent(id, ticket))
	require.NoError(t, err)
	return a
}

func (f *fixture) setState(t *testing.T, id ident.AgentID, to state.AgentState) {
	t.Helper()
	changed, err := f.Agents.SetState(context.Background(), &state.Transition{Agent: id, To: to})
	require.NoError(t, err)
	require.True(t, changed)
}

func (f *fixture) count(t *testing.T, table string) int64 {
	t.Helper()
	n, err := f.db.NewSelect().Table(table).Count(context.Background())
	require.NoError(t, err)
	return n
}

func ptr[T any](v T) *T { return &v }

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func newFakeClock() *clock.Fake { return clock.NewFake(t0) }
