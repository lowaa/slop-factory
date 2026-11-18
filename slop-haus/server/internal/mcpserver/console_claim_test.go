package mcpserver_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
)

const consoleQueueRefusal = "consoles claim a named ticket; the queue is for workers"

// consoleBy is a console as each way of being one: by kind, and by the legacy
// flag on a worker.
func (r *realRig) consoleBy(name string, kind bool) *consoleSession {
	r.t.Helper()
	if !kind {
		return r.agentSession(name, true)
	}
	created, err := r.agents.CreateKind(r.ctx, name, agent.KindConsole)
	require.NoError(r.t, err)
	return r.connectAs(name, created.ID)
}

func (r *realRig) activeLeases(agentID uuid.UUID) int {
	r.t.Helper()
	var n int
	require.NoError(r.t, r.database.QueryRow(r.ctx,
		`SELECT count(*) FROM leases WHERE agent_id = $1 AND released_at IS NULL`, agentID).Scan(&n))
	return n
}

func TestConsoleTicketNext(t *testing.T) {
	for name, byKind := range map[string]bool{"kind": true, "legacy flag": false} {
		t.Run(name, func(t *testing.T) {
			r := newRealRig(t)
			queued := r.openTicket("queued")
			c := r.consoleBy("console", byKind)

			res := c.call(t, "ticket_next", nil)
			assert.Contains(t, resultText(t, res), consoleQueueRefusal)
			assert.Zero(t, r.activeLeases(c.agentID), "refused, so no lease")

			res = c.call(t, "ticket_next", map[string]any{"ticket_id": queued.ID})
			assert.Contains(t, resultText(t, res), "Granted ticket")
			assert.Equal(t, 1, r.activeLeases(c.agentID), "a named ticket is granted")
		})
	}
}

func TestWorkerTicketNextIsUnaffectedByConsoles(t *testing.T) {
	r := newRealRig(t)
	r.openTicket("queued")
	assert.Contains(t, resultText(t, r.call("ticket_next", nil)), "Granted ticket")
}

func TestCheckinTellsAConsoleToNameATicket(t *testing.T) {
	r := newRealRig(t)
	c := r.consoleBy("console", true)
	assert.Contains(t, resultText(t, c.call(t, "checkin", nil)), "ticket_next with its id")
	assert.Contains(t, resultText(t, r.call("checkin", nil)), "Call ticket_next to take work")
}
