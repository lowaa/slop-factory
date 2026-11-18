package ticket_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// attemptStatusOf is the status an attempt shows for a ticket's: the table
// attempt_status_for_ticket is written from, restated here so the
// two are compared and neither is trusted for the other.
var attemptStatusOf = map[ticket.Status]string{
	ticket.StatusPreparing: "queued", ticket.StatusParked: "queued", ticket.StatusOpen: "queued",
	ticket.StatusBlocked: "queued", ticket.StatusInProgress: "in_progress", ticket.StatusDiscussion: "discussion",
	ticket.StatusReview: "review", ticket.StatusDone: "done", ticket.StatusCancelled: "closed",
}

// attemptFollowsTicket asserts the promoted attempt of id shows the ticket's
// status, updated_at and closed_at.
func (f *fixture) attemptFollowsTicket(t *testing.T, id int64, want ticket.Status) {
	t.Helper()
	var status string
	var sameUpdated, sameClosed bool
	var ticketStatus ticket.Status
	require.NoError(t, f.database.QueryRow(f.ctx, `
		SELECT t.status, a.status, a.updated_at <= t.updated_at, a.closed_at IS NOT DISTINCT FROM t.closed_at
		  FROM tickets t JOIN attempts a ON a.tenant_id = t.tenant_id AND a.ticket_id = t.id AND a.promoted_at IS NOT NULL
		 WHERE t.id = $1`, id).Scan(&ticketStatus, &status, &sameUpdated, &sameClosed))
	require.Equal(t, want, ticketStatus)
	assert.Equal(t, attemptStatusOf[want], status, "the attempt shows a ticket in %s", want)
	assert.True(t, sameUpdated, "and is no newer than the ticket: the queue state alone (blocked, open) moves only the ticket")
	assert.True(t, sameClosed, "and its closed_at")
}

// Each transition ticket.Service makes writes the attempt's status in the same
// transaction as the ticket's. The
// cases are one transition each, so a transition that forgets its attempt
// fails its own case.
func TestTransitionsWriteTheAttemptStatus(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		do   func(t *testing.T, f *fixture) (id int64, want ticket.Status)
	}
	// held is a ticket claimed by a worker.
	held := func(t *testing.T, f *fixture, gated bool) (int64, uuid.UUID) {
		t.Helper()
		a := f.agent(t, "builder")
		var created *ticket.Ticket
		if gated {
			created = f.gated(t, "Gated", "server/")
		} else {
			created = f.ticket(t, "Plain", "server/")
		}
		f.claim(t, a)
		return created.ID, a
	}
	cases := []tc{
		{"claim", func(t *testing.T, f *fixture) (int64, ticket.Status) {
			id, _ := held(t, f, false)
			return id, ticket.StatusInProgress
		}},
		{"discuss", func(t *testing.T, f *fixture) (int64, ticket.Status) {
			id, a := held(t, f, false)
			f.discuss(t, a)
			return id, ticket.StatusDiscussion
		}},
		{"review", func(t *testing.T, f *fixture) (int64, ticket.Status) {
			id, a := held(t, f, true)
			require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, a))
			f.review(t, a)
			return id, ticket.StatusReview
		}},
		{"approve", func(t *testing.T, f *fixture) (int64, ticket.Status) {
			id, a := held(t, f, true)
			require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, a))
			f.review(t, a)
			approved := f.approve(t, id)
			return id, approved.Status
		}},
		{"request changes", func(t *testing.T, f *fixture) (int64, ticket.Status) {
			id, a := held(t, f, true)
			require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, a))
			f.review(t, a)
			sent := f.requestChanges(t, id)
			return id, sent.Status
		}},
		{"done", func(t *testing.T, f *fixture) (int64, ticket.Status) {
			id, a := held(t, f, false)
			_, err := f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "Shipped."})
			require.NoError(t, err)
			return id, ticket.StatusDone
		}},
		{"park", func(t *testing.T, f *fixture) (int64, ticket.Status) {
			created := f.ticket(t, "Idea", "server/")
			_, err := f.setStatus(t, created.ID, ticket.StatusParked)
			require.NoError(t, err)
			return created.ID, ticket.StatusParked
		}},
		{"cancel", func(t *testing.T, f *fixture) (int64, ticket.Status) {
			id, _ := held(t, f, false)
			_, err := f.setStatus(t, id, ticket.StatusCancelled)
			require.NoError(t, err)
			return id, ticket.StatusCancelled
		}},
		{"reopen", func(t *testing.T, f *fixture) (int64, ticket.Status) {
			created := f.ticket(t, "Closed early", "server/")
			_, err := f.setStatus(t, created.ID, ticket.StatusCancelled)
			require.NoError(t, err)
			_, err = f.setStatus(t, created.ID, ticket.StatusOpen)
			require.NoError(t, err)
			return created.ID, ticket.StatusOpen
		}},
		{"handoff", func(t *testing.T, f *fixture) (int64, ticket.Status) {
			id, a := held(t, f, false)
			_, err := f.svc.ReleaseWith(f.ctx, a, ticket.ReleaseParams{Reason: "stopping here", Handoff: "Branch pushed."})
			require.NoError(t, err)
			return id, ticket.StatusOpen
		}},
		{"release blocked", func(t *testing.T, f *fixture) (int64, ticket.Status) {
			_, _, _, waiting := blockedOn(t, f)
			return waiting.ID, ticket.StatusBlocked
		}},
		{"unblock", func(t *testing.T, f *fixture) (int64, ticket.Status) {
			_, b, dep, waiting := blockedOn(t, f)
			_, err := f.svc.Done(f.ctx, b, &ticket.DoneParams{Summary: "Done."})
			require.NoError(t, err)
			require.Equal(t, ticket.StatusDone, f.status(t, dep.ID))
			return waiting.ID, ticket.StatusOpen
		}},
		{"lease expiry", func(t *testing.T, f *fixture) (int64, ticket.Status) {
			id, a := held(t, f, false)
			other := f.ticket(t, "Elsewhere", "web/")
			f.expire(t, a)
			_, err := f.svc.Claim(f.ctx, f.agent(t, "second"), &other.ID)
			require.NoError(t, err)
			return id, ticket.StatusOpen
		}},
		{"agent disabled", func(t *testing.T, f *fixture) (int64, ticket.Status) {
			id, a := held(t, f, false)
			require.NoError(t, f.svc.ReleaseLeasesFor(f.ctx, a, "disabled", time.Now().Add(time.Hour)))
			return id, ticket.StatusOpen
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			id, want := c.do(t, f)
			f.attemptFollowsTicket(t, id, want)
		})
	}
}
