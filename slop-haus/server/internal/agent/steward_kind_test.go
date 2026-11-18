package agent_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// steward is minted the way production does it, by a supervisor.
func (f *supervisorFixture) steward(t *testing.T) uuid.UUID {
	t.Helper()
	steward, err := f.agents.Steward(f.ctx, f.supervisor(t, "steward-sup", 1).ID)
	require.NoError(t, err)
	return steward.ID
}

func TestStewardKind_ProposesButIsNeverHandedWork(t *testing.T) {
	f := newSupervisorFixture(t)
	id := f.steward(t)

	isSteward, err := f.agents.IsSteward(f.ctx, id)
	require.NoError(t, err)
	assert.True(t, isSteward)
	may, err := f.agents.MayPropose(f.ctx, id)
	require.NoError(t, err)
	assert.True(t, may)

	_, err = f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "work", Status: ticket.StatusOpen})
	require.NoError(t, err)
	claim, err := f.tickets.Claim(f.ctx, id, nil)
	require.NoError(t, err)
	assert.Nil(t, claim.Ticket, "a steward is skipped by the queue")

	_, err = f.agents.SetDisabled(f.ctx, id, true)
	require.NoError(t, err, "a person switches it off from the Agents screen")
	isSteward, err = f.agents.IsSteward(f.ctx, id)
	require.NoError(t, err)
	assert.False(t, isSteward, "disabled")
}

func TestStewardKind_OnlyAStewardIsOne(t *testing.T) {
	f := newConsoleFixture(t)
	console := f.consoleOfKind(t, "console")
	isSteward, err := f.agents.IsSteward(f.ctx, console.ID)
	require.NoError(t, err)
	assert.False(t, isSteward)
	isSteward, err = f.agents.IsSteward(f.ctx, uuid.New())
	require.NoError(t, err)
	assert.False(t, isSteward)

	_, err = f.agents.CreateKind(f.ctx, "s", agent.KindSteward)
	assert.ErrorIs(t, err, agent.ErrBadKind, "the public creation path stays closed")
}
