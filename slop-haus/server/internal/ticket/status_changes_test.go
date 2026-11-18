package ticket_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// change is one ticket_status_changes row, less its timestamps.
type change struct {
	From   string
	To     string
	Actor  string
	Who    *uuid.UUID
	Cause  string
	Prio   string
	Ticket int64
}

func (f *fixture) history(t *testing.T, id int64) []change {
	t.Helper()
	rows, err := f.database.Query(f.ctx, `
		SELECT COALESCE(from_status, ''), to_status, actor_kind, actor_id, COALESCE(cause, ''), priority, ticket_id
		  FROM ticket_status_changes WHERE ticket_id = $1 ORDER BY id`, id)
	require.NoError(t, err)
	defer rows.Close()
	var got []change
	for rows.Next() {
		var c change
		require.NoError(t, rows.Scan(&c.From, &c.To, &c.Actor, &c.Who, &c.Cause, &c.Prio, &c.Ticket))
		got = append(got, c)
	}
	require.NoError(t, rows.Err())
	return got
}

// assertHistoryFollowsStatus is what every transition test gets through
// written(): the ticket's recorded history starts at creation, each row
// picks up where the last left off, and the last is where the ticket is.
// A status written without a row breaks the chain at that write, so no
// transition those tests walk can skip the record.
func (f *fixture) assertHistoryFollowsStatus(t *testing.T, id int64, status ticket.Status) {
	t.Helper()
	history := f.history(t, id)
	require.NotEmpty(t, history, "ticket #%d has no recorded status change", id)
	assert.Empty(t, history[0].From, "the first change is the ticket's creation")
	for i := 1; i < len(history); i++ {
		assert.Equal(t, history[i-1].To, history[i].From, "change %d of #%d picks up where the last left off", i, id)
		assert.NotEqual(t, history[i].From, history[i].To, "a change that moves nothing is not recorded")
	}
	assert.Equal(t, string(status), history[len(history)-1].To, "the last change of #%d is where it is now", id)
}

// Each way a status moves leaves exactly one row, saying from where to
// where, who moved it and, where it is known, why.
func TestStatusChanges_EveryRouteIsRecordedWithItsActor(t *testing.T) {
	t.Parallel()
	agent := func(id uuid.UUID) *uuid.UUID { return &id }

	t.Run("filed by an agent, then marked ready by a person", func(t *testing.T) {
		f := newFixture(t)
		filer, user := f.agent(t, "filer"), &f.human
		filed := f.filedBy(t, filer, "Filed", ticket.StatusPreparing)
		require.Equal(t, []change{
			{To: "preparing", Actor: "agent", Who: agent(filer), Cause: "created", Prio: "normal", Ticket: filed.ID},
		}, f.history(t, filed.ID))

		_, err := f.svc.MarkReady(ticket.WithUser(f.ctx, f.human), filed.ID)
		require.NoError(t, err)
		assert.Equal(t, change{From: "preparing", To: "open", Actor: "user", Who: user, Prio: "normal", Ticket: filed.ID},
			f.history(t, filed.ID)[1])
	})

	t.Run("claim, discuss, answer, claim, review, approve, claim, done", func(t *testing.T) {
		f := newFixture(t)
		a, user := f.agent(t, "builder-1"), &f.human
		tk := f.ticket(t, "Walks the board", "walk/")
		f.claim(t, a)
		f.discuss(t, a)
		f.humanReply(t, tk.ID)
		f.claim(t, a)
		f.review(t, a)
		_, err := f.svc.Approve(f.ctx, tk.ID, f.human)
		require.NoError(t, err)
		f.claim(t, a)
		_, err = f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "ok", PRURL: reviewPR})
		require.NoError(t, err)

		assert.Equal(t, []change{
			{To: "open", Actor: "system", Cause: "created"},
			{From: "open", To: "in_progress", Actor: "agent", Who: agent(a), Cause: "claimed"},
			{From: "in_progress", To: "discussion", Actor: "agent", Who: agent(a), Cause: "discussion"},
			{From: "discussion", To: "open", Actor: "user", Who: user},
			{From: "open", To: "in_progress", Actor: "agent", Who: agent(a), Cause: "claimed"},
			{From: "in_progress", To: "review", Actor: "agent", Who: agent(a), Cause: "review"},
			{From: "review", To: "open", Actor: "user", Who: user},
			{From: "open", To: "in_progress", Actor: "agent", Who: agent(a), Cause: "claimed"},
			{From: "in_progress", To: "done", Actor: "agent", Who: agent(a), Cause: "done"},
		}, core(f.history(t, tk.ID)))
	})

	t.Run("blocked by a release, unblocked by the blocker finishing", func(t *testing.T) {
		f := newFixture(t)
		a, other := f.agent(t, "builder-1"), f.agent(t, "builder-2")
		blocker := f.ticket(t, "Blocker", "blocker/")
		waiting := f.ticket(t, "Waiting", "waiting/")
		_, err := f.svc.Claim(f.ctx, a, &waiting.ID)
		require.NoError(t, err)
		_, err = f.svc.Release(f.ctx, a, "needs the blocker", &blocker.ID)
		require.NoError(t, err)
		_, err = f.svc.Claim(f.ctx, other, &blocker.ID)
		require.NoError(t, err)
		_, err = f.svc.Done(f.ctx, other, &ticket.DoneParams{Summary: "ok"})
		require.NoError(t, err)

		got := core(f.history(t, waiting.ID))
		require.Len(t, got, 4)
		assert.Equal(t, change{From: "in_progress", To: "blocked", Actor: "agent", Who: agent(a), Cause: "needs the blocker"}, got[2])
		assert.Equal(t, change{From: "blocked", To: "open", Actor: "system", Cause: "unblocked"}, got[3])
	})

	t.Run("a ticket filed blocked", func(t *testing.T) {
		f := newFixture(t)
		user := &f.human
		blocker := f.ticket(t, "Blocker 2", "b2/")
		blocked, err := f.svc.Create(f.ctx, &ticket.CreateParams{
			Title: "Born blocked", BlockedOn: []int64{blocker.ID}, CreatedByUserID: user, Status: ticket.StatusOpen,
		})
		require.NoError(t, err)
		assert.Equal(t, []change{
			{To: "open", Actor: "user", Who: user, Cause: "created", Prio: "normal", Ticket: blocked.ID},
			{From: "open", To: "blocked", Actor: "user", Who: user, Cause: "blocked", Prio: "normal", Ticket: blocked.ID},
		}, f.history(t, blocked.ID))
	})

	t.Run("lease expiry, and an agent disabled", func(t *testing.T) {
		f := newFixture(t)
		a, other := f.agent(t, "builder-1"), f.agent(t, "builder-2")
		expiring := f.ticket(t, "Expires", "expires/")
		_, err := f.svc.Claim(f.ctx, a, &expiring.ID)
		require.NoError(t, err)
		f.expire(t, a)
		_, err = f.svc.Claim(f.ctx, other, nil)
		require.NoError(t, err)
		got := f.history(t, expiring.ID)
		require.Len(t, got, 4)
		assert.Equal(t, change{From: "in_progress", To: "open", Actor: "system", Cause: "lease_expired", Prio: "normal", Ticket: expiring.ID}, got[2])

		disabled := f.ticket(t, "Disabled", "disabled/")
		_, err = f.svc.Claim(f.ctx, a, &disabled.ID)
		require.NoError(t, err)
		require.NoError(t, f.svc.ReleaseLeasesFor(f.ctx, a, "agent disabled", time.Now().Add(time.Hour)))
		got = f.history(t, disabled.ID)
		assert.Equal(t, change{From: "in_progress", To: "open", Actor: "system", Cause: "agent disabled", Prio: "normal", Ticket: disabled.ID}, got[len(got)-1])
	})

	t.Run("a person's moves on the board, and a merge on GitHub", func(t *testing.T) {
		f := newFixture(t)
		a, user := f.agent(t, "builder-1"), &f.human
		moved := f.ticket(t, "Moved about", "moved/")
		_, err := f.svc.MoveMany(ticket.WithUser(f.ctx, f.human), []int64{moved.ID}, ticket.StatusCancelled)
		require.NoError(t, err)
		got := f.history(t, moved.ID)
		assert.Equal(t, change{From: "open", To: "cancelled", Actor: "user", Who: user, Prio: "normal", Ticket: moved.ID}, got[1])

		merged := f.inReviewWith(t, a, "Merged elsewhere", githubRefLink)
		_, err = f.svc.CloseMergedPR(f.ctx, githubRef, f.human)
		require.NoError(t, err)
		got = f.history(t, merged.ID)
		assert.Equal(t, change{From: "review", To: "done", Actor: "user", Who: user, Prio: "normal", Ticket: merged.ID}, got[len(got)-1])
	})
}

// core drops what a walk's expectations do not repeat on every row: the
// ticket and its priority.
func core(cs []change) []change {
	out := make([]change, len(cs))
	for i, c := range cs {
		c.Ticket, c.Prio = 0, ""
		out[i] = c
	}
	return out
}

// A transition that fails after writing the status leaves no row, as it
// leaves no status.
func TestStatusChanges_ARolledBackTransitionLeavesNoRow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.ticket(t, "Held back", "held/")
	before := f.history(t, tk.ID)
	store := newStore(f.database)
	boom := errors.New("boom")

	err := store.InTransition(f.ctx, func(tx ticket.TransitionTx) error {
		if err := tx.WriteTicket(f.ctx, tk.ID, &ticket.TicketWrite{Status: ticket.To(ticket.StatusCancelled)}); err != nil {
			return err
		}
		return boom
	})

	require.ErrorIs(t, err, boom)
	assert.Equal(t, before, f.history(t, tk.ID))
	assert.Equal(t, ticket.StatusOpen, f.status(t, tk.ID))
}

// The board's routes know who is signed in; a move made through them is
// recorded as that person's, not as an automatic one.
func TestStatusChanges_ABoardRouteRecordsTheSignedInUser(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	edited := f.ticket(t, "Edited", "edited/")
	drafted := f.preparing(t, "Drafted")
	bulk := f.ticket(t, "Bulk moved", "bulk/")

	for _, rec := range []*httptest.ResponseRecorder{
		send(t, mux, http.MethodPatch, fmt.Sprintf("/api/tickets/%d", edited.ID), `{"status": "cancelled"}`),
		send(t, mux, http.MethodPost, fmt.Sprintf("/api/tickets/%d/ready", drafted.ID), ""),
		send(t, mux, http.MethodPost, "/api/tickets/bulk/status", fmt.Sprintf(`{"ids": [%d], "status": "parked"}`, bulk.ID)),
	} {
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}

	for _, id := range []int64{edited.ID, drafted.ID, bulk.ID} {
		history := f.history(t, id)
		last := history[len(history)-1]
		assert.Equal(t, "user", last.Actor, "#%d", id)
		assert.Equal(t, &f.human, last.Who, "#%d", id)
	}
}

// The record is under the same row-level security as the tickets it
// describes: each tenant sees its own history, and a context with no tenant
// sees none.
func TestStatusChanges_AreTenantScoped(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mine := f.ticket(t, "Mine", "mine/")
	otherCtx := db.WithTenant(context.Background(), dbtest.NewTenant(t, f.database))
	theirs, err := f.svc.Create(otherCtx, &ticket.CreateParams{Title: "Theirs"})
	require.NoError(t, err)
	require.Equal(t, mine.ID, theirs.ID, "both tenants number from 1: only the tenant tells them apart")

	var inMine, inTheirs, visible int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM ticket_status_changes`).Scan(&inMine))
	require.NoError(t, f.database.QueryRow(otherCtx, `SELECT count(*) FROM ticket_status_changes`).Scan(&inTheirs))
	require.NoError(t, f.database.Global().QueryRow(context.Background(),
		`SELECT count(*) FROM ticket_status_changes`).Scan(&visible))
	assert.Equal(t, 1, inMine)
	assert.Equal(t, 1, inTheirs)
	assert.Zero(t, visible, "no tenant, no rows")
}
