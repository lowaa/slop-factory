package agents_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

// Tests of max_live: past it, a start stops the longest-waiting idle agent
// (parks it), and a parked agent is resumed by id when it next has work.

// twoLive is one normal slot, one urgent, and processes for two agents.
const twoLive = "concurrency: {max_active: 1, max_live: 2}\nagents: {nudges: 2}\n"

// waitingAgent files a ticket whose agent asks a question and waits on it.
func (f *fixture) waitingAgent(title string, mut func(*tickets.NewTicket)) (*fake.Session, ident.Principal) {
	f.t.Helper()
	s, _, p := f.startedAgent(title, mut)
	f.turn(s, func() { f.ask(p) })
	f.await(agents.EventState, func(e *state.Event) bool { return e.Agent == p.Agent && toState(state.StateWaitingUser)(e) })
	return s, p
}

func of(agent ident.AgentID) func(*state.Event) bool {
	return func(e *state.Event) bool { return e.Agent == agent }
}

func (f *fixture) tell(to ident.AgentID, text string) {
	f.t.Helper()
	_, err := f.mgr.Tell(f.ctx(), ident.UserPrincipal(), string(to), text, false)
	require.NoError(f.t, err)
}

func (f *fixture) pending(agent ident.AgentID) int {
	f.t.Helper()
	msgs, err := f.state.Messages.Pending(context.Background(), state.AgentParty(agent))
	require.NoError(f.t, err)
	return len(msgs)
}

func TestPastMaxLiveTheLongestWaitingAgentIsParkedAndResumedForItsAnswer(t *testing.T) {
	t.Parallel()
	f := newFixture(t, twoLive)
	s1, a1 := f.waitingAgent("first", nil)
	f.clock.Advance(time.Minute) // a1 has waited longest
	_, a2 := f.waitingAgent("second", nil)

	f.file("third", nil)
	parked := f.await(agents.EventParked, nil)
	assert.Equal(t, a1.Agent, parked.Agent)
	s3 := f.session()
	f.user(s3)
	rec, err := f.state.Agents.Get(context.Background(), a1.Agent)
	require.NoError(t, err)
	require.NotNil(t, rec.ParkedAt)
	assert.Equal(t, state.StateWaitingUser, rec.State, "it still waits on the user")
	assert.True(t, f.mgr.IsParked(a1.Agent))

	// The third holds the only slot: the answer queues, and the parked
	// agent with it.
	f.do(s3, fake.Command{Cmd: fake.CmdTurnStart})
	f.tell(a1.Agent, "CSV")
	assert.Equal(t, 1, f.pending(a1.Agent))

	// The third waits too: the slot frees, and a2, waiting longest now, is
	// parked to give a1 back a process.
	f.clock.Advance(time.Minute)
	a3 := f.agentFor(f.ticketTitled("third"))
	f.ask(ident.AgentPrincipal(a3.ID, a3.Ticket))
	f.do(s3, fake.Command{Cmd: fake.CmdTurnEnd})
	f.await(agents.EventParked, of(a2.Agent))
	resumed := f.session()
	assert.True(t, resumed.Resumed)
	assert.Equal(t, s1.ID, resumed.ID, "resumed by its conversation id")
	assert.Contains(t, f.user(resumed).Text, "CSV")
	rec, err = f.state.Agents.Get(context.Background(), a1.Agent)
	require.NoError(t, err)
	assert.Nil(t, rec.ParkedAt)
	assert.False(t, f.mgr.IsParked(a1.Agent))
}

// An agent in a turn is never parked, even when it has been in its state
// longer than the idle one.
func TestAnAgentInATurnIsNeverParked(t *testing.T) {
	t.Parallel()
	f := newFixture(t, twoLive)
	su, _, urgent := f.startedAgent("urgent", func(nt *tickets.NewTicket) { nt.Urgent = true })
	f.do(su, fake.Command{Cmd: fake.CmdTurnStart})
	f.await(agents.EventState, func(e *state.Event) bool { return e.Agent == urgent.Agent && toState(state.StateRefining)(e) })
	f.clock.Advance(time.Minute)
	_, idle := f.waitingAgent("normal", nil)

	f.file("new", nil)
	parked := f.await(agents.EventParked, nil)
	assert.Equal(t, idle.Agent, parked.Agent)
	assert.False(t, f.mgr.IsParked(urgent.Agent))
}

// A parked agent waiting on its blockers is resumed when they are done, to
// be told so: a ticket change, not only a message, is work for it.
func TestAParkedAgentIsResumedWhenItsBlockersAreDone(t *testing.T) {
	t.Parallel()
	f := newFixture(t, twoLive)
	blocker := f.unassigned("blocker")
	s1, tk, a1 := f.startedAgent("blocked", func(nt *tickets.NewTicket) { nt.BlockedOn = []ident.TicketID{blocker.ID} })
	f.turn(s1, nil)
	f.await(agents.EventState, withReason(state.StateIdle, "waiting on its ticket's blockers"))
	f.clock.Advance(time.Minute)
	_, _ = f.waitingAgent("second", nil)
	f.file("third", nil)
	f.await(agents.EventParked, of(a1.Agent))
	s3 := f.session()
	f.user(s3)
	f.clock.Advance(time.Minute)
	a3 := f.agentFor(f.ticketTitled("third"))
	f.turn(s3, func() { f.ask(ident.AgentPrincipal(a3.ID, a3.Ticket)) })
	f.await(agents.EventState, func(e *state.Event) bool { return e.Agent == a3.ID && toState(state.StateWaitingUser)(e) })

	f.setStatus(blocker.ID, tickets.StatusOpen)
	f.setStatus(blocker.ID, tickets.StatusInProgress)
	f.setStatus(blocker.ID, tickets.StatusDone)
	resumed := f.session()
	assert.Equal(t, s1.ID, resumed.ID)
	assert.Contains(t, f.user(resumed).Text, "blockers are done: finish refining it")
	assert.Equal(t, tickets.StatusPreparing, f.ticket(tk.ID).Status)
}

// A restart leaves a parked agent parked: it gets no process until it has
// work.
func TestARestartLeavesAParkedAgentParked(t *testing.T) {
	t.Parallel()
	f := newFixture(t, twoLive)
	_, a1 := f.waitingAgent("first", nil)
	f.clock.Advance(time.Minute)
	_, _ = f.waitingAgent("second", nil)
	f.file("third", nil)
	f.await(agents.EventParked, of(a1.Agent))
	s3 := f.session()
	f.user(s3)
	f.clock.Advance(time.Minute)
	a3 := f.agentFor(f.ticketTitled("third"))
	f.turn(s3, func() { f.ask(ident.AgentPrincipal(a3.ID, a3.Ticket)) })
	f.await(agents.EventState, func(e *state.Event) bool { return e.Agent == a3.ID && toState(state.StateWaitingUser)(e) })

	m := f.restartWith(nil)
	assert.True(t, m.IsParked(a1.Agent))
	// Only the other two are resumed, then a1 is for its answer, past a
	// third park.
	f.session()
	f.session()
	f.tell(a1.Agent, "CSV")
	resumed := f.session()
	assert.Contains(t, f.user(resumed).Text, "CSV")
	assert.False(t, m.IsParked(a1.Agent))
}

// A parked agent at the head of the queue, with every live agent queued
// behind it, parks one of them for its place: sparing queued agents would
// leave the whole pool waiting for good.
func TestAParkedHeadOfTheQueueParksAnAgentQueuedBehindIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t, twoLive)
	s1, a1 := f.waitingAgent("first", nil)
	f.clock.Advance(time.Minute)
	_, a2 := f.waitingAgent("second", nil)
	f.file("third", nil)
	f.await(agents.EventParked, of(a1.Agent))
	s3 := f.session()
	f.user(s3)
	a3 := f.agentFor(f.ticketTitled("third"))
	f.do(s3, fake.Command{Cmd: fake.CmdTurnStart}) // the third holds the slot

	f.tell(a1.Agent, "for a1")
	f.tell(a2.Agent, "for a2")
	assert.Equal(t, []ident.TicketID{a1.Ticket, a2.Ticket}, f.queuedTickets(state.PoolNormal))
	// Interrupted, the third's turn ends with a message to deliver: it
	// queues behind a1 too, and the slot is free.
	_, err := f.mgr.Tell(f.ctx(), ident.UserPrincipal(), string(a3.ID), "stop", true)
	require.NoError(t, err)

	resumed := f.session()
	assert.Equal(t, s1.ID, resumed.ID)
	assert.Contains(t, f.user(resumed).Text, "for a1")
}

// A message left for a parked agent with no queue place (the daemon died
// between the two writes) still wakes it after a restart.
func TestAParkedAgentsMessageWithoutAQueuePlaceWakesItAfterARestart(t *testing.T) {
	t.Parallel()
	f := newFixture(t, twoLive)
	s1, a1 := f.waitingAgent("first", nil)
	f.clock.Advance(time.Minute)
	_, _ = f.waitingAgent("second", nil)
	f.file("third", nil)
	f.await(agents.EventParked, of(a1.Agent))
	s3 := f.session()
	f.user(s3)
	f.clock.Advance(time.Minute)
	a3 := f.agentFor(f.ticketTitled("third"))
	f.turn(s3, func() { f.ask(ident.AgentPrincipal(a3.ID, a3.Ticket)) })
	f.await(agents.EventState, func(e *state.Event) bool { return e.Agent == a3.ID && toState(state.StateWaitingUser)(e) })

	f.restartWith(func() {
		_, err := f.state.Messages.Enqueue(context.Background(), &state.NewMessage{
			From: state.UserParty, To: state.AgentParty(a1.Agent), Kind: state.KindTell, Body: "lost its place",
		})
		require.NoError(t, err)
	})
	for range 3 {
		if s := f.session(); s.ID == s1.ID {
			assert.Contains(t, f.user(s).Text, "lost its place")
			return
		}
	}
	t.Fatal("the parked agent was never resumed")
}

func (f *fixture) ticketTitled(title string) ident.TicketID {
	f.t.Helper()
	ts, err := f.tickets.List(context.Background(), &tickets.Query{})
	require.NoError(f.t, err)
	for _, tk := range ts {
		if tk.Title == title {
			return tk.ID
		}
	}
	f.t.Fatalf("no ticket %q", title)
	return ""
}
