package agents_test

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
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
