package agents_test

import (
	"context"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

const holder = "pid 1 on /dev/pts/9"

// working starts a ticket's agent and has it in a turn.
func (f *fixture) working(title string) (*fake.Session, *state.Agent) {
	f.t.Helper()
	tk := f.file(title, nil)
	s := f.session()
	f.user(s)
	f.do(s, fake.Command{Cmd: fake.CmdTurnStart})
	f.await(agents.EventState, toState(state.StateRefining))
	return s, f.agentFor(tk.ID)
}

// attached attaches the agent working s with --now, as a person would.
func (f *fixture) attached(s *fake.Session, a *state.Agent, req *agents.AttachRequest) *agents.Attachment {
	f.t.Helper()
	req.Target, req.Now = string(a.ID), true
	if req.Holder == "" {
		req.Holder = holder
	}
	got := make(chan error, 1)
	var att *agents.Attachment
	go func() {
		var err error
		att, err = f.mgr.Attach(f.ctx(), req)
		got <- err
	}()
	_, err := s.Expect(f.ctx(), fake.RepInterrupt)
	require.NoError(f.t, err)
	_, err = s.Expect(f.ctx(), fake.RepStdinClosed)
	require.NoError(f.t, err)
	require.NoError(f.t, <-got)
	return att
}

func env(spec []string, key string) (string, bool) {
	for _, kv := range spec {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

func TestAttachWaitsForTheTurnThenHandsTheConversationOver(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, a := f.working("t")
	got := make(chan *agents.Attachment, 1)
	go func() {
		att, err := f.mgr.Attach(f.ctx(), &agents.AttachRequest{Target: string(a.Ticket), Holder: holder})
		assert.NoError(t, err)
		got <- att
	}()
	f.await(agents.EventAttaching, nil)
	// It waits for the turn: the conversation is stopped only once it ends.
	f.do(s, fake.Command{Cmd: fake.CmdTurnEnd})
	_, err := s.Expect(f.ctx(), fake.RepStdinClosed)
	require.NoError(t, err)
	att := <-got
	require.NotNil(t, att)
	f.await(agents.EventState, toState(state.StateAttached))

	assert.Equal(t, []string{"--tui", "--resume", s.ID}, att.Command.Args)
	assert.Equal(t, a.Worktrees[0].Path, att.Command.Dir, "the worktree")
	token, ok := env(att.Command.Env, control.TokenEnv)
	require.True(t, ok)
	who, err := f.state.Agents.AgentForTokenHash(f.ctx(), control.HashToken(token))
	require.NoError(t, err, "a fresh token that identifies the agent")
	assert.Equal(t, a.ID, who.Agent)
	assert.Equal(t, state.StateRefining, att.Agent.State, "the state before the attach")

	attachedNow, err := f.state.Agents.Get(f.ctx(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, state.ModeAttached, attachedNow.Mode)
	require.NotNil(t, attachedNow.Lease)
	assert.Equal(t, holder, attachedNow.Lease.Holder)
}

func TestAMessageToAnAttachedAgentIsHeldForTheHandBack(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, a := f.working("t")
	f.attached(s, a, &agents.AttachRequest{})
	_, err := f.mgr.Tell(f.ctx(), ident.UserPrincipal(), string(a.ID), "the API moved to v2", true)
	require.NoError(t, err)
	pending, err := f.state.Messages.Pending(f.ctx(), state.AgentParty(a.ID))
	require.NoError(t, err)
	require.Len(t, pending, 1, "held, not delivered")

	out, err := f.mgr.HandBack(f.ctx(), a.ID, holder, "the person quit the TUI")
	require.NoError(t, err)
	assert.Equal(t, agents.Resumed, out)
	resumed := f.session()
	assert.True(t, resumed.Resumed)
	assert.Equal(t, s.ID, resumed.ID, "the same conversation")
	note := f.user(resumed)
	assert.Contains(t, note.Text, "the API moved to v2")
	assert.Contains(t, note.Text, "A person worked with you directly")
	f.await(agents.EventHandedBack, nil)
	back, err := f.state.Agents.Get(f.ctx(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, state.ModeProgrammatic, back.Mode)
	assert.Nil(t, back.Lease)
}

func TestASecondAttachIsRefusedNamingTheHolder(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, a := f.working("t")
	f.attached(s, a, &agents.AttachRequest{})
	_, err := f.mgr.Attach(f.ctx(), &agents.AttachRequest{Target: string(a.ID), Holder: "someone else"})
	var held *state.LeaseHeldError
	require.ErrorAs(t, err, &held)
	assert.Equal(t, holder, held.Holder)
	assert.Contains(t, err.Error(), holder)
	// Nor may the same CLI attach twice: reconnecting is Reattach.
	_, err = f.mgr.Attach(f.ctx(), &agents.AttachRequest{Target: string(a.ID), Holder: holder})
	assert.ErrorAs(t, err, &held)
}

func TestAnAttachedAgentKeepsItsSlot(t *testing.T) {
	t.Parallel()
	f := newFixture(t, oneSlot)
	s, a := f.working("first")
	f.attached(s, a, &agents.AttachRequest{})
	second := f.file("second", nil)
	// An urgent ticket is a barrier: the scheduler takes eligible tickets
	// oldest first, so by the time the urgent one has its agent, the second
	// was passed over for want of the slot the attached agent holds.
	urgent := f.file("urgent", func(nt *tickets.NewTicket) { nt.Urgent = true })
	f.await(agents.EventSpawned, func(e *state.Event) bool { return e.Ticket == urgent.ID })
	_, err := f.state.Agents.LiveForTicket(f.ctx(), second.ID)
	require.ErrorIs(t, err, state.ErrNotFound)
	f.user(f.session()) // the urgent agent's kickoff

	// Handed back, it goes straight into its turn on the slot it kept.
	_, err = f.mgr.HandBack(f.ctx(), a.ID, holder, "the person quit the TUI")
	require.NoError(t, err)
	resumed := f.session()
	f.user(resumed)
	f.do(resumed, fake.Command{Cmd: fake.CmdTurnStart})
	_, err = f.mgr.Ask(f.ctx(), ident.AgentPrincipal(a.ID, a.Ticket), "?", nil, "")
	require.NoError(t, err)
	f.do(resumed, fake.Command{Cmd: fake.CmdTurnEnd})
	f.await(agents.EventSpawned, func(e *state.Event) bool { return e.Ticket == second.ID })
}

func TestDoneFromTheTUIFinishesTheTicketInsteadOfResuming(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, a := f.working("t")
	f.attached(s, a, &agents.AttachRequest{})
	require.NoError(t, f.mgr.Done(f.ctx(), ident.AgentPrincipal(a.ID, a.Ticket), "Finished by hand."))
	out, err := f.mgr.HandBack(f.ctx(), a.ID, holder, "the person quit the TUI")
	require.NoError(t, err)
	assert.Equal(t, agents.Finished, out)
	f.await(agents.EventFinished, nil)
	tk, err := f.tickets.Get(f.ctx(), a.Ticket)
	require.NoError(t, err)
	assert.Equal(t, tickets.StatusDone, tk.Status)
	ended, err := f.state.Agents.Get(f.ctx(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, state.StateDone, ended.State)
}

func TestNoResumeReleasesTheAgentAndItsTicketIsNotRespawned(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, a := f.working("mine now")
	att := f.attached(s, a, &agents.AttachRequest{NoResume: true})
	assert.True(t, att.Released)
	assert.Equal(t, []string{"--tui", "--resume", s.ID}, att.Command.Args)
	released, err := f.state.Agents.Get(f.ctx(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, state.StateReleased, released.State)
	assert.Nil(t, released.Lease)
	tk, err := f.tickets.Get(f.ctx(), a.Ticket)
	require.NoError(t, err)
	assert.Equal(t, tickets.UserID("ada"), tk.AssigneeID, "the ticket stays assigned")

	// The scheduler takes eligible tickets oldest first, so once the newer
	// ticket has its agent, the released one was passed over.
	later := f.file("later", nil)
	f.await(agents.EventSpawned, func(e *state.Event) bool { return e.Ticket == later.ID })
	evs, err := f.state.Events.ReadAfter(f.ctx(), 0, 500)
	require.NoError(t, err)
	spawns := slices.DeleteFunc(evs, func(e state.Event) bool { return e.Type != agents.EventSpawned || e.Ticket != a.Ticket })
	assert.Len(t, spawns, 1, "only the agent that was released")
}

func TestAnExpiredLeaseIsHandedBackByTheClock(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "agents: {attach_lease: 30s}\n")
	s, a := f.working("t")
	f.attached(s, a, &agents.AttachRequest{})
	f.clock.Advance(31 * time.Second)
	ev := f.await(agents.EventHandedBack, nil)
	assert.Contains(t, string(ev.Payload), "lease expired")
	resumed := f.session()
	assert.Equal(t, s.ID, resumed.ID)
	f.user(resumed)
	// The expired holder's heartbeat and hand-back learn it's over.
	assert.ErrorIs(t, f.mgr.Heartbeat(f.ctx(), a.ID, holder), agents.ErrNotAttached)
	_, err := f.mgr.HandBack(f.ctx(), a.ID, holder, "late")
	assert.ErrorIs(t, err, agents.ErrNotAttached)
}

func TestAHeartbeatKeepsTheLease(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "agents: {attach_lease: 30s}\n")
	s, a := f.working("t")
	f.attached(s, a, &agents.AttachRequest{})
	f.clock.Advance(20 * time.Second)
	require.NoError(t, f.mgr.Heartbeat(f.ctx(), a.ID, holder))
	f.clock.Advance(20 * time.Second) // 40s after the attach, 20s after the heartbeat
	// The sweeper has run on that tick; a sweep still finds nothing expired.
	expired, err := f.state.Agents.ExpiredLeases(f.ctx(), f.clock.Now())
	require.NoError(t, err)
	assert.Empty(t, expired)
	still, err := f.state.Agents.Get(f.ctx(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, state.StateAttached, still.State)
}

func TestAnAttachSurvivesARestartAndItsCLIReconnects(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, a := f.working("t")
	f.attached(s, a, &agents.AttachRequest{})
	require.NoError(t, f.mgr.Stop(context.Background()))

	self, err := os.Executable()
	require.NoError(t, err)
	m := f.newManager(&fakeHarness{&fake.Harness{Binary: self, Control: f.ctl.Socket(), Logger: slog.New(slog.DiscardHandler), Clock: clock.Real{}}})
	require.NoError(t, m.Start(context.Background()))
	t.Cleanup(func() { _ = m.Stop(context.Background()) })
	t.Cleanup(func() { _ = f.ctl.Close() })

	// Not resumed: the person still has it. Its verbs still work.
	_, err = m.Reattach(f.ctx(), string(a.ID), "another CLI")
	assert.ErrorIs(t, err, agents.ErrNotAttached)
	_, err = m.Reattach(f.ctx(), string(a.ID), holder)
	require.NoError(t, err)
	require.NoError(t, m.SetNote(f.ctx(), ident.AgentPrincipal(a.ID, a.Ticket), "fixed by hand"))

	out, err := m.HandBack(f.ctx(), a.ID, holder, "the person quit the TUI")
	require.NoError(t, err)
	assert.Equal(t, agents.Resumed, out)
	resumed := f.session()
	assert.True(t, resumed.Resumed)
	assert.Equal(t, s.ID, resumed.ID)
	assert.Contains(t, f.user(resumed).Text, "A person worked with you directly")
}
