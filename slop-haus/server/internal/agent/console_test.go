package agent_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// consoleFixture is the console flag against the real stores: what the
// flag does to an agent's pending proposals is a database matter.
type consoleFixture struct {
	database *db.DB
	tenant   uuid.UUID
	ctx      context.Context
	agents   *agent.Service
	pending  *pendingaction.Service
	tickets  *ticket.Service
	user     *authn.User
}

func newConsoleFixture(t *testing.T) *consoleFixture {
	t.Helper()
	database := dbtest.WithDB(t)
	tenant := dbtest.NewTenant(t, database)
	f := &consoleFixture{database: database, tenant: tenant, ctx: db.WithTenant(context.Background(), tenant)}
	f.pending = pendingaction.NewService(pendingaction.NewPostgresStore(database), pendingaction.Config{}, slog.DiscardHandler)
	f.tickets = ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler)
	f.agents = agent.NewService(agent.NewPostgresStore(database), f.tickets, nil, slog.DiscardHandler,
		agent.WithProposalVoider(f.pending))
	f.user = &authn.User{ID: dbtest.NewUser(t, database), TenantID: tenant, AuthTime: time.Now()}
	return f
}

// console is an agent registered as a console, as the consent screen does.
func (f *consoleFixture) console(t *testing.T, name string) *agent.Agent {
	t.Helper()
	created, err := f.agents.CreateKind(f.ctx, name, agent.KindConsole)
	require.NoError(t, err)
	return created
}

func (f *consoleFixture) propose(t *testing.T, a *agent.Agent) *pendingaction.Action {
	t.Helper()
	tk, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "t", Status: ticket.StatusOpen})
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `UPDATE tickets SET status = 'preparing' WHERE id = $1`, tk.ID)
	require.NoError(t, err)
	got, err := f.pending.Propose(f.ctx, &pendingaction.ProposeParams{AgentID: a.ID, TicketID: tk.ID, Kind: pendingaction.KindReady})
	require.NoError(t, err)
	return got
}

func (f *consoleFixture) state(t *testing.T, id uuid.UUID) pendingaction.State {
	t.Helper()
	got, err := f.pending.Get(f.ctx, id)
	require.NoError(t, err)
	return got.State
}

func TestDisablingAConsoleVoidsOnlyThatAgentsPendingProposals(t *testing.T) {
	f := newConsoleFixture(t)
	leaving, staying := f.console(t, "leaving"), f.console(t, "staying")
	gone, kept := f.propose(t, leaving), f.propose(t, staying)

	_, err := f.agents.SetDisabled(f.ctx, leaving.ID, true)
	require.NoError(t, err)

	assert.Equal(t, pendingaction.StateVoided, f.state(t, gone.ID))
	assert.Equal(t, pendingaction.StatePending, f.state(t, kept.ID))
	_, err = f.pending.Propose(f.ctx, &pendingaction.ProposeParams{AgentID: leaving.ID, TicketID: *gone.TicketID, Kind: pendingaction.KindReady})
	assert.ErrorIs(t, err, pendingaction.ErrNotConsole, "disabled is read at call time")
}

func TestDisablingAConsoleVoidsItsPendingProposals(t *testing.T) {
	f := newConsoleFixture(t)
	c := f.console(t, "console")
	p := f.propose(t, c)

	_, err := f.agents.SetDisabled(f.ctx, c.ID, true)
	require.NoError(t, err)
	assert.Equal(t, pendingaction.StateVoided, f.state(t, p.ID))
}

func TestGrantsEndingVoidsPendingProposals(t *testing.T) {
	f := newConsoleFixture(t)
	c := f.console(t, "console")
	p := f.propose(t, c)

	require.NoError(t, f.agents.GrantsEnded(context.Background(), f.tenant, c.ID),
		"the caller's context need not be scoped to the tenant")
	assert.Equal(t, pendingaction.StateVoided, f.state(t, p.ID))
}

type failingVoider struct{ err error }

func (v failingVoider) VoidAgent(context.Context, uuid.UUID, string) (int64, error) { return 0, v.err }

// A grant ending is how the last way to act as the agent goes, so a failed
// void is reported rather than swallowed.
func TestGrantsEndingReportsAFailedVoid(t *testing.T) {
	f := newConsoleFixture(t)
	c := f.console(t, "console")
	boom := errors.New("void failed")
	agents := agent.NewService(agent.NewPostgresStore(f.database), f.tickets, nil, slog.DiscardHandler,
		agent.WithProposalVoider(failingVoider{err: boom}))

	err := agents.GrantsEnded(context.Background(), f.tenant, c.ID)
	require.ErrorIs(t, err, boom)
}

func TestMayProposeReadsTheKindAndTheDisabledSwitch(t *testing.T) {
	f := newConsoleFixture(t)
	plain, err := f.agents.Create(f.ctx, "plain")
	require.NoError(t, err)
	c := f.console(t, "console")

	is, err := f.agents.MayPropose(f.ctx, plain.ID)
	require.NoError(t, err)
	assert.False(t, is)
	is, err = f.agents.MayPropose(f.ctx, c.ID)
	require.NoError(t, err)
	assert.True(t, is)
	is, err = f.agents.MayPropose(f.ctx, uuid.New())
	require.NoError(t, err)
	assert.False(t, is, "an unknown agent is not a console")

	_, err = f.agents.SetDisabled(f.ctx, c.ID, true)
	require.NoError(t, err)
	is, err = f.agents.MayPropose(f.ctx, c.ID)
	require.NoError(t, err)
	assert.False(t, is, "a disabled console is refused too")
}
