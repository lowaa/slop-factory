package mcpserver_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func (r *realRig) currentDeclaration(agentID uuid.UUID) (found bool, ticketID *int64) {
	r.t.Helper()
	err := r.database.QueryRow(r.ctx, `SELECT true, ticket_id FROM usage_declarations WHERE agent_id = $1 ORDER BY id DESC LIMIT 1`,
		agentID).Scan(&found, &ticketID)
	if err != nil {
		return false, nil
	}
	return found, ticketID
}

func (r *realRig) declaredAttempt(agentID uuid.UUID) *int {
	r.t.Helper()
	var attempt *int
	require.NoError(r.t, r.database.QueryRow(r.ctx,
		`SELECT attempt_number FROM usage_declarations WHERE agent_id = $1 ORDER BY id DESC LIMIT 1`, agentID).Scan(&attempt))
	return attempt
}

func TestWorkingOn_RecordsTheAttempt(t *testing.T) {
	r := newRealRig(t)
	c := r.agentSession("console", true)
	single := r.forceStatus(ticket.StatusOpen)
	two := r.forceStatus(ticket.StatusOpen)
	r.withSiblingAttempt(two.ID)

	res := c.call(t, "working_on", map[string]any{"ticket_id": single.ID})
	require.False(t, res.IsError, resultText(t, res))
	one := 1
	assert.Equal(t, &one, r.declaredAttempt(c.agentID), "the only attempt, unnamed")

	res = c.call(t, "working_on", map[string]any{"ticket_id": two.ID, "attempt": 2})
	require.False(t, res.IsError, resultText(t, res))
	two2 := 2
	assert.Equal(t, &two2, r.declaredAttempt(c.agentID))

	res = c.call(t, "working_on", map[string]any{"ticket_id": nil})
	require.False(t, res.IsError, resultText(t, res))
	assert.Nil(t, r.declaredAttempt(c.agentID))
}

func TestWorkingOn_AConsoleDeclaresAndClears(t *testing.T) {
	r := newRealRig(t)
	tk := r.forceStatus(ticket.StatusOpen)
	c := r.agentSession("console", true)

	res := c.call(t, "working_on", map[string]any{"ticket_id": tk.ID})
	require.False(t, res.IsError, resultText(t, res))
	assert.Contains(t, resultText(t, res), "goes to #")
	_, declared := r.currentDeclaration(c.agentID)
	require.NotNil(t, declared)
	assert.Equal(t, tk.ID, *declared)
	got, err := r.tickets.Get(r.ctx, tk.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusOpen, got.Status, "declaring does not claim or change the ticket")

	res = c.call(t, "working_on", map[string]any{"ticket_id": nil})
	require.False(t, res.IsError, resultText(t, res))
	found, declared := r.currentDeclaration(c.agentID)
	assert.True(t, found)
	assert.Nil(t, declared, "null is a declaration of none")
}

func TestWorkingOn_ATicketThatDoesNotExistIsRefused(t *testing.T) {
	r := newRealRig(t)
	c := r.agentSession("console", true)

	res := c.call(t, "working_on", map[string]any{"ticket_id": 999_999})

	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "no such ticket")
	found, _ := r.currentDeclaration(c.agentID)
	assert.False(t, found)
}

func TestWorkingOn_AConductorWithoutALiveConversationIsToldWhy(t *testing.T) {
	r := newRealRig(t)
	tk := r.forceStatus(ticket.StatusOpen)
	conductor := r.conductorSession("conductor")

	res := conductor.call(t, "working_on", map[string]any{"ticket_id": tk.ID})

	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "live conversation")
}
