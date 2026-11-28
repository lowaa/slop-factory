package agents_test

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

// Tests of the daemon's drain (Manager.Stop): what it waits for, and what
// it does once its deadline has passed.

// onStop runs do on every stop the drain asks of agent's conversation,
// before the stop goes through; do is told how many came before.
func (f *fixture) onStop(agent ident.AgentID, do func(n int32)) {
	var calls atomic.Int32
	f.hooks.set(func(h *harnessHooks) {
		h.stop = func(id ident.AgentID) {
			if id == agent {
				do(calls.Add(1) - 1)
			}
		}
	})
}

// Past its deadline the drain kills what it was waiting for, and says so:
// an agent stuck mid-turn is killed, not recorded as crashed, and resumed
// by the next start, told its turn may have been cut short. A second Stop
// reports what the first did, rather than nil while the first's work may
// still be under way.
func TestADrainPastItsDeadlineKillsTheAgentAndReportsIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, a := f.working("t")
	ctx, cancel := context.WithCancel(f.ctx())
	// The deadline passes as the drain waits for the turn to end.
	f.onStop(a.ID, func(n int32) {
		if n == 0 {
			cancel()
		}
	})
	err := f.mgr.Stop(ctx)
	require.ErrorIs(t, err, context.Canceled)
	_, err = s.Await(f.ctx(), "never sent")
	require.ErrorContains(t, err, "session ended", "the agent stuck mid-turn was killed")

	require.ErrorIs(t, f.mgr.Stop(f.ctx()), context.Canceled, "a second Stop reports the first's result")
	back, err := f.state.Agents.Get(f.ctx(), a.ID)
	require.NoError(t, err)
	assert.Nil(t, back.EndedAt)
	assert.Equal(t, state.StateRefining, back.State, "killed by the drain, not crashed")

	f.startAgain()
	resumed := f.session()
	assert.Equal(t, s.ID, resumed.ID)
	assert.Contains(t, f.user(resumed).Text, "slop-bot restarted")
}

// A conversation whose graceful stop doesn't return (a harness that
// ignores it, a driver blocked) is forced once the deadline has passed:
// the drain is bounded by its ctx, not by the harness.
func TestAGracefulStopThatHangsIsForcedPastTheDeadline(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, a := f.working("t")
	ctx, cancel := context.WithCancel(f.ctx())
	hung := make(chan struct{})
	t.Cleanup(func() { close(hung) })
	f.onStop(a.ID, func(n int32) {
		if n == 0 {
			cancel()
			<-hung // the graceful stop never returns
		}
	})
	require.ErrorIs(t, f.mgr.Stop(ctx), context.Canceled)
	_, err := s.Await(f.ctx(), "never sent")
	require.ErrorContains(t, err, "session ended", "forced")
}

// A Stop that lands while another is draining waits for it, bounded by its
// own context, rather than returning nil at once.
func TestASecondStopWaitsForTheFirst(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, a := f.working("t")
	draining := make(chan struct{})
	f.onStop(a.ID, func(n int32) {
		if n == 0 {
			close(draining)
		}
	})
	first := make(chan error, 1)
	go func() { first <- f.mgr.Stop(f.ctx()) }()
	<-draining

	gaveUp, cancel := context.WithCancel(f.ctx())
	cancel()
	require.ErrorIs(t, f.mgr.Stop(gaveUp), context.Canceled, "the first is still waiting on the turn")
	f.do(s, fake.Command{Cmd: fake.CmdTurnEnd})
	require.NoError(t, <-first)
	require.NoError(t, f.mgr.Stop(f.ctx()))
}

// An ended agent's cleanup still running when the deadline passes is
// waited for, ForceStopGrace more, and reported if it outlasts that: the
// daemon closes the stores it writes to next.
func TestADrainPastItsDeadlineWaitsOutAnEndingAgentsCleanup(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	_, a := f.working("t")
	inCleanup, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release) })
	f.wts.mu.Lock()
	f.wts.beforeCleanup = func() { close(inCleanup); <-release }
	f.wts.mu.Unlock()
	f.setStatus(a.Ticket, tickets.StatusCancelled)
	<-inCleanup

	gaveUp, cancel := context.WithCancel(f.ctx())
	cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- f.mgr.Stop(gaveUp) }()
	// The grace runs on the fake clock: moved on until Stop gives up.
	for {
		select {
		case err := <-stopped:
			require.ErrorIs(t, err, context.Canceled)
			require.ErrorContains(t, err, "still being cleaned up", "waited for, then reported")
			return
		default:
			f.clock.Advance(agents.ForceStopGrace)
			runtime.Gosched()
		}
	}
}

// stopPastTheGrace stops f's manager with a deadline already passed, moving
// the fake clock on until the drain gives up on what it still waits for.
func (f *fixture) stopPastTheGrace() error {
	f.t.Helper()
	gaveUp, cancel := context.WithCancel(f.ctx())
	cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- f.mgr.Stop(gaveUp) }()
	for {
		select {
		case err := <-stopped:
			return err
		default:
			f.clock.Advance(agents.ForceStopGrace)
			runtime.Gosched()
		}
	}
}

// A start that ignores the drain's cancellation and is still in flight
// when the drain gives up on it can't bring a runner up after the drain
// has stopped the ones it had: its conversation is stopped instead. A
// resume is the case that matters: a spawn's kick-off fails on its ended
// context and cleans up, but a resume carries on.
func TestAStartThatOutlivesTheDrainIsStoppedNotAdopted(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	f.waitingAgent("t", nil)
	require.NoError(t, f.mgr.Stop(f.ctx()))
	entered, release := make(chan struct{}), make(chan struct{})
	f.hooks.set(func(h *harnessHooks) {
		h.ignoresCancel = true
		h.start = func(context.Context, *harness.Workspace, *harness.ConversationRef) error {
			close(entered)
			<-release
			return nil
		}
	})
	f.startAgain()
	<-entered
	err := f.stopPastTheGrace()
	require.ErrorContains(t, err, "starts still in flight")

	close(release)
	resumed := f.session()
	_, err = resumed.Await(f.ctx(), "never sent")
	require.ErrorContains(t, err, "session ended", "its conversation was stopped, not left running")
}

// A sweep still in flight when the drain's deadline has passed is waited
// for, ForceStopGrace more, and reported if it outlasts that; Exited
// closes only once it has gone, so the daemon doesn't close state.db under
// it.
func TestASweepOutlivingTheDrainIsReportedAndExitedWaitsForIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "agents: {attach_lease: 30s}\n")
	s, a := f.working("t")
	f.attached(s, a, &agents.AttachRequest{})
	inSweep, release := make(chan struct{}), make(chan struct{})
	// The sweep hands the expired attach back; its resume's start is held.
	f.hooks.set(func(h *harnessHooks) {
		h.start = func(context.Context, *harness.Workspace, *harness.ConversationRef) error {
			close(inSweep)
			<-release
			return errors.New("released")
		}
	})
	f.clock.Advance(time.Hour) // the lease expires, and the sweeper ticks
	<-inSweep
	err := f.stopPastTheGrace()
	require.ErrorContains(t, err, "sweeper still running")
	select {
	case <-f.mgr.Exited():
		require.FailNow(t, "exited with the sweep still in flight")
	default:
	}
	close(release)
	<-f.mgr.Exited()
}

// A spawn the fence turns away has made its agent's record and worktrees
// already: it is cleaned up as a start cut short by the stop is, released
// with its worktrees removed and its ticket not parked, or the record would
// keep the ticket from ever getting an agent.
func TestASpawnTurnedAwayByTheFenceLeavesNoLiveAgent(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	entered, release := make(chan struct{}), make(chan struct{})
	f.hooks.set(func(h *harnessHooks) {
		h.ignoresCancel = true
		h.start = func(context.Context, *harness.Workspace, *harness.ConversationRef) error {
			close(entered)
			<-release
			return nil
		}
	})
	tk := f.file("t", nil)
	<-entered
	require.ErrorContains(t, f.stopPastTheGrace(), "starts still in flight")
	close(release)
	f.mgr.WaitStarts()

	f.noLiveAgent(tk.ID)
	f.wts.mu.Lock()
	cleaned := append([]ident.TicketID(nil), f.wts.cleanups...)
	f.wts.mu.Unlock()
	assert.Equal(t, []ident.TicketID{tk.ID}, cleaned, "its worktrees were cleaned up")
	assert.NotEqual(t, tickets.StatusMisconfigured, f.ticket(tk.ID).Status, "not a ticket that can't run")
}

// A late start the fence turns away whose conversation won't stop (the
// driver's kill failed, the harness still there) keeps its worktrees: they
// aren't cleaned up under a harness still working in them, and its record
// is left for the next start. Exited waits while the start is still stopping
// its conversation.
func TestAFencedStartWhoseHarnessWontStopKeepsItsWorktrees(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	entered, release := make(chan struct{}), make(chan struct{})
	killing, failKill := make(chan struct{}), make(chan struct{})
	f.hooks.set(func(h *harnessHooks) {
		h.ignoresCancel = true
		h.start = func(context.Context, *harness.Workspace, *harness.ConversationRef) error {
			close(entered)
			<-release
			return nil
		}
		h.forceFails = func() error {
			close(killing)
			<-failKill
			return errors.New("the kill timed out")
		}
	})
	tk := f.file("t", nil)
	<-entered
	require.ErrorContains(t, f.stopPastTheGrace(), "starts still in flight")
	close(release)
	s := f.session()
	<-killing
	select {
	case <-f.mgr.Exited():
		require.FailNow(t, "exited while the late start was still stopping its conversation")
	default:
	}
	close(failKill)
	f.mgr.WaitStarts()

	f.wts.mu.Lock()
	cleaned := append([]ident.TicketID(nil), f.wts.cleanups...)
	f.wts.mu.Unlock()
	assert.Empty(t, cleaned, "no cleanup under a harness still running")
	_, err := s.Do(f.ctx(), fake.Command{Cmd: fake.CmdText, Text: "still here"})
	require.NoError(t, err, "the harness is still there")
	_ = f.agentFor(tk.ID) // its record is left live, for the next start
	<-f.mgr.Exited()
}
