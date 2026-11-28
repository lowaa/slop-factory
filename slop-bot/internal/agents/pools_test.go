package agents_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

func TestPoolUsageCountsAgentsInATurnOnly(t *testing.T) {
	t.Parallel()
	f := newFixture(t, oneSlot)
	tk := f.file("first", nil)
	s := f.session()
	f.user(s)
	a := f.agentFor(tk.ID)

	f.do(s, fake.Command{Cmd: fake.CmdTurnStart})
	assert.Equal(t, 1, f.mgr.PoolUsage()[0].Active, "in a turn")

	_, err := f.mgr.Ask(f.ctx(), ident.AgentPrincipal(a.ID, tk.ID), "?", nil, "")
	require.NoError(t, err)
	f.do(s, fake.Command{Cmd: fake.CmdTurnEnd})
	f.await(agents.EventState, toState(state.StateWaitingUser))
	usage := f.mgr.PoolUsage()
	require.Len(t, usage, 2)
	assert.Equal(t, agents.PoolUsage{Pool: state.PoolNormal, Active: 0, Max: 1}, usage[0], "a waiting agent holds no slot")
	assert.Equal(t, state.PoolUrgent, usage[1].Pool)
}
