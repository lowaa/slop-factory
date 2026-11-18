package ticket_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Reviewing a change after it merged, against real Postgres: which done
// tickets are still waiting on a person's look, and the record of that look.

const mergedPR = "https://github.example/acme/repo/pull/7"

// merge claims ticketID as agentID and finishes it, as an agent that merged
// its own PR does.
func (f *fixture) merge(t *testing.T, agentID uuid.UUID, ticketID int64) *ticket.Ticket {
	t.Helper()
	_, err := f.svc.Claim(f.ctx, agentID, &ticketID)
	require.NoError(t, err)
	done, err := f.svc.Done(f.ctx, agentID, &ticket.DoneParams{Summary: "merged", PRURL: mergedPR})
	require.NoError(t, err)
	require.Equal(t, ticket.StatusDone, done.Status)
	return done
}

func (f *fixture) awaitingPostReview(t *testing.T) []int64 {
	t.Helper()
	tickets, err := f.svc.List(f.ctx, &ticket.ListFilter{AwaitingPostReview: true})
	require.NoError(t, err)
	ids := make([]int64, 0, len(tickets))
	for _, tk := range tickets {
		ids = append(ids, tk.ID)
	}
	return ids
}

func TestAwaitingPostReview_OnlyDoneTicketsNobodyApprovedOrReviewed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")

	selfMerged := f.merge(t, a, f.ticket(t, "Self-merged").ID)

	// Asked for a review nobody required, and got an approval: looked at.
	approved := f.ticket(t, "Approved anyway")
	_, err := f.svc.Claim(f.ctx, a, &approved.ID)
	require.NoError(t, err)
	f.review(t, a)
	f.approve(t, approved.ID)
	f.merge(t, a, approved.ID)

	// Required approval, and the human merged it themselves.
	gated := f.gated(t, "Gated")
	_, err = f.setStatus(t, gated.ID, ticket.StatusDone)
	require.NoError(t, err)

	cancelled := f.ticket(t, "Dropped")
	_, err = f.setStatus(t, cancelled.ID, ticket.StatusCancelled)
	require.NoError(t, err)

	f.ticket(t, "Still open")

	assert.Equal(t, []int64{selfMerged.ID}, f.awaitingPostReview(t))
}

func TestAwaitingPostReview_MostRecentlyMergedFirst(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	older := f.merge(t, a, f.ticket(t, "Filed first, merged last").ID)
	newer := f.merge(t, a, f.ticket(t, "Filed last, merged first").ID)
	// Close order opposite to filing order, so id order would get it wrong.
	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET closed_at = now() + interval '1 hour' WHERE id = $1`, older.ID)
	require.NoError(t, err)

	assert.Equal(t, []int64{older.ID, newer.ID}, f.awaitingPostReview(t))
}

func TestMarkPostReviewed_RecordsWhoAndTheComment(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	done := f.merge(t, a, f.ticket(t, "Self-merged").ID)

	marked, err := f.svc.MarkPostReviewed(f.ctx, done.ID, f.human, "  Naming is off in the handler; filed a follow-up.  ")
	require.NoError(t, err)

	require.NotNil(t, marked.PostReviewedAt)
	require.NotNil(t, marked.PostReviewedByUserID)
	assert.Equal(t, f.human, *marked.PostReviewedByUserID)
	assert.Equal(t, ticket.StatusDone, marked.Status, "bookkeeping only: the ticket stays done")
	assert.Empty(t, f.awaitingPostReview(t))

	comments, err := f.svc.ListComments(f.ctx, done.ID)
	require.NoError(t, err)
	last := comments[len(comments)-1]
	assert.Equal(t, "Naming is off in the handler; filed a follow-up.", last.Body)
	require.NotNil(t, last.AuthorUserID)
	assert.Equal(t, f.human, *last.AuthorUserID)
}

func TestMarkPostReviewed_WithoutACommentPostsNone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	done := f.merge(t, a, f.ticket(t, "Self-merged").ID)
	before, err := f.svc.ListComments(f.ctx, done.ID)
	require.NoError(t, err)

	_, err = f.svc.MarkPostReviewed(f.ctx, done.ID, f.human, "   ")
	require.NoError(t, err)

	after, err := f.svc.ListComments(f.ctx, done.ID)
	require.NoError(t, err)
	assert.Len(t, after, len(before))
}

func TestMarkPostReviewed_RefusesAndLeavesNoComment(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	open := f.ticket(t, "Not merged yet")
	done := f.merge(t, a, f.ticket(t, "Self-merged").ID)
	first, err := f.svc.MarkPostReviewed(f.ctx, done.ID, f.human, "")
	require.NoError(t, err)

	_, err = f.svc.MarkPostReviewed(f.ctx, open.ID, f.human, "looked")
	assert.ErrorIs(t, err, ticket.ErrNotDone)

	_, err = f.svc.MarkPostReviewed(f.ctx, done.ID, f.human, "looked again")
	assert.ErrorIs(t, err, ticket.ErrAlreadyPostReviewed)
	assert.Equal(t, first.PostReviewedAt, f.get(t, done.ID).PostReviewedAt, "the first mark stands")

	_, err = f.svc.MarkPostReviewed(f.ctx, 9999, f.human, "")
	assert.ErrorIs(t, err, ticket.ErrNotFound)

	for _, id := range []int64{open.ID, done.ID} {
		comments, err := f.svc.ListComments(f.ctx, id)
		require.NoError(t, err)
		for _, c := range comments {
			assert.NotContains(t, c.Body, "looked", "a refused mark leaves no comment on #%d", id)
		}
	}
}

// A ticket taken out of done will merge a different change, so it has to be
// looked at again once it is done again.
func TestMarkPostReviewed_ClearedWhenTheTicketLeavesDone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	done := f.merge(t, a, f.ticket(t, "Self-merged").ID)
	_, err := f.svc.MarkPostReviewed(f.ctx, done.ID, f.human, "")
	require.NoError(t, err)

	// Done to done is not leaving it.
	stays, err := f.setStatus(t, done.ID, ticket.StatusDone)
	require.NoError(t, err)
	assert.NotNil(t, stays.PostReviewedAt)

	reopened, err := f.setStatus(t, done.ID, ticket.StatusOpen)
	require.NoError(t, err)
	assert.Nil(t, reopened.PostReviewedAt)
	assert.Nil(t, reopened.PostReviewedByUserID)

	f.merge(t, a, done.ID)
	assert.Equal(t, []int64{done.ID}, f.awaitingPostReview(t))
}

// Tenant A can neither see tenant B's unreviewed #1 nor mark it.
func TestPostReview_DoesNotCrossTenants(t *testing.T) {
	t.Parallel()
	a, b := newTwoTenants(t)
	tb := b.ticket(t, "Tenant B's self-merged ticket")
	_, err := b.svc.Claim(b.ctx, b.agentID, &tb.ID)
	require.NoError(t, err)
	_, err = b.svc.Done(b.ctx, b.agentID, &ticket.DoneParams{PRURL: mergedPR})
	require.NoError(t, err)
	human := dbtest.NewUser(t, a.database)

	listed, err := a.svc.List(a.ctx, &ticket.ListFilter{AwaitingPostReview: true})
	require.NoError(t, err)
	assert.Empty(t, listed)

	_, err = a.svc.MarkPostReviewed(a.ctx, tb.ID, human, "")
	assert.ErrorIs(t, err, ticket.ErrNotFound)

	still, err := b.svc.List(b.ctx, &ticket.ListFilter{AwaitingPostReview: true})
	require.NoError(t, err)
	require.Len(t, still, 1, "tenant B's ticket is still waiting")
}

// setClosedAt pins when a ticket merged, so paging and date filters do not
// depend on the clock.
func (f *fixture) setClosedAt(t *testing.T, id int64, at string) {
	t.Helper()
	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET closed_at = $2::timestamptz WHERE id = $1`, id, at)
	require.NoError(t, err)
}

func pageIDs(p *ticket.Page) []int64 {
	ids := make([]int64, 0, len(p.Tickets))
	for _, tk := range p.Tickets {
		ids = append(ids, tk.ID)
	}
	return ids
}

// Pages walk the whole list once, in order, including tickets that share a
// closed_at (the id breaks the tie) and one with none (which sorts last).
func TestListPage_KeysetWalksEveryTicketOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	var all []int64
	for i := 0; i < 6; i++ {
		all = append(all, f.merge(t, a, f.ticket(t, fmt.Sprintf("T%d", i)).ID).ID)
	}
	f.setClosedAt(t, all[0], "2026-03-01T00:00:00Z")
	f.setClosedAt(t, all[1], "2026-03-03T00:00:00Z")
	f.setClosedAt(t, all[2], "2026-03-03T00:00:00Z")
	f.setClosedAt(t, all[3], "2026-03-02T00:00:00Z")
	// Two with none, so a page ends on one and the next resumes from a
	// cursor that has no time.
	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET closed_at = NULL WHERE id = ANY($1)`, []int64{all[4], all[5]})
	require.NoError(t, err)

	want := []int64{all[2], all[1], all[3], all[0], all[5], all[4]}
	var got []int64
	var after *ticket.PageCursor
	pages := 0
	for {
		page, err := f.svc.ListPage(f.ctx, &ticket.ListFilter{AwaitingPostReview: true, Limit: 2, After: after})
		require.NoError(t, err)
		got = append(got, pageIDs(page)...)
		pages++
		if page.Next == nil {
			break
		}
		// Through the wire form, as a client sends it back.
		after, err = ticket.DecodePageCursor(page.Next.Encode())
		require.NoError(t, err)
	}
	assert.Equal(t, want, got)
	assert.Equal(t, 3, pages)
}

func TestListPage_DefaultsThePageSize(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	for i := 0; i < ticket.DefaultPageSize+1; i++ {
		f.merge(t, a, f.ticket(t, fmt.Sprintf("T%d", i)).ID)
	}

	page, err := f.svc.ListPage(f.ctx, &ticket.ListFilter{AwaitingPostReview: true})
	require.NoError(t, err)
	assert.Len(t, page.Tickets, ticket.DefaultPageSize)
	assert.NotNil(t, page.Next)
}

func TestListPage_ExactMultipleHasNoNextPage(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	f.merge(t, a, f.ticket(t, "One").ID)
	f.merge(t, a, f.ticket(t, "Two").ID)

	page, err := f.svc.ListPage(f.ctx, &ticket.ListFilter{AwaitingPostReview: true, Limit: 2})
	require.NoError(t, err)
	assert.Len(t, page.Tickets, 2)
	assert.Nil(t, page.Next)
}

func TestListPage_FiltersByTextAndMergedDates(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	early := f.merge(t, a, f.ticket(t, "Fix widget").ID).ID
	mid := f.merge(t, a, f.ticket(t, "Fix gadget").ID).ID
	late := f.merge(t, a, f.ticket(t, "Fix widget again").ID).ID
	f.setClosedAt(t, early, "2026-03-01T10:00:00Z")
	f.setClosedAt(t, mid, "2026-03-05T23:59:59Z")
	f.setClosedAt(t, late, "2026-03-09T00:00:00Z")
	day := func(s string) *time.Time {
		v, err := time.Parse(time.DateOnly, s)
		require.NoError(t, err)
		return &v
	}
	list := func(flt ticket.ListFilter) []int64 {
		flt.AwaitingPostReview = true
		page, err := f.svc.ListPage(f.ctx, &flt)
		require.NoError(t, err)
		return pageIDs(page)
	}

	assert.Equal(t, []int64{late, early}, list(ticket.ListFilter{Text: "widget"}))
	assert.Equal(t, []int64{late, mid}, list(ticket.ListFilter{Closed: ticket.TimeRange{From: day("2026-03-05")}}))
	assert.Equal(t, []int64{mid, early}, list(ticket.ListFilter{Closed: ticket.TimeRange{Before: day("2026-03-06")}}))
	assert.Equal(t, []int64{mid}, list(ticket.ListFilter{Closed: ticket.TimeRange{From: day("2026-03-02"), Before: day("2026-03-06")}}))
	assert.Equal(t, []int64{early}, list(ticket.ListFilter{Text: "widget", Closed: ticket.TimeRange{Before: day("2026-03-06")}}))
}

func TestListPage_RejectsWhatItCannotPage(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, flt := range []ticket.ListFilter{
		{AwaitingPostReview: true, Limit: -1},
		{AwaitingPostReview: true, Limit: ticket.MaxPageSize + 1},
	} {
		_, err := f.svc.ListPage(f.ctx, &flt)
		assert.ErrorIs(t, err, ticket.ErrInvalidPage, "%+v", flt)
	}
	for _, bad := range []string{"!!", "", "bm9waXBl"} {
		_, err := ticket.DecodePageCursor(bad)
		assert.ErrorIs(t, err, ticket.ErrInvalidPage, "%q", bad)
	}
}
