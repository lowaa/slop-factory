package agents_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

// The failure notice is for a `done` that didn't finish the ticket: one
// that did leaves the agent nothing to carry on with.
func TestASuccessfulDoneSendsNoFailureNotice(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	tk := f.file("t", func(nt *tickets.NewTicket) { nt.Status = tickets.StatusOpen })
	s := f.session()
	f.user(s)
	a := f.agentFor(tk.ID)
	f.turn(s, func() {
		require.NoError(t, f.mgr.Done(f.ctx(), ident.AgentPrincipal(a.ID, tk.ID), "Exported as CSV."))
	})
	f.await(agents.EventFinished, nil)
	assert.Zero(t, f.systemMessages(a.ID, "didn't go through"))
}
