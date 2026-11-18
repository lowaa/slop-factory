package hookserver_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/hookserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// archivedHarness is a harness whose caller is in a real tenant row, so the
// tenant's archived state is read from the database; archive flips it.
func archivedHarness(t *testing.T) (h *harness, archive func(), unarchive func()) {
	t.Helper()
	database := dbtest.WithDB(t)
	tenant := dbtest.NewTenant(t, database)
	h = newHarnessWith(func(d *hookserver.Deps) { d.Archive = tenantstate.NewChecker(database) })
	h.ctx = db.WithTenant(h.ctx, tenant)
	h.tickets.lease = &ticket.Lease{TicketID: 7}
	h.tickets.ticket = &ticket.Ticket{ID: 7, Status: ticket.StatusInProgress}
	return h, func() { dbtest.ArchiveTenant(t, database, tenant) }, func() { dbtest.UnarchiveTenant(t, database, tenant) }
}

// An archived tenant's hooks do no work: Stop lets the agent go without
// counting a block, and the other two bind and record
// nothing. Unarchived, the same calls do their work.
func TestHooks_AnArchivedTenantsCallsDoNothing(t *testing.T) {
	h, archive, unarchive := archivedHarness(t)

	archive()
	decision, err := h.handler.Stop(h.ctx, &hookserver.StopInput{SessionID: "s1"})
	require.NoError(t, err)
	assert.Empty(t, decision.Decision, "the agent is let go")
	_, err = h.handler.SessionStart(h.ctx, &hookserver.SessionStartInput{SessionID: "s1"})
	require.NoError(t, err)
	require.NoError(t, h.handler.UserPromptSubmit(h.ctx, &hookserver.UserPromptSubmitInput{SessionID: "s1"}))
	assert.Zero(t, h.tickets.leaseCalls, "no lease is looked up")
	assert.Zero(t, h.sessions.blocksCalled)
	assert.Zero(t, h.sessions.startCalls)
	assert.Zero(t, h.sessions.touched)
	assert.Zero(t, h.sessions.resets)

	unarchive()
	decision, err = h.handler.Stop(h.ctx, &hookserver.StopInput{SessionID: "s1"})
	require.NoError(t, err)
	assert.Equal(t, "block", decision.Decision)
	_, err = h.handler.SessionStart(h.ctx, &hookserver.SessionStartInput{SessionID: "s1"})
	require.NoError(t, err)
	assert.NotZero(t, h.sessions.startCalls)
}
