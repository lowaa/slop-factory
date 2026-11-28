package agents_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

// reminders are the ask reminders in the user's inbox.
func (f *fixture) reminders() []string {
	f.t.Helper()
	msgs, err := f.state.Messages.Pending(context.Background(), state.UserParty)
	require.NoError(f.t, err)
	var out []string
	for _, m := range msgs {
		if m.Kind == state.KindSystem && strings.HasPrefix(m.Body, "Reminder:") {
			out = append(out, m.Body)
		}
	}
	return out
}

// An ask still open agents.ask_reminder_after after it was asked brings the
// user one reminder, on the sweeper's clock, and no more.
func TestAnUnansweredAskRemindsTheUserOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "agents: {ask_reminder_after: 1h}\n")
	_, a := f.waitingAgent("t", nil)

	f.clock.Advance(59 * time.Minute)
	f.mgr.RemindAsks(f.ctx())
	assert.Empty(t, f.reminders(), "not due yet")

	f.clock.Advance(time.Minute)
	ev := f.await(agents.EventAskReminded, nil)
	assert.Equal(t, a.Agent, ev.Agent)
	assert.Equal(t, a.Ticket, ev.Ticket)
	got := f.reminders()
	require.Len(t, got, 1)
	assert.Contains(t, got[0], "agent "+string(a.Agent)+" (ticket "+string(a.Ticket)+") asked 1h0m0s ago")
	assert.Contains(t, got[0], "> CSV or XLSX?")
	assert.Contains(t, got[0], "slop-bot tell "+string(a.Agent))

	f.clock.Advance(24 * time.Hour)
	f.mgr.RemindAsks(f.ctx())
	assert.Len(t, f.reminders(), 1, "once per ask")
}

// An answered ask is never reminded of.
func TestAnAnsweredAskIsNotRemindedOf(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "agents: {ask_reminder_after: 1h}\n")
	_, a := f.waitingAgent("t", nil)
	f.tell(a.Agent, "CSV")
	f.clock.Advance(2 * time.Hour)
	f.mgr.RemindAsks(f.ctx())
	assert.Empty(t, f.reminders())
}
