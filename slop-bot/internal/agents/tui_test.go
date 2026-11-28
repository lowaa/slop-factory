package agents_test

import (
	"context"
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

// What a TUI driver's Send and Stop can refuse with a person in the
// session, and how the manager carries on. The fake rpc harness stands in;
// the hooks give it the TUI driver's errors.

// waitingMerge is an agent with nothing to do: its deliveries are the
// test's.
func (f *fixture) waitingMerge() (*fake.Session, ident.Principal) {
	f.t.Helper()
	s, _, agent := f.startedAgent("t", nil)
	f.turn(s, func() { require.NoError(f.t, f.mgr.ReadyToMerge(f.ctx(), agent, "")) })
	f.await(agents.EventState, toState(state.StateWaitingMerge))
	return s, agent
}

// failFirstSend makes agent's next Send fail with err, once.
func (f *fixture) failFirstSend(agent ident.AgentID, err error) {
	var failed atomic.Bool
	f.hooks.set(func(h *harnessHooks) {
		h.send = func(_ context.Context, a ident.AgentID, _ *harness.Message) error {
			if a == agent && failed.CompareAndSwap(false, true) {
				return err
			}
			return nil
		}
	})
}

func (f *fixture) messageStatus(agent ident.AgentID, body string) state.MessageStatus {
	f.t.Helper()
	msgs, err := f.state.Messages.Log(context.Background(), &state.LogQuery{Party: state.AgentParty(agent), Limit: 500})
	require.NoError(f.t, err)
	for _, m := range msgs {
		if m.Body == body {
			return m.Status
		}
	}
	require.FailNow(f.t, "no such message", body)
	return ""
}

func TestAPointerTypedButNotSubmittedIsntTypedAgain(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, agent := f.waitingMerge()
	f.failFirstSend(agent.Agent, harness.ErrUnsubmitted)
	_, err := f.mgr.Tell(f.ctx(), ident.UserPrincipal(), string(agent.Agent), "merge it", false)
	require.NoError(t, err)
	f.await(agents.EventState, withReason(state.StateIdle, "a person is in its session"))
	assert.Equal(t, state.StatusDelivered, f.messageStatus(agent.Agent, "merge it"),
		"its pointer is in the prompt: the inbox has it, and it isn't pointed at again")

	_, err = f.mgr.Tell(f.ctx(), ident.UserPrincipal(), string(agent.Agent), "and rebase", false)
	require.NoError(t, err)
	got := f.user(s).Text
	assert.Contains(t, got, "and rebase")
	assert.NotContains(t, got, "merge it")
}

func TestAMessageHeldForAPersonInTheSessionIsTriedAgain(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, agent := f.waitingMerge()
	timers := f.clock.Waiters()
	f.failFirstSend(agent.Agent, harness.ErrAttached)
	_, err := f.mgr.Tell(f.ctx(), ident.UserPrincipal(), string(agent.Agent), "merge it", false)
	require.NoError(t, err)
	f.await(agents.EventState, withReason(state.StateIdle, "a person is in its session"))
	assert.Equal(t, state.StatusQueued, f.messageStatus(agent.Agent, "merge it"), "nothing was typed")

	// Nothing says when the person leaves: the delivery is tried again on
	// the clock.
	f.blockUntil(timers + 1)
	f.clock.Advance(agents.HeldRetry)
	assert.Contains(t, f.user(s).Text, "merge it")
}

func TestAnAgentFinishesWithAPersonInItsSession(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, tk, agent := f.startedAgent("t", nil)
	f.hooks.set(func(h *harnessHooks) { h.attached = true })
	// Its own done stops it gracefully, which a TUI driver refuses with a
	// person in the session (one attached without slop-bot): the end
	// doesn't wait for them, the session is killed.
	f.turn(s, func() { require.NoError(t, f.mgr.Done(f.ctx(), agent, "shipped")) })
	f.await(agents.EventFinished, func(e *state.Event) bool { return e.Agent == agent.Agent })
	assert.Equal(t, tickets.StatusDone, f.ticket(tk.ID).Status)
}
