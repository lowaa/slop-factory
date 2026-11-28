package agents_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

// These tests are for the manager's reaction to a store that can't write
// (state.db made to refuse the one write by a trigger) or a ticketing system
// that can't answer: it warns and carries on, or gives the failure back,
// never leaves a half-done change it doesn't say.

// noAgentTicket files a ticket nobody is assigned, so the scheduler leaves it
// alone and the test decides what runs for it.
func (f *fixture) noAgentTicket() *tickets.Ticket {
	f.t.Helper()
	return f.file("t", func(nt *tickets.NewTicket) { nt.Assignee = "" })
}

// deadAgent records a live agent for tk that no process runs, as one whose
// harness died and nothing has resumed yet.
func (f *fixture) deadAgent(tk *tickets.Ticket) *state.Agent {
	f.t.Helper()
	a, err := f.state.Agents.Create(context.Background(), &state.NewAgent{
		ID: ident.AgentID("dead-" + string(tk.ID)), Ticket: tk.ID, Harness: config.ClaudeCode, Driver: fake.Driver,
		Pool: state.PoolNormal, State: state.StateIdle, Reason: "test", TokenHash: control.HashToken("token"),
	})
	require.NoError(f.t, err)
	return a
}

func TestAFailedMergeRevokeIsWarnedOf(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	tk := f.noAgentTicket()
	require.NoError(t, f.state.MergeGrants.Grant(f.ctx(), tk.ID, state.UserParty))
	f.failOn("BEFORE DELETE ON merge_grants")

	f.mgr.RevokeMerge(f.ctx(), tk.ID)

	assert.Contains(t, f.logs.String(), "clearing a merge authorisation")
	assert.Contains(t, f.logs.String(), string(tk.ID))
}

func TestANoticeThatCantBeQueuedIsNotDelivered(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, _, agent := f.startedAgent("t", nil)
	f.failOn("BEFORE INSERT ON messages WHEN NEW.kind = 'system'")

	assert.False(t, f.mgr.EnqueueNotice(f.ctx(), agent.Agent, "a notice"))

	assert.Contains(t, f.logs.String(), "queueing a notice")
	pending, err := f.state.Messages.Pending(f.ctx(), state.AgentParty(agent.Agent))
	require.NoError(t, err)
	assert.Empty(t, pending)
	_ = s
}

// A harness that can't take a message mid-turn gets it at the turn's end,
// not at once.
func TestAMidTurnMessageWaitsForTheTurnsEndWhenTheDriverCantSteer(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	f.hooks.set(func(h *harnessHooks) { h.noSteer = true })
	s, tk, agent := f.startedAgent("t", nil)
	f.do(s, fake.Command{Cmd: fake.CmdTurnStart})
	f.await(agents.EventState, toState(state.StateRefining))

	_, err := f.mgr.Tell(f.ctx(), ident.UserPrincipal(), string(tk.ID), "main is red", false)
	require.NoError(t, err)

	pending, err := f.state.Messages.Pending(f.ctx(), state.AgentParty(agent.Agent))
	require.NoError(t, err)
	require.Len(t, pending, 1, "held, not sent mid-turn")
	f.do(s, fake.Command{Cmd: fake.CmdTurnEnd})
	got := f.user(s)
	assert.False(t, got.MidTurn)
	assert.Contains(t, got.Text, "main is red")
}

func TestAStateThatCantBeRecordedIsWarnedOfAndStillAnnounced(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, tk, _ := f.startedAgent("t", nil)
	f.failOn("BEFORE UPDATE OF state ON agents")

	f.do(s, fake.Command{Cmd: fake.CmdTurnStart})
	f.await(agents.EventState, toState(state.StateRefining))

	assert.Contains(t, f.logs.String(), "recording a state")
	assert.Equal(t, state.StateStarting, f.agentFor(tk.ID).State, "the record kept the last state it could write")
}

func TestABlockerNoticeThatCantBeRecordedIsWarnedOf(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	blocker := f.unassigned("blocker")
	s, tk, agent := f.openAndStarted("t")
	f.failOn("BEFORE UPDATE OF blocked_told ON agents")
	_, err := f.tickets.AddBlocker(context.Background(), ident.UserPrincipal(), tk.ID, blocker.ID)
	require.NoError(t, err)
	assert.Contains(t, f.user(s).Text, "now blocked", "the notice goes first")

	// The scheduler's own pass may have got there first; either way the
	// record can't be written.
	require.NoError(t, f.mgr.Sync(f.ctx(), tk.ID), "a write after the reads is warned of, not retried")

	assert.Contains(t, f.logs.String(), "recording a blocker notice")
	assert.False(t, f.agentFor(tk.ID).BlockedTold)
	_ = agent
}

func TestAConversationIdThatCantBeRecordedDoesntStopTheAgent(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	f.failOn("BEFORE UPDATE OF conversation_id ON agents")
	f.file("t", nil)

	f.await(agents.EventSpawned, nil)

	assert.Contains(t, f.logs.String(), "recording the conversation id")
	assert.Contains(t, f.user(f.session()).Text, "assigned ticket", "the agent is running and was given its ticket")
}

func TestAnAgentThatCantBeRecordedIsNotStarted(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	tk := f.noAgentTicket()
	f.failOn("BEFORE INSERT ON agents")

	err := f.mgr.SpawnAgent(f.ctx(), tk, state.PoolNormal, tickets.AgentSpec{Harness: config.ClaudeCode})

	require.ErrorContains(t, err, "injected failure")
	_, err = f.state.Agents.LiveForTicket(f.ctx(), tk.ID)
	assert.ErrorIs(t, err, state.ErrNotFound)
}

func TestAnAgentThatCantBeEndedInItsRecordIsWarnedOf(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	tk := f.noAgentTicket()
	a := f.deadAgent(tk)
	f.failOn("BEFORE UPDATE OF ended_at ON agents")
	f.setStatus(tk.ID, tickets.StatusCancelled)

	// Its ticket ended while nothing ran it: the record is ended, here.
	require.NoError(t, f.mgr.Sync(f.ctx(), tk.ID), "a write after the reads is warned of, not retried")

	assert.Contains(t, f.logs.String(), "ending an agent")
	got, err := f.state.Agents.Get(f.ctx(), a.ID)
	require.NoError(t, err)
	assert.Nil(t, got.EndedAt)
}

func TestASyncThatCantReadItsTicketLeavesTheAgentAlone(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	tk := f.noAgentTicket()
	a := f.deadAgent(tk)
	f.racing.failGet(tk.ID, errors.New("tickets are gone"))
	f.setStatus(tk.ID, tickets.StatusCancelled)

	// The ticket ended, but the manager can't see that: it doesn't guess.
	require.Error(t, f.mgr.Sync(f.ctx(), tk.ID), "and says so, for the scheduler to try again")

	got, err := f.state.Agents.Get(f.ctx(), a.ID)
	require.NoError(t, err)
	assert.Nil(t, got.EndedAt)
}

func TestRecoveringAnAgentThatIsGoneOrEndedDoesNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	tk := f.noAgentTicket()
	f.mgr.Recover(f.ctx(), "no-such-agent")

	a := f.deadAgent(tk)
	_, err := f.state.Agents.SetState(f.ctx(), &state.Transition{Agent: a.ID, To: state.StateDone, Reason: "ended"})
	require.NoError(t, err)
	f.mgr.Recover(f.ctx(), a.ID)

	got, err := f.state.Agents.Get(f.ctx(), a.ID)
	require.NoError(t, err)
	assert.Zero(t, got.Recoveries, "no resume was counted")
	assert.Empty(t, f.logs.String(), "nothing was tried, so nothing failed")
}

func TestAnAgentThatCantBeResumedIsGivenUpOnUnlessTheDaemonIsStopping(t *testing.T) {
	t.Parallel()
	cantStart := func(context.Context, *harness.Workspace, *harness.ConversationRef) error {
		return errors.New("no such harness")
	}
	t.Run("running", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, "")
		f.hooks.set(func(h *harnessHooks) { h.start = cantStart })
		tk := f.noAgentTicket()
		a := f.deadAgent(tk)

		f.mgr.Recover(f.ctx(), a.ID)

		got, err := f.state.Agents.Get(f.ctx(), a.ID)
		require.NoError(t, err)
		assert.Equal(t, state.StateReleased, got.State)
		assert.NotNil(t, got.EndedAt)
	})
	t.Run("stopping", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, "")
		f.hooks.set(func(h *harnessHooks) { h.start = cantStart })
		tk := f.noAgentTicket()
		a := f.deadAgent(tk)
		f.mgr.CancelBase()

		f.mgr.Recover(f.ctx(), a.ID)

		got, err := f.state.Agents.Get(f.ctx(), a.ID)
		require.NoError(t, err)
		assert.Nil(t, got.EndedAt, "left for the next start to try again")
		assert.Equal(t, 1, got.Recoveries)
	})
}

func TestARecoveryThatCantBeCountedIsWarnedOfAndNotTried(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	tk := f.noAgentTicket()
	a := f.deadAgent(tk)
	f.failOn("BEFORE UPDATE OF recoveries ON agents")

	f.mgr.Recover(f.ctx(), a.ID)

	assert.Contains(t, f.logs.String(), "counting a recovery")
	assert.Contains(t, f.logs.String(), string(a.ID))
	got, err := f.state.Agents.Get(f.ctx(), a.ID)
	require.NoError(t, err)
	assert.Nil(t, got.EndedAt, "left as it was, not given up on")
	assert.Equal(t, state.StateIdle, got.State)
	evs, err := f.state.Events.ReadAfter(f.ctx(), 0, 1000)
	require.NoError(t, err)
	for i := range evs {
		assert.NotEqual(t, agents.EventResumed, string(evs[i].Type), "no resume was tried")
	}
}

// A harness that dies while a start for its ticket is in flight is recovered
// once that start settles, not before and not never.
func TestARecoveryThatWaitedForAStartRunsWhenItSettles(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	tk := f.noAgentTicket()
	a := f.deadAgent(tk)
	hold := make(chan struct{})

	require.True(t, f.mgr.RecoverWhenStartSettles(a.ID, tk.ID, hold))
	got, err := f.state.Agents.Get(f.ctx(), a.ID)
	require.NoError(t, err)
	require.Zero(t, got.Recoveries, "waiting for the start in flight")
	close(hold)
	f.mgr.WaitStarts()

	got, err = f.state.Agents.Get(f.ctx(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, got.Recoveries, "recovered once the start settled")
}

func TestAdvanceToGivesBackWhyATicketCantBeWalked(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	by := ident.UserPrincipal()

	t.Run("it can't be read", func(t *testing.T) {
		tk := f.noAgentTicket()
		f.racing.failGet(tk.ID, errors.New("tickets are gone"))
		err := f.mgr.AdvanceTo(f.ctx(), by, tk.ID, tickets.StatusInProgress)
		require.ErrorContains(t, err, "tickets are gone")
	})
	t.Run("it is off the path", func(t *testing.T) {
		tk := f.noAgentTicket()
		f.setStatus(tk.ID, tickets.StatusDiscussion)
		err := f.mgr.AdvanceTo(f.ctx(), by, tk.ID, tickets.StatusInProgress)
		require.ErrorIs(t, err, agents.ErrCantFinish)
		assert.Equal(t, tickets.StatusDiscussion, f.ticket(tk.ID).Status, "not moved")
	})
	t.Run("a step is refused", func(t *testing.T) {
		tk := f.noAgentTicket()
		f.racing.failUpdate(tk.ID, errors.New("the ticket is locked"))
		err := f.mgr.AdvanceTo(f.ctx(), by, tk.ID, tickets.StatusInProgress)
		require.ErrorContains(t, err, "moving ticket "+string(tk.ID)+" to open")
		require.ErrorContains(t, err, "the ticket is locked")
	})
}

// The lease sweep gives up on an agent whose lease it can't release, rather
// than handing back one it doesn't hold.
func TestASweepThatCantReleaseALeaseLeavesTheAttach(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "agents: {attach_lease: 30s}\n")
	s, a := f.working("t")
	f.attached(s, a, &agents.AttachRequest{})
	f.failOn("BEFORE UPDATE ON agents WHEN OLD.mode = 'attached' AND NEW.mode = 'programmatic'")
	f.clock.Advance(time.Hour)

	f.mgr.SweepLeases(f.ctx())

	got, err := f.state.Agents.Get(f.ctx(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, state.ModeAttached, got.Mode)
	assert.Equal(t, state.StateAttached, got.State, "not handed back, resumed or crashed")
	assert.NotNil(t, got.Lease)
	assert.NotContains(t, f.logs.String(), "handing back an expired attach")
}
