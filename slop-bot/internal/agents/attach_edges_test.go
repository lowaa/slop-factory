package agents_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

// restarted is a second manager on f's state, as after a daemon restart.
func (f *fixture) restarted() *agents.Manager {
	f.t.Helper()
	require.NoError(f.t, f.mgr.Stop(context.Background()))
	self, err := os.Executable()
	require.NoError(f.t, err)
	m := f.newManager(&fakeHarness{&fake.Harness{Binary: self, Control: f.ctl.Socket(), Logger: slog.New(slog.DiscardHandler), Clock: clock.Real{}}})
	require.NoError(f.t, m.Start(context.Background()))
	f.t.Cleanup(func() { _ = m.Stop(context.Background()) })
	f.t.Cleanup(func() { _ = f.ctl.Close() })
	return m
}

// A turn far longer than the lease can't cost the waiting attach anything:
// the lease is taken only once the conversation has stopped.
func TestALongTurnDoesntCostAPendingAttachItsLease(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "agents: {attach_lease: 30s}\n")
	s, a := f.working("t")
	got := make(chan error, 1)
	go func() {
		_, err := f.mgr.Attach(f.ctx(), &agents.AttachRequest{Target: string(a.ID), Holder: holder})
		got <- err
	}()
	f.await(agents.EventAttaching, nil)
	// A second attach meanwhile is refused, naming the one waiting.
	_, err := f.mgr.Attach(f.ctx(), &agents.AttachRequest{Target: string(a.ID), Holder: "someone else"})
	var held *state.LeaseHeldError
	require.ErrorAs(t, err, &held)
	assert.Equal(t, holder, held.Holder)

	f.clock.Advance(time.Hour)
	f.mgr.SweepLeases(f.ctx())
	f.do(s, fake.Command{Cmd: fake.CmdTurnEnd})
	require.NoError(t, <-got)
	attached, err := f.state.Agents.Get(f.ctx(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, state.StateAttached, attached.State)
	require.NotNil(t, attached.Lease)
	assert.True(t, attached.Lease.ExpiresAt.After(f.clock.Now()), "a lease from the end of the wait")
	require.NoError(t, f.mgr.Heartbeat(f.ctx(), a.ID, holder))
}

func TestAnAttachGivenUpMidWaitResumesTheAgent(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, a := f.working("t")
	ctx, cancel := context.WithCancel(f.ctx())
	got := make(chan error, 1)
	go func() {
		_, err := f.mgr.Attach(ctx, &agents.AttachRequest{Target: string(a.ID), Holder: holder})
		got <- err
	}()
	f.await(agents.EventAttaching, nil)
	cancel()
	require.ErrorIs(t, <-got, context.Canceled, "it returns without waiting for the turn")

	f.do(s, fake.Command{Cmd: fake.CmdTurnEnd})
	resumed := f.session()
	assert.Equal(t, s.ID, resumed.ID)
	f.user(resumed)
	back, err := f.state.Agents.Get(f.ctx(), a.ID)
	require.NoError(t, err)
	assert.Nil(t, back.Lease)
}

func TestDoneFromTheTUISurvivesARestart(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, a := f.working("t")
	f.attached(s, a, &agents.AttachRequest{})
	require.NoError(t, f.mgr.Done(f.ctx(), ident.AgentPrincipal(a.ID, a.Ticket), "Finished by hand."))
	m := f.restarted()
	out, err := m.HandBack(f.ctx(), a.ID, holder, "the person quit the TUI")
	require.NoError(t, err)
	assert.Equal(t, agents.Finished, out)
	f.await(agents.EventFinished, nil)
}

func TestAnAskStillOpenAfterTheHandBackWaitsForTheUser(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, a := f.working("t")
	_, err := f.mgr.Ask(f.ctx(), ident.AgentPrincipal(a.ID, a.Ticket), "CSV or XLSX?", nil, "")
	require.NoError(t, err)
	f.do(s, fake.Command{Cmd: fake.CmdTurnEnd})
	f.await(agents.EventState, toState(state.StateWaitingUser))
	_, err = f.mgr.Attach(f.ctx(), &agents.AttachRequest{Target: string(a.ID), Holder: holder})
	require.NoError(t, err)

	_, err = f.mgr.HandBack(f.ctx(), a.ID, holder, "the person quit the TUI")
	require.NoError(t, err)
	resumed := f.session()
	f.user(resumed)
	f.turn(resumed, nil)
	f.await(agents.EventState, toState(state.StateWaitingUser))
}

func TestTheSweepLeavesAnAttachWhoseStreamIsOpen(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "agents: {attach_lease: 30s}\n")
	s, a := f.working("t")
	f.attached(s, a, &agents.AttachRequest{})
	closeStream := f.mgr.StreamOpen(a.ID)
	// Woken from a suspend: the lease is long expired, the CLI still there.
	f.clock.Advance(time.Hour)
	f.mgr.SweepLeases(f.ctx())
	require.NoError(t, f.mgr.Heartbeat(f.ctx(), a.ID, holder), "its heartbeat renews the expired lease")

	closeStream()
	f.clock.Advance(time.Hour)
	f.mgr.SweepLeases(f.ctx())
	ev := f.await(agents.EventHandedBack, nil)
	assert.Contains(t, string(ev.Payload), "lease expired")
}

func TestAHandBackCutShortByARestartResumesTheAgent(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, a := f.working("t")
	f.attached(s, a, &agents.AttachRequest{})
	require.NoError(t, f.mgr.Stop(context.Background()))
	// The daemon died after releasing the lease, before resuming.
	require.NoError(t, f.state.Agents.ReleaseLease(f.ctx(), a.ID, holder))

	self, err := os.Executable()
	require.NoError(t, err)
	m := f.newManager(&fakeHarness{&fake.Harness{Binary: self, Control: f.ctl.Socket(), Logger: slog.New(slog.DiscardHandler), Clock: clock.Real{}}})
	require.NoError(t, m.Start(context.Background()))
	t.Cleanup(func() { _ = m.Stop(context.Background()) })
	t.Cleanup(func() { _ = f.ctl.Close() })
	resumed := f.session()
	assert.Equal(t, s.ID, resumed.ID)
	assert.Contains(t, f.user(resumed).Text, "slop-bot restarted")
}

func TestAttachingAnAgentQueuedForASlotDoesntStallItsPool(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "concurrency: {max_active: 2, max_live: 4}\nagents: {nudges: 2}\n")
	first := f.file("first", nil)
	s1 := f.session()
	f.user(s1)
	a1 := f.agentFor(first.ID)
	f.turn(s1, func() {
		_, err := f.mgr.Ask(f.ctx(), ident.AgentPrincipal(a1.ID, first.ID), "?", nil, "")
		require.NoError(t, err)
	})
	f.await(agents.EventState, toState(state.StateWaitingUser))
	// Two more agents take both slots.
	f.file("second", nil)
	s2 := f.session()
	f.user(s2)
	f.do(s2, fake.Command{Cmd: fake.CmdTurnStart})
	third := f.file("third", nil)
	s3 := f.session()
	f.user(s3)
	a3 := f.agentFor(third.ID)
	f.do(s3, fake.Command{Cmd: fake.CmdTurnStart})
	// The answer queues the first for a slot; then it's attached, which
	// gives it one of its own.
	_, err := f.mgr.Tell(f.ctx(), ident.UserPrincipal(), string(a1.ID), "go ahead", false)
	require.NoError(t, err)
	_, err = f.mgr.Attach(f.ctx(), &agents.AttachRequest{Target: string(a1.ID), Holder: holder})
	require.NoError(t, err)

	// The third waits on the user, freeing a slot; the second's nudge must
	// get it, with the attached agent no longer queued ahead of it.
	f.turn(s3, func() {
		_, err := f.mgr.Ask(f.ctx(), ident.AgentPrincipal(a3.ID, third.ID), "?", nil, "")
		require.NoError(t, err)
	})
	f.do(s2, fake.Command{Cmd: fake.CmdTurnEnd})
	nudge := f.user(s2)
	assert.Contains(t, nudge.Text, "without saying why")
}
