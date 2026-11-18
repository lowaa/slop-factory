package mcpserver_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func (r *realRig) parkedTicket(title string) *ticket.Ticket {
	r.t.Helper()
	created, err := r.tickets.Create(r.ctx, &ticket.CreateParams{Title: title, Status: ticket.StatusPreparing})
	require.NoError(r.t, err)
	parked := ticket.StatusParked
	moved, err := r.tickets.Update(r.ctx, created.ID, &ticket.UpdateParams{Status: &parked})
	require.NoError(r.t, err)
	return moved
}

// A parked ticket, seen from an agent's side: on record and readable, never
// handed out, never filed.
func TestParked_ThroughTheAgentTools(t *testing.T) {
	r := newRealRig(t)
	idea := r.parkedTicket("Someday")

	next := r.call("ticket_next", map[string]any{})
	require.False(t, next.IsError, resultText(t, next))
	assert.Contains(t, resultText(t, next), "The queue is empty.")
	directed := r.call("ticket_next", map[string]any{"ticket_id": idea.ID})
	assert.NotContains(t, resultText(t, directed), "Granted", "not even when asked for by id")
	_, err := r.tickets.LeaseFor(r.ctx, r.agentID)
	assert.ErrorIs(t, err, ticket.ErrNoLease, "the agent holds nothing")
	stillParked, err := r.tickets.Get(r.ctx, idea.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusParked, stillParked.Status)

	read := r.call("ticket_get", map[string]any{"ticket_id": idea.ID})
	require.False(t, read.IsError, resultText(t, read))
	assert.Contains(t, resultText(t, read), fmt.Sprintf("#%d Someday [parked,", idea.ID))
	assert.Contains(t, resultText(t, read), "not work to take")
	assert.Equal(t, "parked", structured(t, read)["status"])

	filed := r.call("ticket_create", map[string]any{"title": "Straight to the shelf", "status": "parked"})
	assert.True(t, filed.IsError, "an agent cannot file a ticket parked: %s", resultText(t, filed))
	assert.Contains(t, resultText(t, filed), "a new ticket is preparing or open")
	all, err := r.tickets.List(r.ctx, nil)
	require.NoError(t, err)
	assert.Len(t, all, 1, "nothing was filed")

	waiting := r.call("ticket_create", map[string]any{"title": "Needs the idea", "status": "open", "blocked_on": []int64{idea.ID}})
	require.False(t, waiting.IsError, resultText(t, waiting))
	assert.Contains(t, resultText(t, waiting), fmt.Sprintf("(blocked), blocked by: #%d Someday (parked)", idea.ID),
		"it says why the new ticket is stuck")
}

// Only ticket_create takes a status, and the service refuses parked there.
// Every other move an agent makes is to a status the tool decides, none of
// them parked -- so a new tool that took a status would be a new way in,
// and should be a deliberate act. `statuses` is held to the same rule, so
// the plural is not a way round it.
func TestNoAgentToolTakesAStatusButTicketCreate(t *testing.T) {
	r := newRig(t)

	// ticket_search's `statuses` is a read filter: it selects rows and
	// moves nothing.
	readFilters := map[string]bool{"ticket_search": true}

	tools, err := r.session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	var withStatus, withStatuses []string
	for _, tool := range tools.Tools {
		schema, ok := tool.InputSchema.(map[string]any)
		require.True(t, ok)
		props, _ := schema["properties"].(map[string]any)
		if _, ok := props["status"]; ok {
			withStatus = append(withStatus, tool.Name)
		}
		if _, ok := props["statuses"]; ok && !readFilters[tool.Name] {
			withStatuses = append(withStatuses, tool.Name)
		}
	}
	assert.Equal(t, []string{"ticket_create"}, withStatus)
	assert.Empty(t, withStatuses, "a tool taking statuses must be a read filter, allowlisted above")
}
