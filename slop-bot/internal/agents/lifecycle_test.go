package agents_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

// Tests of how an agent's life ends, and how it continues past a failure:
// no ticket may be left behind a live agent that nothing runs.

func (f *fixture) setStatus(id ident.TicketID, to tickets.Status) {
	f.t.Helper()
	_, err := f.tickets.Update(context.Background(), ident.UserPrincipal(), id, &tickets.TicketChange{Status: &to})
	require.NoError(f.t, err)
}

// openAndStarted files an open ticket and waits for its agent's kick-off
// and for the ticket's move to in_progress, which the agent's start makes.
func (f *fixture) openAndStarted(title string) (*fake.Session, *tickets.Ticket, ident.Principal) {
	f.t.Helper()
	started := f.racing.arm(func() {})
	tk := f.file(title, func(nt *tickets.NewTicket) { nt.Status = tickets.StatusOpen })
	s := f.session()
	f.user(s)
	select {
	case err := <-started:
		require.NoError(f.t, err)
	case <-f.ctx().Done():
		f.t.Fatal("the ticket was never moved on")
	}
	require.Equal(f.t, tickets.StatusInProgress, f.ticket(tk.ID).Status)
	a := f.agentFor(tk.ID)
	return s, tk, ident.AgentPrincipal(a.ID, tk.ID)
}

func (f *fixture) noLiveAgent(ticket ident.TicketID) {
	f.t.Helper()
	_, err := f.state.Agents.LiveForTicket(context.Background(), ticket)
	require.ErrorIs(f.t, err, state.ErrNotFound)
}

func reason(e *state.Event) string {
	var p map[string]string
	_ = json.Unmarshal(e.Payload, &p)
	return p["reason"]
}

func TestAnOpenTicketIsStartedWhenItsAgentIs(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	// openAndStarted asserts the move: no ticket event follows the spawn to
	// trigger it.
	f.openAndStarted("t")
}

func TestAFailedStartReleasesTheAgentAndParksTheTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	f.hooks.set(func(h *harnessHooks) {
		h.start = func(context.Context, *harness.Workspace, *harness.ConversationRef) error {
			return errors.New("no such binary")
		}
	})
	tk := f.file("t", nil)
	released := f.await(agents.EventReleased, nil)
	f.await(agents.EventMisconfigure, nil)
	a, err := f.state.Agents.Get(context.Background(), released.Agent)
	require.NoError(t, err)
	assert.Equal(t, state.StateReleased, a.State)
	f.noLiveAgent(tk.ID)
	assert.Equal(t, tickets.StatusMisconfigured, f.ticket(tk.ID).Status, "parked, not retried in a loop")
	assert.Equal(t, []ident.TicketID{tk.ID}, f.wts.cleanups, "its worktrees were cleaned up")

	// Once fixed, the user requeues it and it gets a new agent.
	f.hooks.set(func(h *harnessHooks) { h.start = nil })
	f.setStatus(tk.ID, tickets.StatusPreparing)
	f.mgr.Kick()
	f.await(agents.EventSpawned, func(e *state.Event) bool { return e.Ticket == tk.ID })
	f.user(f.session())
}

func TestACrashedAgentIsResumed(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	tk := f.file("t", nil)
	s := f.session()
	f.user(s)
	f.do(s, fake.Command{Cmd: fake.CmdTurnStart})
	f.do(s, fake.Command{Cmd: fake.CmdExit, Code: 3})
	f.await(agents.EventState, toState(state.StateCrashed))

	resumed := f.session()
	assert.True(t, resumed.Resumed)
	assert.Equal(t, s.ID, resumed.ID)
	assert.Contains(t, f.user(resumed).Text, "ended unexpectedly")
	assert.Equal(t, 1, f.agentFor(tk.ID).Recoveries)
	f.turn(resumed, nil)
	f.await("harness.turn_ended", nil)
	assert.Equal(t, 0, f.agentFor(tk.ID).Recoveries, "a finished turn ends the run of crashes")
}

func TestAnAgentThatKeepsCrashingIsReleased(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	tk := f.file("t", nil)
	s := f.session()
	f.user(s)
	for range 3 {
		f.do(s, fake.Command{Cmd: fake.CmdExit, Code: 3})
		s = f.session()
		require.True(t, s.Resumed)
		f.user(s)
	}
	f.do(s, fake.Command{Cmd: fake.CmdExit, Code: 3})
	released := f.await(agents.EventReleased, nil)
	assert.Contains(t, reason(released), "died 4 times")
	f.await(agents.EventMisconfigure, nil)
	f.noLiveAgent(tk.ID)
	assert.Equal(t, tickets.StatusMisconfigured, f.ticket(tk.ID).Status)
	assert.Equal(t, []ident.TicketID{tk.ID}, f.wts.cleanups)
}

func TestMarkingTheTicketDoneEndsItsAgent(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, tk, _ := f.openAndStarted("t")
	f.do(s, fake.Command{Cmd: fake.CmdTurnStart})
	f.setStatus(tk.ID, tickets.StatusDone)
	// Mid-turn: the work is no longer wanted, so the turn is cut off.
	finished := f.await(agents.EventFinished, nil)
	ended, err := f.state.Agents.Get(context.Background(), finished.Agent)
	require.NoError(t, err)
	assert.Equal(t, state.StateDone, ended.State)
	assert.Equal(t, []ident.TicketID{tk.ID}, f.wts.cleanups)
}

func TestATicketCancelledWhileTheDaemonWasDownEndsItsAgent(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, tk, agent := f.startedAgent("t", nil)
	f.turn(s, func() {
		_, err := f.mgr.Ask(f.ctx(), agent, "?", nil, "")
		require.NoError(t, err)
	})
	f.await(agents.EventState, toState(state.StateWaitingUser))
	var resumes atomic.Int32
	f.hooks.set(func(h *harnessHooks) {
		h.start = func(_ context.Context, _ *harness.Workspace, resume *harness.ConversationRef) error {
			if resume != nil {
				resumes.Add(1)
			}
			return nil
		}
	})
	f.restartWith(func() { f.setStatus(tk.ID, tickets.StatusCancelled) })
	f.await(agents.EventFinished, func(e *state.Event) bool { return e.Agent == agent.Agent })
	assert.Zero(t, resumes.Load(), "ended from its record, never resumed into the cancelled work")
	f.noLiveAgent(tk.ID)
	assert.Equal(t, []ident.TicketID{tk.ID}, f.wts.cleanups)
}

func TestAHungStartHoldsUpNeitherOtherTicketsNorStop(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	entered := make(chan struct{})
	var first atomic.Bool
	f.hooks.set(func(h *harnessHooks) {
		h.start = func(ctx context.Context, _ *harness.Workspace, _ *harness.ConversationRef) error {
			if first.CompareAndSwap(false, true) {
				close(entered)
				<-ctx.Done() // a handshake that never answers
				return ctx.Err()
			}
			return nil
		}
	})
	hung := f.file("hangs", nil)
	<-entered
	other := f.file("other", nil)
	f.await(agents.EventSpawned, func(e *state.Event) bool { return e.Ticket == other.ID })
	s := f.session()
	f.user(s)
	// Settled, so the drain has nothing of its to wait for.
	a := f.agentFor(other.ID)
	f.turn(s, func() {
		_, err := f.mgr.Ask(f.ctx(), ident.AgentPrincipal(a.ID, other.ID), "?", nil, "")
		require.NoError(t, err)
	})
	f.await(agents.EventState, toState(state.StateWaitingUser))

	require.NoError(t, f.mgr.Stop(f.ctx()), "Stop gives up on the start rather than waiting on it")
	released := f.await(agents.EventReleased, func(e *state.Event) bool { return e.Ticket == hung.ID })
	assert.Contains(t, reason(released), "daemon stopped")
	f.noLiveAgent(hung.ID)
}

func TestDoneMovesAnInProgressTicketStraightToDone(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	blocker := f.file("blocker", func(nt *tickets.NewTicket) { nt.Assignee = "" })
	s, tk, agent := f.openAndStarted("t")
	// Blocked mid-work, by a follow-up it filed, say: in_progress to done
	// is still a legal move, and must not detour through open.
	_, err := f.tickets.AddBlocker(context.Background(), ident.UserPrincipal(), tk.ID, blocker.ID)
	require.NoError(t, err)
	f.user(s) // the blocker notice
	f.turn(s, func() { require.NoError(t, f.mgr.Done(f.ctx(), agent, "shipped")) })
	f.await(agents.EventFinished, nil)
	assert.Equal(t, tickets.StatusDone, f.ticket(tk.ID).Status)
}

func TestDoneIsRefusedForATicketThatCantGetThere(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	blocker := f.file("blocker", func(nt *tickets.NewTicket) { nt.Assignee = "" })
	_, tk, agent := f.startedAgent("t", func(nt *tickets.NewTicket) { nt.BlockedOn = []ident.TicketID{blocker.ID} })
	err := f.mgr.Done(f.ctx(), agent, "x")
	require.ErrorIs(t, err, agents.ErrCantFinish, "its work can't start, so it can't be done")
	assert.Equal(t, tickets.StatusPreparing, f.ticket(tk.ID).Status)
}

func TestDoneIsRefusedInADiscussion(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, tk, agent := f.startedAgent("t", nil)
	f.turn(s, func() {
		require.NoError(t, f.mgr.Discuss(f.ctx(), agent, "Drop the column?", nil, ""))
		err := f.mgr.Done(f.ctx(), agent, "dropped it")
		require.ErrorIs(t, err, agents.ErrCantFinish, "the user resolves a discussion, not the agent")
	})
	f.await(agents.EventState, toState(state.StateDiscussion))
	assert.Equal(t, tickets.StatusDiscussion, f.ticket(tk.ID).Status)
}
