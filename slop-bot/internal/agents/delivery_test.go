package agents_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
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

// Tests of what reaches an agent, when, and what it means: blocker
// notices, answers, declarations, and delivery under concurrency.

func (f *fixture) ask(agent ident.Principal) {
	f.t.Helper()
	_, err := f.mgr.Ask(f.ctx(), agent, "CSV or XLSX?", nil, "")
	require.NoError(f.t, err)
}

func (f *fixture) openAsks(agent ident.AgentID) int {
	f.t.Helper()
	asks, err := f.state.Messages.OpenAsksFrom(context.Background(), agent)
	require.NoError(f.t, err)
	return len(asks)
}

// systemMessages are the notices slop-bot sent agent whose body has substr.
func (f *fixture) systemMessages(agent ident.AgentID, substr string) int {
	f.t.Helper()
	msgs, err := f.state.Messages.Log(context.Background(), &state.LogQuery{Party: state.AgentParty(agent), Limit: 500})
	require.NoError(f.t, err)
	n := 0
	for _, m := range msgs {
		if m.Kind == state.KindSystem && strings.Contains(m.Body, substr) {
			n++
		}
	}
	return n
}

func withReason(to state.AgentState, why string) func(*state.Event) bool {
	return func(e *state.Event) bool {
		var p map[string]string
		_ = json.Unmarshal(e.Payload, &p)
		return p["to"] == string(to) && p["reason"] == why
	}
}

func (f *fixture) unassigned(title string) *tickets.Ticket {
	return f.file(title, func(nt *tickets.NewTicket) { nt.Assignee = "" })
}

func TestABlockerAddedMidWorkIsANoticeAndItsRemovalWakesTheAgent(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	blocker := f.unassigned("blocker")
	s, tk, agent := f.openAndStarted("t")
	f.do(s, fake.Command{Cmd: fake.CmdTurnStart})
	_, err := f.tickets.AddBlocker(context.Background(), ident.UserPrincipal(), tk.ID, blocker.ID)
	require.NoError(t, err)
	got := f.user(s)
	assert.True(t, got.MidTurn)
	assert.Contains(t, got.Text, "[slop-bot] Your ticket is now blocked by "+string(blocker.ID))
	assert.NotContains(t, got.Text, "Message from the user", "slop-bot's notice carries no one's authority")

	// Told to wait, it waits: no nudge.
	f.do(s, fake.Command{Cmd: fake.CmdTurnEnd})
	f.await(agents.EventState, withReason(state.StateIdle, "waiting on its ticket's blockers"))

	// An edit that doesn't change what blocks it isn't a change to tell.
	title := "renamed"
	_, err = f.tickets.Update(context.Background(), ident.UserPrincipal(), tk.ID, &tickets.TicketChange{Title: &title})
	require.NoError(t, err)
	_, err = f.tickets.RemoveBlocker(context.Background(), ident.UserPrincipal(), tk.ID, blocker.ID)
	require.NoError(t, err)
	got = f.user(s)
	assert.False(t, got.MidTurn)
	assert.Contains(t, got.Text, "blockers are done: carry on")
	assert.Equal(t, 1, f.systemMessages(agent.Agent, "now blocked"), "told once, the feed's redeliveries and other edits aside")
	assert.Equal(t, 0, f.agentFor(tk.ID).Nudges)
}

func TestARefiningAgentWaitsForItsBlockersAndIsToldWhenTheyAreDone(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	blocker := f.unassigned("blocker")
	s, tk, _ := f.startedAgent("t", func(nt *tickets.NewTicket) { nt.BlockedOn = []ident.TicketID{blocker.ID} })
	f.turn(s, nil) // refined, and stops
	f.await(agents.EventState, withReason(state.StateIdle, "waiting on its ticket's blockers"))
	f.setStatus(blocker.ID, tickets.StatusOpen)
	f.setStatus(blocker.ID, tickets.StatusInProgress)
	f.setStatus(blocker.ID, tickets.StatusDone)
	assert.Contains(t, f.user(s).Text, "blockers are done: finish refining it")
	assert.Equal(t, tickets.StatusPreparing, f.ticket(tk.ID).Status)
}

func TestABlockerNoticeAnswersNoQuestion(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	blocker := f.unassigned("blocker")
	s, tk, agent := f.openAndStarted("t")
	f.turn(s, func() { f.ask(agent) })
	f.await(agents.EventState, toState(state.StateWaitingUser))
	_, err := f.tickets.AddBlocker(context.Background(), ident.UserPrincipal(), tk.ID, blocker.ID)
	require.NoError(t, err)
	assert.Contains(t, f.user(s).Text, "now blocked")
	assert.Equal(t, 1, f.openAsks(agent.Agent), "the question still waits for the user")
}

func TestABroadcastAnswersNoQuestion(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, _, agent := f.startedAgent("t", nil)
	f.turn(s, func() { f.ask(agent) })
	f.await(agents.EventState, toState(state.StateWaitingUser))
	_, err := f.mgr.TellAll(f.ctx(), ident.UserPrincipal(), "main is red")
	require.NoError(t, err)
	assert.Contains(t, f.user(s).Text, "main is red")
	assert.Equal(t, 1, f.openAsks(agent.Agent))
}

func TestAnAnswerMidTurnClosesTheAsk(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, tk, agent := f.startedAgent("t", nil)
	f.do(s, fake.Command{Cmd: fake.CmdTurnStart})
	// ask returns at once, so the answer can come before the turn ends.
	f.ask(agent)
	_, err := f.mgr.Tell(f.ctx(), ident.UserPrincipal(), string(tk.ID), "CSV", false)
	require.NoError(t, err)
	got := f.user(s)
	assert.True(t, got.MidTurn)
	assert.Contains(t, got.Text, "CSV")
	assert.Equal(t, 0, f.openAsks(agent.Agent), "answered")
	f.do(s, fake.Command{Cmd: fake.CmdTurnEnd})
	// Nothing to wait for: the turn's end without a reason is nudged.
	f.await(agents.EventNudged, nil)
}

func TestADeclarationSurvivesAMessageDeliveredAtTheTurnsEnd(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	// A driver that can't steer holds mid-turn messages for the turn's end.
	f.hooks.set(func(h *harnessHooks) { h.noSteer = true })
	_, one, other := f.startedAgent("one", nil)
	s, two, agent := f.startedAgent("two", nil)
	f.do(s, fake.Command{Cmd: fake.CmdTurnStart})
	f.ask(agent)
	_, err := f.mgr.Tell(f.ctx(), other, string(two.ID), "I'm changing paths.go", false)
	require.NoError(t, err)
	f.do(s, fake.Command{Cmd: fake.CmdTurnEnd})
	assert.Contains(t, f.user(s).Text, "I'm changing paths.go")
	// The agent takes that turn and says nothing new: it's still waiting
	// on the user.
	f.turn(s, nil)
	f.await(agents.EventState, func(e *state.Event) bool {
		return e.Agent == agent.Agent && toState(state.StateWaitingUser)(e)
	})
	assert.Equal(t, 0, f.agentFor(two.ID).Nudges)
	_ = one
}

func TestConcurrentTellsDeliverEachMessageOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, tk, agent := f.startedAgent("t", nil)
	f.turn(s, func() { require.NoError(t, f.mgr.ReadyToMerge(f.ctx(), agent, "")) })
	f.await(agents.EventState, toState(state.StateWaitingMerge))

	// The first send blocks until every tell has queued its message, so the
	// others all reach delivery while it is in flight.
	entered, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	f.hooks.set(func(h *harnessHooks) {
		h.send = func(_ context.Context, a ident.AgentID, _ *harness.Message) error {
			if a == agent.Agent && first.CompareAndSwap(false, true) {
				close(entered)
				<-release
			}
			return nil
		}
	})
	const n = 4
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			_, err := f.mgr.Tell(context.Background(), ident.UserPrincipal(), string(tk.ID), fmt.Sprintf("m%d", i), false)
			assert.NoError(t, err)
		})
	}
	<-entered
	for range n {
		f.await(agents.EventQueued, func(e *state.Event) bool { return e.Agent == agent.Agent })
	}
	close(release)
	wg.Wait()

	evs, err := f.state.Events.ReadAfter(context.Background(), 0, 500)
	require.NoError(t, err)
	delivered := 0
	for _, e := range evs {
		if e.Type == agents.EventDelivered && e.Agent == agent.Agent {
			var p map[string]any
			require.NoError(t, json.Unmarshal(e.Payload, &p))
			delivered += int(p["count"].(float64))
		}
	}
	// The kick-off isn't an inbox message; the four tells are, each once.
	assert.Equal(t, n, delivered)
}

func TestAFailedDeliveryGivesItsSlotBack(t *testing.T) {
	t.Parallel()
	f := newFixture(t, oneSlot)
	s, _, agent := f.startedAgent("first", nil)
	f.turn(s, func() { require.NoError(t, f.mgr.ReadyToMerge(f.ctx(), agent, "")) })
	f.await(agents.EventState, toState(state.StateWaitingMerge))
	f.hooks.set(func(h *harnessHooks) {
		h.send = func(_ context.Context, a ident.AgentID, _ *harness.Message) error {
			if a == agent.Agent {
				return errors.New("the pipe broke")
			}
			return nil
		}
	})
	_, err := f.mgr.Tell(f.ctx(), ident.UserPrincipal(), string(agent.Agent), "merge it", false)
	require.NoError(t, err, "queued, whatever the delivery did")
	// The only slot is free again: new work gets it.
	second := f.file("second", nil)
	f.await(agents.EventSpawned, func(e *state.Event) bool { return e.Ticket == second.ID })
}

func TestMessagesQueuedBeforeARestartAreDeliveredAfterIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, _, agent := f.startedAgent("t", nil)
	f.turn(s, func() { f.ask(agent) })
	f.await(agents.EventState, toState(state.StateWaitingUser))
	// The answer is queued but can't be delivered before the daemon stops.
	f.hooks.set(func(h *harnessHooks) {
		h.send = func(context.Context, ident.AgentID, *harness.Message) error { return errors.New("not now") }
	})
	_, err := f.mgr.Tell(f.ctx(), ident.UserPrincipal(), string(agent.Agent), "go ahead", false)
	require.NoError(t, err)
	f.restartWith(func() { f.hooks.set(func(h *harnessHooks) { h.send = nil }) })
	resumed := f.session()
	require.Equal(t, s.ID, resumed.ID)
	assert.Contains(t, f.user(resumed).Text, "go ahead")
}

func TestARepoAddedToALiveTicketGetsAWorktree(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, tk, _ := f.startedAgent("t", func(nt *tickets.NewTicket) { nt.Repos = nil })
	assert.Empty(t, f.agentFor(tk.ID).Worktrees)
	f.do(s, fake.Command{Cmd: fake.CmdTurnStart})
	repos := []string{"app"}
	_, err := f.tickets.Update(context.Background(), ident.UserPrincipal(), tk.ID, &tickets.TicketChange{Repos: &repos})
	require.NoError(t, err)
	got := f.user(s)
	assert.Contains(t, got.Text, "also touches these repos")
	wts := f.agentFor(tk.ID).Worktrees
	require.Len(t, wts, 1)
	assert.Contains(t, got.Text, wts[0].Path)
	assert.True(t, wts[0].Primary, "it had none, so it is the agent's main worktree")
}

// A steer sent into a turn being interrupted can be lost with it (pi, and
// codex without its own queue), so `tell --interrupt` waits for the turn to
// end and sends the message as the next one.
func TestAnInterruptingTellIsTheNextTurnNotASteer(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, _, agent := f.startedAgent("t", nil)
	f.do(s, fake.Command{Cmd: fake.CmdTurnStart})
	f.await(agents.EventState, toState(state.StateRefining))
	// The interrupt is seen but the turn ends only when the test says, so
	// nothing can be delivered in between by luck.
	f.do(s, fake.Command{Cmd: fake.CmdIgnoreInterrupts, On: true})
	_, err := f.mgr.Tell(f.ctx(), ident.UserPrincipal(), string(agent.Agent), "stop and use v2", true)
	require.NoError(t, err)
	_, err = s.Expect(f.ctx(), fake.RepInterrupt)
	require.NoError(t, err)
	f.do(s, fake.Command{Cmd: fake.CmdTurnEnd, Outcome: string(harness.TurnInterrupted)})
	got := f.user(s)
	assert.Contains(t, got.Text, "stop and use v2")
	assert.False(t, got.MidTurn, "sent once the interrupted turn ended")
}

// Claude Code and pi find a session by its cwd, so an agent that gained its
// first worktree mid-conversation is resumed where it started, with the
// worktree as another directory: started there, it would lose its session.
func TestAResumeRunsWhereTheConversationStarted(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	dirs := make(chan *harness.Workspace, 2)
	f.hooks.set(func(h *harnessHooks) {
		h.start = func(_ context.Context, ws *harness.Workspace, _ *harness.ConversationRef) error {
			dirs <- ws
			return nil
		}
	})
	s, tk, _ := f.startedAgent("t", func(nt *tickets.NewTicket) { nt.Repos = nil })
	first := <-dirs
	repos := []string{"app"}
	_, err := f.tickets.Update(context.Background(), ident.UserPrincipal(), tk.ID, &tickets.TicketChange{Repos: &repos})
	require.NoError(t, err)
	f.user(s)
	wts := f.agentFor(tk.ID).Worktrees
	require.Len(t, wts, 1)

	f.do(s, fake.Command{Cmd: fake.CmdExit, Code: 3})
	resumed := f.session()
	require.True(t, resumed.Resumed)
	again := <-dirs
	assert.Equal(t, first.Dir, again.Dir, "resumed in the directory it started in")
	assert.Contains(t, again.ExtraDirs, wts[0].Path, "and its new worktree is still in reach")
}

func TestTheLoopGuardTellsTheUserOnceAndLimitsMessagesToThem(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	_, _, agent := f.startedAgent("t", nil)
	ctx := f.ctx()
	for i := range 30 { // sendLimit
		_, err := f.mgr.Tell(ctx, agent, "user", "fyi", false)
		require.NoError(t, err, "message %d", i)
	}
	for range 3 {
		_, err := f.mgr.Tell(ctx, agent, "user", "fyi", false)
		require.ErrorIs(t, err, agents.ErrLoopGuard)
	}
	evs, err := f.state.Events.ReadAfter(context.Background(), 0, 500)
	require.NoError(t, err)
	told := 0
	for _, e := range evs {
		if e.Type == agents.EventUserTold && strings.Contains(string(e.Payload), "loop guard") {
			told++
		}
	}
	assert.Equal(t, 1, told, "once per window, not on every refused retry")
}
