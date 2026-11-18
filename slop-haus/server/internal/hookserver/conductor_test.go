package hookserver_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/hookserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// conductorIdentity is an identity that says it is a conductor, as
// *agent.Agent does.
type conductorIdentity struct{ identity }

func (conductorIdentity) IsConductor() bool { return true }

func (h *harness) asConductor() {
	h.ctx = authn.NewContext(context.Background(), conductorIdentity{identity{id: uuid.New()}})
}

// A conductor's host runs the session and reports it itself: the worker hooks
// must not bind it, brief it, or send it to claim work.
func TestStop_AConductorIsNeverKeptFromStopping(t *testing.T) {
	h := newHarness()
	h.tickets.lease = &ticket.Lease{TicketID: 1}
	worker, err := h.handler.Stop(h.ctx, &hookserver.StopInput{SessionID: "s1"})
	require.NoError(t, err)
	require.Equal(t, "block", worker.Decision, "a worker holding a ticket is kept on it")

	h = newHarness()
	h.tickets.lease = &ticket.Lease{TicketID: 1}
	h.asConductor()
	decision, err := h.handler.Stop(h.ctx, &hookserver.StopInput{SessionID: "s1"})

	require.NoError(t, err)
	assert.Empty(t, decision.Decision)
	assert.Zero(t, h.sessions.touched, "and its session is not bookkept as a worker's")
}

func TestSessionStart_AConductorsSessionIsNotBoundOrBriefed(t *testing.T) {
	h := newHarness()
	h.asConductor()

	out, err := h.handler.SessionStart(h.ctx, &hookserver.SessionStartInput{SessionID: "s1", CWD: "/srv/ws"})

	require.NoError(t, err)
	assert.Empty(t, out.AdditionalContext)
	assert.Zero(t, h.sessions.startCalls)
}

// consoleIdentity says it is a console, as *agent.Agent does.
type consoleIdentity struct{ identity }

func (consoleIdentity) IsConsole() bool { return true }

func (h *harness) asConsole() {
	h.ctx = authn.NewContext(context.Background(), consoleIdentity{identity{id: uuid.New()}})
}

// A console takes the ticket it is told to and nothing from the queue, so
// stopping with nothing held is never met with an instruction to claim or wait.
func TestStop_AConsoleHoldingNothingIsNeitherSentToClaimNorToWait(t *testing.T) {
	h := newHarness()
	h.asConsole()

	decision, err := h.handler.Stop(h.ctx, &hookserver.StopInput{SessionID: "s1"})

	require.NoError(t, err)
	assert.Empty(t, decision.Decision)
}

func TestStop_AConsoleHoldingATicketIsNudgedAsAnyAgentIs(t *testing.T) {
	h := newHarness()
	h.tickets.lease = &ticket.Lease{TicketID: 42}
	h.tickets.ticket = &ticket.Ticket{ID: 42, Status: ticket.StatusInProgress}
	h.asConsole()

	decision, err := h.handler.Stop(h.ctx, &hookserver.StopInput{SessionID: "s1"})

	require.NoError(t, err)
	assert.Equal(t, "block", decision.Decision)
	assert.Contains(t, decision.Reason, "#42")
}
