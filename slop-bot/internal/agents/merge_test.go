package agents_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

func (f *fixture) mergeGranted(ticket ident.TicketID) bool {
	f.t.Helper()
	ok, err := f.state.MergeGrants.Granted(context.Background(), ticket)
	require.NoError(f.t, err)
	return ok
}

func TestAMergeAuthorisationIsTheUsersAndEndsWithTheTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, tk, agent := f.startedAgent("t", nil)

	assert.ErrorIs(t, f.mgr.AuthoriseMerge(f.ctx(), agent, string(agent.Agent)), agents.ErrUserOnly)
	assert.False(t, f.mergeGranted(tk.ID))
	assert.ErrorIs(t, f.mgr.AuthoriseMerge(f.ctx(), ident.UserPrincipal(), "nobody"), agents.ErrUnknownTarget)

	require.NoError(t, f.mgr.AuthoriseMerge(f.ctx(), ident.UserPrincipal(), string(tk.ID)), "by the ticket's id")
	f.await(agents.EventMergeAuthorised, nil)
	assert.True(t, f.mergeGranted(tk.ID))

	f.turn(s, func() { require.NoError(t, f.mgr.Done(f.ctx(), agent, "merged")) })
	f.await(agents.EventFinished, nil)
	assert.False(t, f.mergeGranted(tk.ID), "a reopened ticket needs a new go-ahead")
}

func TestACancelledTicketLosesItsMergeAuthorisation(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, tk, agent := f.startedAgent("t", nil)
	require.NoError(t, f.mgr.AuthoriseMerge(f.ctx(), ident.UserPrincipal(), string(agent.Agent)), "by the agent's id")
	f.do(s, fake.Command{Cmd: fake.CmdTurnStart})
	st := tickets.StatusCancelled
	_, err := f.tickets.Update(context.Background(), ident.UserPrincipal(), tk.ID, &tickets.TicketChange{Status: &st})
	require.NoError(t, err)
	f.await(agents.EventFinished, nil)
	assert.False(t, f.mergeGranted(tk.ID))
}

func TestATicketMarkedDoneByHandLosesItsMergeAuthorisation(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, tk, _ := f.startedAgent("t", nil)
	require.NoError(t, f.mgr.AuthoriseMerge(f.ctx(), ident.UserPrincipal(), string(tk.ID)))
	f.do(s, fake.Command{Cmd: fake.CmdTurnStart})
	for _, st := range []tickets.Status{tickets.StatusOpen, tickets.StatusInProgress, tickets.StatusDone} {
		cur := f.ticket(tk.ID).Status
		if cur == st || !tickets.CanTransition(cur, st) {
			continue
		}
		_, err := f.tickets.Update(context.Background(), ident.UserPrincipal(), tk.ID, &tickets.TicketChange{Status: &st})
		require.NoError(t, err)
	}
	require.Equal(t, tickets.StatusDone, f.ticket(tk.ID).Status)
	f.await(agents.EventFinished, nil)
	assert.False(t, f.mergeGranted(tk.ID))

	err := f.mgr.AuthoriseMerge(f.ctx(), ident.UserPrincipal(), string(tk.ID))
	assert.Error(t, err, "nothing to authorise: the ticket's agent has ended")
}

func TestAnEndedTicketCantBeAuthorised(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	_, tk, _ := f.startedAgent("t", nil)
	cancelled := tickets.StatusCancelled
	_, err := f.tickets.Update(context.Background(), ident.UserPrincipal(), tk.ID, &tickets.TicketChange{Status: &cancelled})
	require.NoError(t, err)
	// Whether or not its agent has been ended yet (then there's no target),
	// an ended ticket gets no go-ahead.
	assert.Error(t, f.mgr.AuthoriseMerge(f.ctx(), ident.UserPrincipal(), string(tk.ID)))
	assert.False(t, f.mergeGranted(tk.ID))
}

func TestSaysMerge(t *testing.T) {
	t.Parallel()
	for text, want := range map[string]bool{
		"merge": true, "Merge.": true, "  MERGE!  ": true,
		"don't merge": false, "merge it after CI": false, "": false, "emerge": false,
	} {
		assert.Equal(t, want, agents.SaysMerge(text), "%q", text)
	}
}
