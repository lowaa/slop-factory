package tickets_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

const (
	preparing     = tickets.StatusPreparing
	discussion    = tickets.StatusDiscussion
	open          = tickets.StatusOpen
	inProgress    = tickets.StatusInProgress
	misconfigured = tickets.StatusMisconfigured
	done          = tickets.StatusDone
	cancelled     = tickets.StatusCancelled
)

// The table, spelt out a second time: a change to the transitions has to
// change this too, on purpose.
func TestCanTransition(t *testing.T) {
	t.Parallel()
	allowed := map[tickets.Status][]tickets.Status{
		preparing:     {discussion, open, misconfigured, cancelled},
		discussion:    {preparing, open, inProgress, cancelled},
		open:          {preparing, discussion, inProgress, misconfigured, cancelled},
		inProgress:    {discussion, open, done, cancelled},
		misconfigured: {preparing, open, cancelled},
		done:          {preparing, open},
		cancelled:     {preparing, open},
	}
	assert.Len(t, allowed, len(tickets.Statuses()))
	for _, from := range tickets.Statuses() {
		for _, to := range tickets.Statuses() {
			want := from == to
			for _, a := range allowed[from] {
				want = want || a == to
			}
			assert.Equal(t, want, tickets.CanTransition(from, to), "%s to %s", from, to)
		}
	}
}

func TestCanTransitionRefusesUnknownStatuses(t *testing.T) {
	t.Parallel()
	assert.False(t, tickets.CanTransition("limbo", open))
	assert.False(t, tickets.CanTransition(open, "limbo"))
	assert.False(t, tickets.CanTransition("", ""))
}

func TestOnlyWorkFinishesATicket(t *testing.T) {
	t.Parallel()
	for _, from := range tickets.Statuses() {
		if from != inProgress && from != done {
			assert.False(t, tickets.CanTransition(from, done), "%s to done", from)
		}
	}
}

func TestTerminal(t *testing.T) {
	t.Parallel()
	for _, s := range tickets.Statuses() {
		assert.Equal(t, s == done || s == cancelled, s.Terminal(), s)
	}
}
