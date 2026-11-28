package agents_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
)

// A message the session took but never got to the model (a codec holding
// it for a turn) was marked delivered when Send took it. When the session
// dies with it, the resumed one gets it.
func TestAMessageTheSessionHeldReachesTheResumedOne(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	tk := f.file("t", nil)
	s := f.session()
	f.user(s)
	f.do(s, fake.Command{Cmd: fake.CmdTurnStart})
	agent := f.agentFor(tk.ID).ID

	f.hooks.set(func(h *harnessHooks) { h.hold = true })
	_, err := f.mgr.Tell(f.ctx(), ident.UserPrincipal(), string(agent), "keep this", false)
	require.NoError(t, err)
	f.await(agents.EventDelivered, nil)
	f.hooks.set(func(h *harnessHooks) { h.hold = false })

	f.do(s, fake.Command{Cmd: fake.CmdExit, Code: 3})
	resumed := f.session()
	require.True(t, resumed.Resumed)
	assert.Contains(t, f.user(resumed).Text, "keep this")
}
