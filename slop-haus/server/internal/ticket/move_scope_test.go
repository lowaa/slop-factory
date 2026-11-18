package ticket_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// A human's move that makes a ticket hold its scope -- into review, from a
// status holding none -- is checked against every other held scope, single
// or bulk, and refused whole on a conflict. A ticket that held its scope
// already is not: the move adds no overlap that was not there.

// forceStatus writes a status behind the service's back, for setting up a
// board no move would leave -- two tickets in review over the same files.
func (f *fixture) forceStatus(t *testing.T, id int64, status ticket.Status) {
	t.Helper()
	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET status = $2 WHERE id = $1`, id, string(status))
	require.NoError(t, err)
}

func requireRefusedOnly(t *testing.T, err error, ids ...int64) []ticket.Refusal {
	t.Helper()
	refusals := refusalsOf(t, err)
	got := make([]int64, len(refusals))
	for i, r := range refusals {
		got[i] = r.TicketID
		require.ErrorIs(t, r.Err, ticket.ErrScopeConflict)
	}
	require.Equal(t, ids, got)
	return refusals
}

func TestMove_IntoReviewOverAnotherHeldScopeIsRefused(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// make leaves a ticket with a/x.go, holding no scope, in a status
		// a move into review is allowed from.
		make func(t *testing.T, f *fixture) *ticket.Ticket
	}{{
		name: "open",
		make: func(t *testing.T, f *fixture) *ticket.Ticket { return f.ticket(t, "Open", "a/x.go") },
	}, {
		name: "done",
		make: func(t *testing.T, f *fixture) *ticket.Ticket {
			tk := f.ticket(t, "Done", "a/x.go")
			_, err := f.setStatus(t, tk.ID, ticket.StatusDone)
			require.NoError(t, err)
			return tk
		},
	}, {
		name: "cancelled",
		make: func(t *testing.T, f *fixture) *ticket.Ticket {
			tk := f.ticket(t, "Cancelled", "a/x.go")
			_, err := f.setStatus(t, tk.ID, ticket.StatusCancelled)
			require.NoError(t, err)
			return tk
		},
	}, {
		// In progress with no lease holds nothing, so it is not a way
		// around the check.
		name: "in progress, unleased",
		make: func(t *testing.T, f *fixture) *ticket.Ticket {
			tk := f.ticket(t, "Unleased", "a/x.go")
			_, err := f.setStatus(t, tk.ID, ticket.StatusInProgress)
			require.NoError(t, err)
			return tk
		},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			// Made first, so the other ticket's setup is not refused by it.
			moved := tc.make(t, f)
			from := f.status(t, moved.ID)
			other, _ := f.holding(t, "other", false, "a/")

			_, err := f.setStatus(t, moved.ID, ticket.StatusReview)

			requireScopeConflict(t, err, moved.ID, other.ID, "a/")
			assert.Equal(t, from, f.status(t, moved.ID), "the move is not written")
		})
	}
}

// Over the wire the refusal is a 409 that names what stood in the way.
func TestMove_ARefusedMoveIntoReviewIsAConflictOverTheWire(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	moved := f.ticket(t, "Open", "a/x.go")
	other, _ := f.holding(t, "other", true, "a/")

	rec := send(t, f.boardMux(), http.MethodPatch, fmt.Sprintf("/api/tickets/%d", moved.ID), `{"status": "review"}`)

	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), fmt.Sprintf("held by #%d", other.ID))
	assert.Equal(t, ticket.StatusOpen, f.status(t, moved.ID))
}

func TestMove_IntoReviewClearOfOtherScopesIsWritten(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	moved := f.ticket(t, "Open", "b/")
	f.holding(t, "other", false, "a/")

	got, err := f.setStatus(t, moved.ID, ticket.StatusReview)

	require.NoError(t, err)
	assert.Equal(t, ticket.StatusReview, got.Status)
}

// One conflict and the whole batch is refused, every conflict named,
// nothing written -- the bulk route judged as the single one is.
func TestBulkMove_IntoReviewOverAHeldScopeRefusesTheBatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	clear := f.ticket(t, "Clear", "c/")
	first := f.ticket(t, "First", "a/x.go")
	second := f.ticket(t, "Second", "b/y.go")
	holderA, _ := f.holding(t, "holder a", false, "a/")
	holderB, _ := f.holding(t, "holder b", true, "b/")

	_, err := f.svc.MoveMany(f.ctx, []int64{second.ID, clear.ID, first.ID}, ticket.StatusReview)

	refusals := requireRefusedOnly(t, err, first.ID, second.ID)
	assert.Equal(t, ticket.StatusOpen, refusals[0].From)
	var conflict *ticket.ScopeConflictError
	require.ErrorAs(t, refusals[0].Err, &conflict)
	assert.Equal(t, holderA.ID, conflict.Blocked.ConflictingTicketID)
	assert.False(t, conflict.InBatch)
	require.ErrorAs(t, refusals[1].Err, &conflict)
	assert.Equal(t, holderB.ID, conflict.Blocked.ConflictingTicketID)
	for _, id := range []int64{clear.ID, first.ID, second.ID} {
		assert.Equal(t, ticket.StatusOpen, f.status(t, id), "#%d is not moved", id)
	}

	rec := send(t, f.boardMux(), http.MethodPost, "/api/tickets/bulk/status",
		fmt.Sprintf(`{"ids": [%d, %d], "status": "review"}`, clear.ID, first.ID))
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), fmt.Sprintf(`"id":%d`, first.ID))
	assert.Equal(t, ticket.StatusOpen, f.status(t, clear.ID))
}

// Two tickets of one batch over the same files would both hold them: both
// are refused, and the refusal says the overlap is the batch's own.
func TestBulkMove_IntoReviewOverlappingWithinTheBatchIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	wide := f.ticket(t, "Wide", "a/")
	narrow := f.ticket(t, "Narrow", "a/x.go")

	_, err := f.svc.MoveMany(f.ctx, []int64{wide.ID, narrow.ID}, ticket.StatusReview)

	refusals := requireRefusedOnly(t, err, wide.ID, narrow.ID)
	for i, other := range []int64{narrow.ID, wide.ID} {
		var conflict *ticket.ScopeConflictError
		require.ErrorAs(t, refusals[i].Err, &conflict)
		assert.Equal(t, other, conflict.Blocked.ConflictingTicketID)
		assert.True(t, conflict.InBatch)
		assert.Contains(t, refusals[i].Reason(), "this same move")
	}
	assert.Equal(t, ticket.StatusOpen, f.status(t, wide.ID))
	assert.Equal(t, ticket.StatusOpen, f.status(t, narrow.ID))
}

// An overlap already on the board is not a move's doing: a ticket in it
// can still be moved out of review, or left there, and a held ticket
// moved into review keeps the scope it held.
func TestMove_ATicketAlreadyHoldingItsScopeIsNotRefused(t *testing.T) {
	t.Parallel()
	t.Run("out of review, and review to review", func(t *testing.T) {
		f := newFixture(t)
		first := f.ticket(t, "First", "a/")
		second := f.ticket(t, "Second", "a/x.go")
		f.forceStatus(t, first.ID, ticket.StatusReview)
		f.forceStatus(t, second.ID, ticket.StatusReview)

		_, err := f.setStatus(t, first.ID, ticket.StatusReview)
		require.NoError(t, err)
		f.moveMany(t, ticket.StatusReview, first.ID, second.ID)
		got, err := f.setStatus(t, first.ID, ticket.StatusDone)
		require.NoError(t, err)
		assert.Equal(t, ticket.StatusDone, got.Status)
	})
	t.Run("leased into review", func(t *testing.T) {
		f := newFixture(t)
		moved, _ := f.holding(t, "moved", false, "a/x.go")
		other := f.ticket(t, "Other", "a/")
		f.forceStatus(t, other.ID, ticket.StatusReview)

		got, err := f.setStatus(t, moved.ID, ticket.StatusReview)
		require.NoError(t, err)
		assert.Equal(t, ticket.StatusReview, got.Status)
	})
}

// A move that also sets paths is judged on the paths it sets, not the
// ones it replaces.
func TestMove_IntoReviewWithNewPathsClearOfTheConflictIsWritten(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	moved := f.ticket(t, "Open", "a/x.go")
	f.holding(t, "other", false, "a/")
	review := ticket.StatusReview

	got, err := f.svc.Update(f.ctx, moved.ID, &ticket.UpdateParams{Status: &review, Paths: scopes("b/")})

	require.NoError(t, err)
	assert.Equal(t, ticket.StatusReview, got.Status)
	assert.Equal(t, []string{"b/"}, pathNames(got))
}
