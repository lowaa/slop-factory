package hookserver_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/hookserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/session"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

type identity struct{ id uuid.UUID }

func (i identity) AgentID() uuid.UUID  { return i.id }
func (i identity) AgentName() string   { return "builder-1" }
func (i identity) TenantID() uuid.UUID { return testTenantID }
func (i identity) ReadOnly() bool      { return false }

// testTenantID is fixed: these tests exercise the hook decision logic
// against fakes with no tenant concept of their own, not cross-tenant
// isolation.
var testTenantID = uuid.New()

type fakeTickets struct {
	leaseCalls int
	lease      *ticket.Lease
	leaseErr   error
	ticket     *ticket.Ticket
}

func (f *fakeTickets) LeaseFor(context.Context, uuid.UUID) (*ticket.Lease, error) {
	f.leaseCalls++
	if f.leaseErr != nil {
		return nil, f.leaseErr
	}
	if f.lease == nil {
		return nil, ticket.ErrNoLease
	}
	return f.lease, nil
}

func (f *fakeTickets) Get(context.Context, int64) (*ticket.Ticket, error) {
	if f.ticket == nil {
		return nil, ticket.ErrNotFound
	}
	return f.ticket, nil
}

type fakeSessions struct {
	blockCount     int
	tripped        bool
	blocksCalled   int
	touched        int
	resets         int
	touchErr       error
	resetErr       error
	startResult    *session.StartResult
	superseded     bool
	unknownSession bool
	startCalls     int
	// started records the run and working directory each Start named.
	started [][2]string
	// described records each Describe, keyed by the run it named.
	described   map[string]*session.Description
	describeErr error
	// stuck records each reason the loop breaker marked a run stuck with,
	// and stuckRuns the agent and run each named.
	stuck     []string
	stuckRuns []string
}

func (f *fakeSessions) Describe(_ context.Context, _ uuid.UUID, claudeSessionID string, d *session.Description) error {
	if f.describeErr != nil {
		return f.describeErr
	}
	if f.described == nil {
		f.described = map[string]*session.Description{}
	}
	f.described[claudeSessionID] = d
	return nil
}

func (f *fakeSessions) MarkStuck(_ context.Context, agentID uuid.UUID, claudeSessionID, reason string) error {
	f.stuck = append(f.stuck, reason)
	f.stuckRuns = append(f.stuckRuns, agentID.String()+"/"+claudeSessionID)
	return nil
}

func (f *fakeSessions) Start(_ context.Context, _ uuid.UUID, claudeSessionID, cwd string) (*session.StartResult, error) {
	f.startCalls++
	f.started = append(f.started, [2]string{claudeSessionID, cwd})
	if f.startResult == nil {
		return &session.StartResult{Arrival: session.ArrivalNew, Session: &session.Session{}}, nil
	}
	return f.startResult, nil
}

func (f *fakeSessions) ResetBlocks(context.Context, uuid.UUID, string) error {
	f.resets++
	return f.resetErr
}

func (f *fakeSessions) RecordBlock(context.Context, uuid.UUID, string) (int, bool, error) {
	f.blocksCalled++
	return f.blockCount, f.tripped, nil
}

// superseded makes TouchSession report that the calling run is no longer
// the agent's current session.
func (f *fakeSessions) TouchSession(context.Context, uuid.UUID, string) (session.State, error) {
	f.touched++
	switch {
	case f.touchErr != nil:
		return session.StateUnknown, f.touchErr
	case f.superseded:
		return session.StateSuperseded, nil
	case f.unknownSession:
		return session.StateUnknown, nil
	}
	return session.StateLive, nil
}

type harness struct {
	handler  *hookserver.Handler
	tickets  *fakeTickets
	sessions *fakeSessions
	ctx      context.Context
	agentID  uuid.UUID
}

func newHarness() *harness { return newHarnessWith(nil) }

// newHarnessWith is newHarness with a say in the handler's dependencies.
func newHarnessWith(configure func(*hookserver.Deps)) *harness {
	t := &fakeTickets{}
	s := &fakeSessions{}
	agentID := uuid.New()
	deps := hookserver.Deps{Tickets: t, Sessions: s}
	if configure != nil {
		configure(&deps)
	}
	return &harness{
		handler:  hookserver.NewHandler(deps, slog.DiscardHandler),
		tickets:  t,
		sessions: s,
		ctx:      authn.NewContext(context.Background(), identity{id: agentID}),
		agentID:  agentID,
	}
}

// The subagent guard, first in the decision table. The Stop hook also fires
// for subagents, and without this an Explore subagent finishing a search
// gets told to go claim tickets.
func TestStop_SubagentAlwaysStops(t *testing.T) {
	h := newHarness()
	h.tickets.lease = &ticket.Lease{TicketID: 42}

	decision, err := h.handler.Stop(h.ctx, &hookserver.StopInput{
		SessionID: "s1", AgentID: "subagent-uuid", AgentType: "Explore",
	})
	require.NoError(t, err)
	assert.Empty(t, decision.Decision, "a subagent must be allowed to stop")
	assert.Zero(t, h.sessions.blocksCalled, "a subagent stop is not a block")
}

func TestStop_HeldTicketBlocks(t *testing.T) {
	h := newHarness()
	h.tickets.lease = &ticket.Lease{TicketID: 42}
	h.tickets.ticket = &ticket.Ticket{ID: 42, Status: ticket.StatusInProgress}

	decision, err := h.handler.Stop(h.ctx, &hookserver.StopInput{SessionID: "s1"})
	require.NoError(t, err)
	assert.Equal(t, "block", decision.Decision)
	assert.True(t, decision.Continue)
	assert.Contains(t, decision.Reason, "#42")
	assert.Contains(t, decision.Reason, "ticket_release")
}

// Nothing sends an agent to the queue or to wait for work: a supervisor
// starts a run per ticket, and a console or Remote Control session has no
// queue to go back to.
func TestStop_IdleAgentStops(t *testing.T) {
	h := newHarness()

	decision, err := h.handler.Stop(h.ctx, &hookserver.StopInput{SessionID: "s1"})
	require.NoError(t, err)
	assert.Empty(t, decision.Decision)
	assert.Zero(t, h.sessions.blocksCalled, "and it is not recorded as a stop-block")
}

// The loop breaker. After MaxStopBlocks with no progress the server stops
// blocking, marks the session stuck, and leaves the lease alone -- releasing it
// would be exactly the ticket/lease mutation the hook contract forbids.
func TestStop_LoopBreakerReleasesTheAgentAndMarksItStuck(t *testing.T) {
	h := newHarness()
	h.tickets.lease = &ticket.Lease{TicketID: 42}
	h.tickets.ticket = &ticket.Ticket{ID: 42, Status: ticket.StatusInProgress}
	h.sessions.blockCount = session.MaxStopBlocks
	h.sessions.tripped = true

	decision, err := h.handler.Stop(h.ctx, &hookserver.StopInput{SessionID: "s1"})
	require.NoError(t, err)
	assert.Empty(t, decision.Decision, "a stuck agent must be allowed to stop")
	require.Len(t, h.sessions.stuck, 1, "a stuck agent must be visible to the human")
	assert.Equal(t, []string{h.agentID.String() + "/s1"}, h.sessions.stuckRuns, "marked on the calling agent's own run")
	assert.Contains(t, h.sessions.stuck[0], "Stuck on #42")
	assertNamesTheRealRecoveryPaths(t, h.sessions.stuck[0], "moving #42 out of in_progress on its page")
}

// The Agents screen has no per-lease release, so the notice must not send
// the human looking for one.
func assertNamesTheRealRecoveryPaths(t *testing.T, notice, moveTicket string) {
	t.Helper()
	assert.Contains(t, notice, moveTicket)
	assert.Contains(t, notice, "or disable the agent")
	assert.NotContains(t, notice, "Agents screen")
}

// An unreachable database looks exactly like "no lease". Treating it that
// way would let an agent holding a ticket stop without finishing or
// releasing it, which is the most expensive state to lose -- the work in
// progress goes with the session.
func TestStop_LeaseLookupFailureFailsClosed(t *testing.T) {
	h := newHarness()
	h.tickets.leaseErr = errors.New("database is down")

	decision, err := h.handler.Stop(h.ctx, &hookserver.StopInput{SessionID: "s1"})
	require.NoError(t, err)
	assert.Equal(t, "block", decision.Decision, "a failed read must not release the agent")
	assert.Contains(t, decision.Reason, "Cannot reach slop-haus")
}

// Failing closed must not become a spin: the block counts against the loop
// breaker like any other, so an outage costs a few retries and a visible
// notice rather than an agent hammering a broken server forever.
func TestStop_LeaseLookupFailureIsStillBoundedByTheLoopBreaker(t *testing.T) {
	h := newHarness()
	h.tickets.leaseErr = errors.New("database is down")
	h.sessions.blockCount = session.MaxStopBlocks
	h.sessions.tripped = true

	decision, err := h.handler.Stop(h.ctx, &hookserver.StopInput{SessionID: "s1"})
	require.NoError(t, err)
	assert.Empty(t, decision.Decision, "the loop breaker still releases the agent")
	require.Len(t, h.sessions.stuck, 1, "and says so")
	assert.Contains(t, h.sessions.stuck[0], "Appears stuck:", "no ticket could be read, so none is named")
	assertNamesTheRealRecoveryPaths(t, h.sessions.stuck[0], "move that ticket out of in_progress on its page")
}

func TestStop_RequiresAnAuthenticatedAgent(t *testing.T) {
	h := newHarness()

	_, err := h.handler.Stop(context.Background(), &hookserver.StopInput{SessionID: "s1"})
	require.Error(t, err, "an unauthenticated hook call is a wiring bug, not an anonymous stop")
}

func TestSessionStart_BriefsTheAgentAndBindsTheSession(t *testing.T) {
	h := newHarness()
	h.tickets.lease = &ticket.Lease{TicketID: 42}

	out, err := h.handler.SessionStart(h.ctx, &hookserver.SessionStartInput{
		SessionID: "s1", CWD: "/srv/ws/1",
	})
	require.NoError(t, err)
	assert.Contains(t, out.AdditionalContext, "builder-1")
	assert.Contains(t, out.AdditionalContext, "#42",
		"a restarted run still holds its ticket and should be told so")
	assert.NotContains(t, out.AdditionalContext, "integration_lock",
		"the working agreement reaches the agent as a file; a second copy here would drift from it")
}

func TestSessionStart_WarnsAboutASecondLiveRun(t *testing.T) {
	h := newHarness()
	h.sessions.startResult = &session.StartResult{
		Arrival: session.ArrivalNew, Session: &session.Session{}, ConcurrentRun: true,
	}

	out, err := h.handler.SessionStart(h.ctx, &hookserver.SessionStartInput{SessionID: "s2"})
	require.NoError(t, err)
	assert.Contains(t, out.SystemMessage, "another agent run")
}

// A /clear, or pi swapping in a fresh session for the next ticket, replaces
// the last session in the same terminal: it is not a second run.
func TestSessionStart_ASessionReplacedInProcessIsNotASecondRun(t *testing.T) {
	for _, source := range []string{"clear", "new"} {
		t.Run(source, func(t *testing.T) {
			h := newHarness()
			h.sessions.startResult = &session.StartResult{
				Arrival: session.ArrivalNew, Session: &session.Session{}, ConcurrentRun: true,
			}

			out, err := h.handler.SessionStart(h.ctx, &hookserver.SessionStartInput{SessionID: "s2", Source: source})
			require.NoError(t, err)
			assert.Empty(t, out.SystemMessage)
		})
	}
}

// A superseded run should stop, not be told to keep working -- and its
// callbacks must not count against the session that replaced it.
func TestStop_SupersededSessionIsAllowedToStop(t *testing.T) {
	h := newHarness()
	h.sessions.superseded = true
	h.tickets.lease = &ticket.Lease{TicketID: 42}
	h.tickets.ticket = &ticket.Ticket{ID: 42, Status: ticket.StatusInProgress}

	decision, err := h.handler.Stop(h.ctx, &hookserver.StopInput{SessionID: "claude-old"})
	require.NoError(t, err)
	assert.Empty(t, decision.Decision, "an old run must not be kept alive by the new run's ticket")
	assert.Zero(t, h.sessions.blocksCalled, "and must not count against the live session")
}

// A run whose SessionStart never landed must be bound and then decided on
// normally -- not dismissed as superseded, which would leave a live agent
// with no loop and no breaker.
func TestStop_UnregisteredSessionIsBoundRatherThanDismissed(t *testing.T) {
	h := newHarness()
	h.sessions.unknownSession = true
	h.tickets.lease = &ticket.Lease{TicketID: 42}
	h.tickets.ticket = &ticket.Ticket{ID: 42, Status: ticket.StatusInProgress}

	decision, err := h.handler.Stop(h.ctx, &hookserver.StopInput{
		SessionID: "never-registered", CWD: "/srv/ws/1",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, h.sessions.startCalls, "the session should have been bound")
	assert.Equal(t, "block", decision.Decision,
		"and then decided on normally -- it is holding a ticket")
	assert.Contains(t, decision.Reason, "#42")
}

// A lease on a done ticket is a shadow's: its agent is told so, rather than
// left to read "done" as leave to stop.
func TestStop_AHeldShadowOfADoneTicketIsToldToCarryOn(t *testing.T) {
	h := newHarness()
	h.tickets.lease = &ticket.Lease{TicketID: 42}
	h.tickets.ticket = &ticket.Ticket{ID: 42, Status: ticket.StatusDone}

	decision, err := h.handler.Stop(h.ctx, &hookserver.StopInput{SessionID: "s1"})
	require.NoError(t, err)
	assert.Equal(t, "block", decision.Decision)
	assert.Contains(t, decision.Reason, ticket.DoneThroughOtherNote)
}

func TestStop_AHeldParkedReplayIsToldToFinishIt(t *testing.T) {
	h := newHarness()
	h.tickets.lease = &ticket.Lease{TicketID: 42}
	h.tickets.ticket = &ticket.Ticket{ID: 42, Status: ticket.StatusParked}

	decision, err := h.handler.Stop(h.ctx, &hookserver.StopInput{SessionID: "s1"})
	require.NoError(t, err)
	assert.Equal(t, "block", decision.Decision)
	assert.Contains(t, decision.Reason, "parked")
	assert.Contains(t, decision.Reason, "replay")
}
