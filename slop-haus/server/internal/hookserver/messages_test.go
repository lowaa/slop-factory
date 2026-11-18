package hookserver_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/agentmsg"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/hookserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// tenantAgent is identity in a real tenant: authn.NewContext scopes the
// context to the identity's tenant.
type tenantAgent struct {
	identity
	tenant uuid.UUID
}

func (a tenantAgent) TenantID() uuid.UUID { return a.tenant }

// messageHarness is the hooks against the real messages store -- whether a
// message was delivered is the store's say, not a fake's -- and fakes for
// everything else.
type messageHarness struct {
	*harness
	svc   *agentmsg.Service
	human uuid.UUID
	ctx   context.Context
}

func newMessageHarness(t *testing.T) *messageHarness {
	t.Helper()
	database := dbtest.WithDB(t)
	tenant := dbtest.NewTenant(t, database)
	tctx := db.WithTenant(context.Background(), tenant)
	agents := agent.NewService(agent.NewPostgresStore(database), ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler), nil, slog.DiscardHandler)
	created, err := agents.Create(tctx, "builder-1")
	require.NoError(t, err)

	svc := agentmsg.NewService(agentmsg.NewPostgresStore(database), agentmsg.Config{}, slog.DiscardHandler)
	h := newHarness()
	h.agentID = created.ID
	h.handler = hookserver.NewHandler(hookserver.Deps{
		Tickets: h.tickets, Messages: svc, Sessions: h.sessions,
	}, slog.DiscardHandler)
	return &messageHarness{
		harness: h, svc: svc, human: dbtest.NewUser(t, database),
		ctx: authn.NewContext(tctx, tenantAgent{identity: identity{id: created.ID}, tenant: tenant}),
	}
}

func (m *messageHarness) say(t *testing.T, body string) {
	t.Helper()
	_, err := m.svc.Send(m.ctx, &authn.User{ID: m.human}, m.agentID, body)
	require.NoError(t, err)
}

func (m *messageHarness) pending(t *testing.T) int {
	t.Helper()
	n, err := m.svc.Pending(m.ctx, m.agentID)
	require.NoError(t, err)
	return n
}

func (m *messageHarness) stop(t *testing.T, in *hookserver.StopInput) hookserver.StopDecision {
	t.Helper()
	d, err := m.handler.Stop(m.ctx, in)
	require.NoError(t, err)
	return d
}

const banner = "MESSAGE FROM THE HUMAN:"

// The message is why an idle agent is kept from stopping, and its text is
// in the reason, so delivery costs no further call.
func TestStop_BlocksAnIdleAgentOnAMessageAndCarriesItsText(t *testing.T) {
	m := newMessageHarness(t)
	m.say(t, "stop, wrong approach")

	d := m.stop(t, &hookserver.StopInput{SessionID: "s1"})
	assert.Equal(t, "block", d.Decision)
	assert.Contains(t, d.Reason, banner)
	assert.Contains(t, d.Reason, "stop, wrong approach")
	assert.Zero(t, m.pending(t), "carried by the block, so delivered")

	again := m.stop(t, &hookserver.StopInput{SessionID: "s1"})
	assert.Empty(t, again.Decision, "told once; with nothing else to do it may stop")
}

func TestStop_AMessageRidesAheadOfTheReasonToKeepGoing(t *testing.T) {
	m := newMessageHarness(t)
	m.tickets.lease = &ticket.Lease{TicketID: 42}
	m.tickets.ticket = &ticket.Ticket{ID: 42, Status: ticket.StatusInProgress}
	m.say(t, "hold your merge")

	d := m.stop(t, &hookserver.StopInput{SessionID: "s1"})
	assert.Equal(t, "block", d.Decision)
	assert.Contains(t, d.Reason, "hold your merge")
	assert.Contains(t, d.Reason, "#42")
	assert.Less(t, strings.Index(d.Reason, "hold your merge"), strings.Index(d.Reason, "#42"), "the human first")
	assert.NotContains(t, d.SystemMessage, "hold your merge", "the terminal line does not repeat it")
	assert.Zero(t, m.pending(t), "carried by the block, so delivered")
}

// A subagent stop is not the agent: it must not use up what the agent
// that dispatched it is to be told.
func TestStop_ASubagentStopLeavesTheMessagePending(t *testing.T) {
	m := newMessageHarness(t)
	m.say(t, "also handle X")

	d := m.stop(t, &hookserver.StopInput{SessionID: "s1", AgentID: "sub", AgentType: "Explore"})
	assert.Empty(t, d.Decision)
	assert.Equal(t, 1, m.pending(t))
}

// Past the loop breaker the agent is let stop, and what it was never handed
// is still waiting for its next call.
func TestStop_TheLoopBreakerLetsItStopWithoutLosingTheMessage(t *testing.T) {
	m := newMessageHarness(t)
	m.sessions.tripped, m.sessions.blockCount = true, 3
	m.say(t, "also handle X")

	d := m.stop(t, &hookserver.StopInput{SessionID: "s1"})
	assert.Empty(t, d.Decision)
	assert.Equal(t, 1, m.pending(t))
}

func TestSessionStart_BriefingIncludesPendingMessagesOnce(t *testing.T) {
	m := newMessageHarness(t)
	m.say(t, "start with the flaky test")

	out, err := m.handler.SessionStart(m.ctx, &hookserver.SessionStartInput{SessionID: "s1", CWD: "/srv/ws/1"})
	require.NoError(t, err)
	assert.Contains(t, out.AdditionalContext, banner)
	assert.Contains(t, out.AdditionalContext, "start with the flaky test")
	assert.Zero(t, m.pending(t))

	next, err := m.handler.SessionStart(m.ctx, &hookserver.SessionStartInput{SessionID: "s2", CWD: "/srv/ws/1"})
	require.NoError(t, err)
	assert.NotContains(t, next.AdditionalContext, "start with the flaky test")
}
