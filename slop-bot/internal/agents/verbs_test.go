package agents_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

// startedAgent files a ticket and waits for its agent's kick-off. The
// ticket is left preparing, not open: the scheduler walks an open ticket to
// in_progress asynchronously, which would race with a verb under test that
// moves the ticket.
func (f *fixture) startedAgent(title string, mut func(*tickets.NewTicket)) (*fake.Session, *tickets.Ticket, ident.Principal) {
	f.t.Helper()
	tk := f.file(title, func(nt *tickets.NewTicket) {
		if mut != nil {
			mut(nt)
		}
	})
	s := f.session()
	f.user(s)
	a := f.agentFor(tk.ID)
	return s, tk, ident.AgentPrincipal(a.ID, tk.ID)
}

func (f *fixture) ticket(id ident.TicketID) *tickets.Ticket {
	f.t.Helper()
	tk, err := f.tickets.Get(context.Background(), id)
	require.NoError(f.t, err)
	return tk
}

func lastComment(tk *tickets.Ticket) string {
	if len(tk.Comments) == 0 {
		return ""
	}
	return tk.Comments[len(tk.Comments)-1].Body
}

func TestDiscussParksTheTicketAndResolveWakesTheAgent(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, tk, agent := f.startedAgent("t", nil)

	f.turn(s, func() {
		require.NoError(t, f.mgr.Discuss(f.ctx(), agent, "Drop the column?", []string{"drop", "keep"}, "keep"))
	})
	f.await(agents.EventState, toState(state.StateDiscussion))
	got := f.ticket(tk.ID)
	assert.Equal(t, tickets.StatusDiscussion, got.Status)
	body := lastComment(got)
	assert.Contains(t, body, "Drop the column?")
	assert.Contains(t, body, "1. drop")
	assert.Contains(t, body, "2. keep")
	assert.Contains(t, body, "Recommendation: keep")

	err := f.mgr.ResolveDiscussion(f.ctx(), agent, tk.ID, "keep it")
	assert.ErrorIs(t, err, agents.ErrUserOnly, "an agent can't resolve its own discussion")

	require.NoError(t, f.mgr.ResolveDiscussion(f.ctx(), ident.UserPrincipal(), tk.ID, "keep it"))
	got = f.ticket(tk.ID)
	// The woken agent's ticket is open, or already walked on to in_progress.
	assert.Contains(t, []tickets.Status{tickets.StatusOpen, tickets.StatusInProgress}, got.Status)
	assert.Contains(t, lastComment(got), "Discussion resolved:** keep it")
	assert.Contains(t, f.user(s).Text, "Your discussion is resolved: keep it")

	err = f.mgr.ResolveDiscussion(f.ctx(), ident.UserPrincipal(), tk.ID, "again")
	assert.ErrorIs(t, err, agents.ErrNotInDiscusson, "it is open now")
}

func TestResolveDiscussionOfAnUnknownTicketFails(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	assert.Error(t, f.mgr.ResolveDiscussion(f.ctx(), ident.UserPrincipal(), "9999", "x"))
}

func TestReadyToMergeCommentsAndTellsTheUser(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, tk, agent := f.startedAgent("t", nil)
	f.turn(s, func() {
		require.NoError(t, f.mgr.ReadyToMerge(f.ctx(), agent, "https://example.test/pr/1"))
	})
	// The user is told as the verb runs, before the turn ends.
	told := f.await(agents.EventUserTold, nil)
	f.await(agents.EventState, toState(state.StateWaitingMerge))
	assert.Equal(t, "Ready to merge. https://example.test/pr/1", lastComment(f.ticket(tk.ID)))
	assert.Contains(t, string(told.Payload), "is ready to merge (https://example.test/pr/1)")
}

func TestReadyToMergeWithoutAPR(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, tk, agent := f.startedAgent("t", nil)
	f.turn(s, func() { require.NoError(t, f.mgr.ReadyToMerge(f.ctx(), agent, "")) })
	f.await(agents.EventState, toState(state.StateWaitingMerge))
	assert.Equal(t, "Ready to merge.", lastComment(f.ticket(tk.ID)))
}

func TestReadyToMergeIsRefusedForAutoMergeTickets(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	_, tk, agent := f.startedAgent("t", func(nt *tickets.NewTicket) { nt.AutoMerge = true })
	err := f.mgr.ReadyToMerge(f.ctx(), agent, "")
	assert.ErrorIs(t, err, agents.ErrHasAutoMerge)
	assert.Empty(t, f.ticket(tk.ID).Comments, "nothing was posted")
}

func TestSetNoteRecordsTheAgentsProgress(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	_, tk, agent := f.startedAgent("t", nil)
	require.NoError(t, f.mgr.SetNote(f.ctx(), agent, "wiring the exporter"))
	assert.Equal(t, "wiring the exporter", f.agentFor(tk.ID).ProgressNote)
}

func TestVerbsActForTheCallersOwnLiveAgentOnly(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	ctx := f.ctx()
	_, err := f.mgr.Ask(ctx, ident.UserPrincipal(), "?", nil, "")
	assert.ErrorIs(t, err, agents.ErrNotYourTicket)
	assert.ErrorIs(t, f.mgr.Done(ctx, ident.Principal{}, "x"), agents.ErrNotYourTicket)
	ghost := ident.AgentPrincipal("tghost-000000", "ghost")
	assert.ErrorIs(t, f.mgr.SetNote(ctx, ghost, "x"), agents.ErrNoLiveAgent)
	assert.ErrorIs(t, f.mgr.Discuss(ctx, ghost, "x", nil, ""), agents.ErrNoLiveAgent)
	assert.ErrorIs(t, f.mgr.ReadyToMerge(ctx, ghost, ""), agents.ErrNoLiveAgent)
}

func TestTellAllReachesEveryOtherLiveAgent(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s1, _, a1 := f.startedAgent("one", nil)
	s2, _, _ := f.startedAgent("two", nil)

	n, err := f.mgr.TellAll(f.ctx(), ident.UserPrincipal(), "main is red")
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Contains(t, f.user(s1).Text, "main is red")
	assert.Contains(t, f.user(s2).Text, "main is red")

	n, err = f.mgr.TellAll(f.ctx(), a1, "heads up")
	require.NoError(t, err)
	assert.Equal(t, 1, n, "an agent's broadcast skips itself")
	assert.Contains(t, f.user(s2).Text, "heads up")
}

func TestTellToTheUserQueuesInTheInbox(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	_, _, agent := f.startedAgent("t", nil)
	msg, err := f.mgr.Tell(f.ctx(), agent, "user", "FYI", false)
	require.NoError(t, err)
	assert.Equal(t, "FYI", msg.Body)
	pending, err := f.state.Messages.Pending(context.Background(), state.UserParty)
	require.NoError(t, err)
	assert.Len(t, pending, 1)
}

func TestTellToAnUnknownTargetFails(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	_, err := f.mgr.Tell(f.ctx(), ident.UserPrincipal(), "nobody", "hi", false)
	assert.ErrorIs(t, err, agents.ErrUnknownTarget)
}

func TestTheLoopGuardStopsAnAgentFloodingAnother(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	_, _, a1 := f.startedAgent("one", nil)
	_, two, _ := f.startedAgent("two", nil)
	ctx := f.ctx()
	// pairLimit is 20 messages in the window.
	for i := 0; i < 20; i++ {
		_, err := f.mgr.Tell(ctx, a1, string(two.ID), "ping", false)
		require.NoError(t, err, "message %d", i)
	}
	_, err := f.mgr.Tell(ctx, a1, string(two.ID), "ping", false)
	require.ErrorIs(t, err, agents.ErrLoopGuard)
	told := f.await(agents.EventUserTold, func(e *state.Event) bool { return strings.Contains(string(e.Payload), "loop guard") })
	assert.Equal(t, a1.Agent, told.Agent, "the user is told which agent was stopped")

	_, err = f.mgr.Tell(ctx, ident.UserPrincipal(), string(two.ID), "carry on", false)
	assert.NoError(t, err, "the guard limits agents, not the user")
}

func TestTellUserFromTheSystem(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	f.mgr.TellUser(f.ctx(), "disk is full")
	told := f.await(agents.EventUserTold, nil)
	assert.Contains(t, string(told.Payload), "disk is full")
}
