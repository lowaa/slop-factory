package agent_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
)

// consoleOfKind is registered as a console, not flagged after the fact.
func (f *consoleFixture) consoleOfKind(t *testing.T, name string) *agent.Agent {
	t.Helper()
	created, err := f.agents.CreateKind(f.ctx, name, agent.KindConsole)
	require.NoError(t, err)
	return created
}

func TestConsoleKindIsAConsole(t *testing.T) {
	f := newConsoleFixture(t)
	c := f.consoleOfKind(t, "console")

	assert.Equal(t, agent.KindConsole, c.Kind)
	assert.True(t, c.IsConsole())

	may, err := f.agents.MayPropose(f.ctx, c.ID)
	require.NoError(t, err)
	assert.True(t, may)
	_, err = f.agents.SetDisabled(f.ctx, c.ID, true)
	require.NoError(t, err)
	may, err = f.agents.MayPropose(f.ctx, c.ID)
	require.NoError(t, err)
	assert.False(t, may, "disabled")
}

func TestConsoleKindProposesAndIsVoidedOnDisable(t *testing.T) {
	f := newConsoleFixture(t)
	c := f.consoleOfKind(t, "console")
	p := f.propose(t, c)

	_, err := f.agents.SetDisabled(f.ctx, c.ID, true)
	require.NoError(t, err)
	assert.Equal(t, pendingaction.StateVoided, f.state(t, p.ID))
}

func TestIsConsoleReadsTheKind(t *testing.T) {
	assert.True(t, (&agent.Agent{Kind: agent.KindConsole}).IsConsole())
	assert.False(t, (&agent.Agent{Kind: agent.KindWorker}).IsConsole())
	assert.False(t, (&agent.Agent{Kind: agent.KindConductor}).IsConsole())
}

func TestCreateKindRefusesAKindThatNeedsMore(t *testing.T) {
	f := newConsoleFixture(t)
	_, err := f.agents.CreateKind(f.ctx, "c", agent.KindConductor)
	assert.ErrorIs(t, err, agent.ErrBadKind, "a conductor needs an owner")
	_, err = f.agents.CreateKind(f.ctx, "c", agent.Kind("other"))
	assert.ErrorIs(t, err, agent.ErrBadKind)
}

func TestConsoleKindIsGuardedByTheDatabase(t *testing.T) {
	f := newConsoleFixture(t)
	c := f.consoleOfKind(t, "console")
	worker, err := f.agents.Create(f.ctx, "worker")
	require.NoError(t, err)

	for name, sql := range map[string]struct {
		query string
		args  []any
	}{
		"kind changes after registration":    {`UPDATE agents SET kind = 'console' WHERE id = $1`, []any{worker.ID}},
		"a console kind turns into a worker": {`UPDATE agents SET kind = 'worker' WHERE id = $1`, []any{c.ID}},
		"a kind the table does not know":     {`INSERT INTO agents (id, name, kind) VALUES ($1, 'raw', 'bogus')`, []any{uuid.New()}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.database.Exec(f.ctx, sql.query, sql.args...)
			assert.Error(t, err)
		})
	}
}

func TestConsolesAreListedWithTheirKind(t *testing.T) {
	f := newConsoleFixture(t)
	c := f.consoleOfKind(t, "console")
	_, err := f.agents.Create(f.ctx, "worker")
	require.NoError(t, err)

	listed, err := f.agents.List(f.ctx, false)
	require.NoError(t, err)
	kinds := map[uuid.UUID]agent.Kind{}
	for _, a := range listed {
		kinds[a.ID] = a.Kind
	}
	assert.Equal(t, agent.KindConsole, kinds[c.ID])
	assert.Len(t, kinds, 2)
}
