package ticket_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Parked against real Postgres: a status only a person moves a ticket into
// or out of, never claimable, and never a finished dependency.

func (f *fixture) parked(t *testing.T, title string, paths ...string) *ticket.Ticket {
	t.Helper()
	created := f.preparing(t, title, paths...)
	moved, err := f.setStatus(t, created.ID, ticket.StatusParked)
	require.NoError(t, err)
	require.Equal(t, ticket.StatusParked, moved.Status)
	return moved
}

func TestPark_APreparingOrOpenTicketIsParkedAndUnparksToPreparing(t *testing.T) {
	t.Parallel()
	for _, from := range []ticket.Status{ticket.StatusPreparing, ticket.StatusOpen} {
		t.Run(string(from), func(t *testing.T) {
			f := newFixture(t)
			created, err := f.svc.Create(f.ctx, &ticket.CreateParams{Title: "Someday", Status: from})
			require.NoError(t, err)

			parked, err := f.setStatus(t, created.ID, ticket.StatusParked)
			require.NoError(t, err)
			assert.Equal(t, ticket.StatusParked, parked.Status)
			assert.Nil(t, parked.ClosedAt, "parked is not finished")

			unparked, err := f.setStatus(t, created.ID, ticket.StatusPreparing)
			require.NoError(t, err)
			assert.Equal(t, ticket.StatusPreparing, unparked.Status)
		})
	}
}

// Only a ticket nobody holds and nothing is waiting on is parked. A held
// one would be taken from under its agent; a blocked or discussion one is
// waiting on something parking would leave dangling; a finished one has
// nothing left to set aside.
func TestPark_RefusesEveryOtherStatus(t *testing.T) {
	t.Parallel()
	cases := map[ticket.Status]func(f *fixture, t *testing.T) int64{
		ticket.StatusInProgress: func(f *fixture, t *testing.T) int64 {
			held := f.ticket(t, "Held")
			f.claim(t, f.agent(t, "worker"))
			return held.ID
		},
		ticket.StatusReview: func(f *fixture, t *testing.T) int64 {
			a := f.agent(t, "worker")
			gated := f.gated(t, "Gated")
			f.claim(t, a)
			f.review(t, a)
			return gated.ID
		},
		ticket.StatusDiscussion: func(f *fixture, t *testing.T) int64 {
			a := f.agent(t, "worker")
			asked := f.ticket(t, "Asked about")
			f.claim(t, a)
			f.discuss(t, a)
			return asked.ID
		},
		ticket.StatusBlocked: func(f *fixture, t *testing.T) int64 {
			waiting := f.ticket(t, "Waiting")
			f.blockedBy(t, waiting.ID, f.ticket(t, "Prerequisite").ID)
			return waiting.ID
		},
		ticket.StatusDone: func(f *fixture, t *testing.T) int64 {
			finished := f.ticket(t, "Finished")
			_, err := f.setStatus(t, finished.ID, ticket.StatusDone)
			require.NoError(t, err)
			return finished.ID
		},
		ticket.StatusCancelled: func(f *fixture, t *testing.T) int64 {
			dropped := f.ticket(t, "Dropped")
			_, err := f.setStatus(t, dropped.ID, ticket.StatusCancelled)
			require.NoError(t, err)
			return dropped.ID
		},
	}
	for from, setUp := range cases {
		t.Run(string(from), func(t *testing.T) {
			f := newFixture(t)
			id := setUp(f, t)
			require.Equal(t, from, f.status(t, id))

			_, err := f.setStatus(t, id, ticket.StatusParked)
			assert.ErrorIs(t, err, ticket.ErrNotParkable)
			assert.Equal(t, from, f.status(t, id), "left where it was")
		})
	}
}

// Unparking goes back to preparing, so a person looks at the ticket again
// before anything can claim it; discarding it or finding it done is allowed,
// as from preparing.
func TestUnpark_GoesOnlyToPreparing(t *testing.T) {
	t.Parallel()
	for _, next := range []ticket.Status{ticket.StatusOpen, ticket.StatusBlocked, ticket.StatusInProgress, ticket.StatusDiscussion, ticket.StatusReview} {
		t.Run("refuses "+string(next), func(t *testing.T) {
			f := newFixture(t)
			idea := f.parked(t, "Idea")

			_, err := f.setStatus(t, idea.ID, next)
			assert.ErrorIs(t, err, ticket.ErrLeaveParked)
			assert.Equal(t, ticket.StatusParked, f.status(t, idea.ID))
		})
	}
	for _, next := range []ticket.Status{ticket.StatusPreparing, ticket.StatusCancelled, ticket.StatusDone, ticket.StatusParked} {
		t.Run("allows "+string(next), func(t *testing.T) {
			f := newFixture(t)
			idea := f.parked(t, "Idea")

			moved, err := f.setStatus(t, idea.ID, next)
			require.NoError(t, err)
			assert.Equal(t, next, moved.Status)
		})
	}
	t.Run("Ready is for preparing tickets only", func(t *testing.T) {
		f := newFixture(t)
		idea := f.parked(t, "Idea")

		_, err := f.svc.MarkReady(f.ctx, idea.ID)
		assert.ErrorIs(t, err, ticket.ErrNotPreparing)
		assert.Equal(t, ticket.StatusParked, f.status(t, idea.ID))
	})
}

// Parked is never offered: not as the next ticket, not by id, and not
// ahead of lower-priority open work. Peek is what the Stop hook and the
// idle-agent wake-up ask, so this covers them too.
func TestParked_IsNeverClaimable(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	idea := f.parked(t, "Someday", "server/")
	urgent := model.PriorityUrgent
	_, err := f.svc.Update(f.ctx, idea.ID, &ticket.UpdateParams{Priority: &urgent})
	require.NoError(t, err)

	peeked, err := f.svc.Peek(f.ctx, a, nil)
	require.NoError(t, err)
	assert.Nil(t, peeked.Ticket, "the Stop hook and the wake-up must not send an agent after it")

	directed, err := f.svc.Claim(f.ctx, a, &idea.ID)
	require.NoError(t, err)
	assert.Nil(t, directed.Ticket, "not even when asked for by id")

	queued := f.ticket(t, "Low-priority open work", "web/")
	result, err := f.svc.Claim(f.ctx, a, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Ticket)
	assert.Equal(t, queued.ID, result.Ticket.ID, "open work is taken; the urgent parked ticket is not")
	assert.Equal(t, ticket.StatusParked, f.status(t, idea.ID))
}

// Parking is "not now", not "won't do": work waiting on a parked ticket
// stays waiting, and is released only once the ticket is unparked and
// actually done.
func TestParked_DependentsStayBlockedUntilItIsUnparkedAndDone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	prerequisite := f.ticket(t, "Prerequisite", "server/")
	waiting := f.ticket(t, "Needs the prerequisite", "web/")
	f.blockedBy(t, waiting.ID, prerequisite.ID)
	require.Equal(t, ticket.StatusBlocked, f.status(t, waiting.ID))

	_, err := f.setStatus(t, prerequisite.ID, ticket.StatusParked)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusBlocked, f.status(t, waiting.ID), "parking the prerequisite releases nothing")
	result, err := f.svc.Claim(f.ctx, a, nil)
	require.NoError(t, err)
	assert.Nil(t, result.Ticket)

	g, err := f.svc.Graph(f.ctx, &ticket.GraphQuery{})
	require.NoError(t, err)
	node := f.graphNode(t, g, waiting.ID)
	assert.True(t, node.Blocked, "the graph shows it held up")
	waitingNow := f.get(t, waiting.ID)
	require.Len(t, waitingNow.Blockers, 1)
	assert.Equal(t, ticket.StatusParked, waitingNow.Blockers[0].Status, "and says by what")

	_, err = f.setStatus(t, prerequisite.ID, ticket.StatusPreparing)
	require.NoError(t, err)
	_, err = f.svc.MarkReady(f.ctx, prerequisite.ID)
	require.NoError(t, err)
	claimed := f.claim(t, a)
	require.NotNil(t, claimed)
	require.Equal(t, prerequisite.ID, claimed.ID)
	_, err = f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "done"})
	require.NoError(t, err)

	assert.Equal(t, ticket.StatusOpen, f.status(t, waiting.ID))
	next := f.claim(t, a)
	require.NotNil(t, next, "claimable once its prerequisite is done")
	assert.Equal(t, waiting.ID, next.ID)
}

// Like preparing, a parked ticket carries no first claim: parking an open
// ticket someone asked about sets it aside for everyone.
func TestPark_DropsFirstClaim(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	asker := f.agent(t, "asker")
	created := f.ticket(t, "Asked about", "server/")
	f.claim(t, asker)
	f.discuss(t, asker)
	f.humanReply(t, created.ID)
	require.Equal(t, ticket.StatusOpen, f.status(t, created.ID))

	moved, err := f.setStatus(t, created.ID, ticket.StatusParked)
	require.NoError(t, err)
	assert.Nil(t, moved.ResumeAgentID)
	assert.Nil(t, moved.AffinityUntil)
}

func TestParked_IsListedOnlyWhenAskedFor(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	idea := f.parked(t, "Someday")
	queued := f.ticket(t, "Now")

	board := f.search(t, &ticket.ListFilter{Statuses: []ticket.Status{ticket.StatusPreparing, ticket.StatusOpen}})
	assert.Equal(t, []int64{queued.ID}, board)

	withParked := f.search(t, &ticket.ListFilter{Statuses: []ticket.Status{ticket.StatusParked, ticket.StatusOpen}})
	assert.ElementsMatch(t, []int64{idea.ID, queued.ID}, withParked)
}

func TestUpdateTicket_ParkAndUnparkOverTheWire(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	idea := f.preparing(t, "Idea")
	path := fmt.Sprintf("/api/tickets/%d", idea.ID)

	rec := send(t, mux, http.MethodPatch, path, `{"status": "parked"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, ticket.StatusParked, decodeTicket(t, rec.Body.Bytes()).Status)

	rec = send(t, mux, http.MethodGet, "/api/tickets?status=parked", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"status":"parked"`, "the board's filter can ask for it")

	waiting := f.ticket(t, "Waits on the idea")
	f.blockedBy(t, waiting.ID, idea.ID)
	rec = send(t, mux, http.MethodGet, fmt.Sprintf("/api/tickets/%d", waiting.ID), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []ticket.TicketRefBody{{ID: idea.ID, Title: "Idea", Status: "parked"}},
		decodeTicket(t, rec.Body.Bytes()).Blockers, "a blocker carries its status, so a parked one is visible")

	rec = send(t, mux, http.MethodPatch, path, `{"status": "open"}`)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "unparked back to preparing", "the refusal says what to do instead")

	rec = send(t, mux, http.MethodPatch, path, `{"status": "preparing"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, ticket.StatusPreparing, decodeTicket(t, rec.Body.Bytes()).Status)

	a := f.agent(t, "worker")
	_, err := f.svc.MarkReady(f.ctx, idea.ID)
	require.NoError(t, err)
	f.claim(t, a)
	rec = send(t, mux, http.MethodPatch, path, `{"status": "parked"}`)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "release a held ticket")
	assert.Equal(t, ticket.StatusInProgress, f.status(t, idea.ID))
}

// Parking and unparking are a person's: an agent's token is refused on the
// board's PATCH both ways, and nothing it can call files a ticket parked.
func TestParkWire_AnAgentCannotParkOrUnpark(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	mux, _ := f.rolesMux(t, true)
	queued := f.ticket(t, "Queued")
	idea := f.parked(t, "Idea")

	rec := sendAs(t, mux, http.MethodPatch, fmt.Sprintf("/api/tickets/%d", queued.ID), rolesAgentToken, `{"status": "parked"}`)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	rec = sendAs(t, mux, http.MethodPatch, fmt.Sprintf("/api/tickets/%d", idea.ID), rolesAgentToken, `{"status": "preparing"}`)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Equal(t, ticket.StatusOpen, f.status(t, queued.ID))
	assert.Equal(t, ticket.StatusParked, f.status(t, idea.ID))

	// Nor is a ticket filed parked, by anyone: parking is a decision about
	// a ticket that exists.
	rec = sendAs(t, mux, http.MethodPost, "/api/tickets", rolesUserToken, `{"title": "Straight to the shelf", "status": "parked"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	a := f.agent(t, "filer")
	_, err := f.svc.Create(f.ctx, &ticket.CreateParams{Title: "x", CreatedByAgentID: &a, Status: ticket.StatusParked})
	assert.ErrorIs(t, err, ticket.ErrInvalidEnum)

	// The token itself works: it is refused for what it is.
	rec = sendAs(t, mux, http.MethodPost, "/api/claims", rolesAgentToken, `{}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}
