package ticket_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func (f *fixture) detail(t *testing.T, id int64) ticket.TicketBody {
	t.Helper()
	rec := send(t, f.boardMux(), http.MethodGet, fmt.Sprintf("/api/tickets/%d", id), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return decodeTicket(t, rec.Body.Bytes())
}

func TestGetTicket_ListsTheOneAttempt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Single", "a/")

	got := f.detail(t, created.ID).Attempts
	require.Len(t, got, 1)
	assert.Equal(t, 1, got[0].Number)
	assert.Equal(t, "queued", got[0].Status)
	assert.True(t, got[0].Promoted)
	assert.NotNil(t, got[0].PromotedAt)
	assert.Nil(t, got[0].Holder)
	assert.Nil(t, got[0].Variant)
	assert.Nil(t, got[0].PRURL)

	holder := f.agent(t, "holder")
	_, err := f.svc.Claim(f.ctx, holder, &created.ID)
	require.NoError(t, err)
	got = f.detail(t, created.ID).Attempts
	require.Len(t, got, 1)
	assert.Equal(t, "in_progress", got[0].Status)
	require.NotNil(t, got[0].Holder)
	assert.Equal(t, "holder", got[0].Holder.Name)
	assert.Equal(t, holder.String(), got[0].Holder.AgentID)
}

func TestGetTicket_ListsSeveralAttemptsByNumberWithOnePromoted(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Several", "a/")
	f.setAttempt(t, created.ID, "pr_url = $2", attemptPR)
	// Inserted out of order, so the ordering is the query's.
	for _, n := range []int{4, 2, 3} {
		_, err := f.database.Exec(f.ctx,
			`INSERT INTO attempts (ticket_id, number, status) VALUES ($1, $2, 'queued')`, created.ID, n)
		require.NoError(t, err)
	}

	got := f.detail(t, created.ID).Attempts
	require.Len(t, got, 4)
	var numbers []int
	var promoted []int
	for _, a := range got {
		numbers = append(numbers, a.Number)
		if a.Promoted {
			promoted = append(promoted, a.Number)
		}
	}
	assert.Equal(t, []int{1, 2, 3, 4}, numbers)
	assert.Equal(t, []int{1}, promoted)
	require.NotNil(t, got[0].PRURL)
	assert.Equal(t, attemptPR, *got[0].PRURL)
	assert.Nil(t, got[1].PRURL, "another attempt's PR is its own")
}

func TestGetTicket_HolderIsTheLiveLeaseOfThatAttemptOnly(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Leased", "a/")
	holder := f.agent(t, "holder")
	_, err := f.svc.Claim(f.ctx, holder, &created.ID)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `INSERT INTO attempts (ticket_id, number, status) VALUES ($1, 2, 'queued')`, created.ID)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `UPDATE leases SET attempt_number = 2 WHERE agent_id = $1`, holder)
	require.NoError(t, err)

	got := f.detail(t, created.ID).Attempts
	require.Len(t, got, 2)
	assert.Nil(t, got[0].Holder, "the lease is on attempt 2")
	require.NotNil(t, got[1].Holder)
	assert.Equal(t, holder.String(), got[1].Holder.AgentID)

	f.expire(t, holder)
	got = f.detail(t, created.ID).Attempts
	assert.Nil(t, got[1].Holder, "a lapsed lease holds nothing")
}

func TestGetTicket_ReportsAnAttemptsReviewAndPromotion(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Reviewed", "a/")
	f.setAttempt(t, created.ID, "approved_at = now(), approved_by_user_id = $2, review_started_at = now(), promoted_by_user_id = $2", f.human)

	got := f.detail(t, created.ID).Attempts
	require.Len(t, got, 1)
	require.NotNil(t, got[0].ApprovedAt)
	require.NotNil(t, got[0].ApprovedByUserID)
	assert.Equal(t, f.human.String(), *got[0].ApprovedByUserID)
	require.NotNil(t, got[0].PromotedByUserID)
	assert.Equal(t, f.human.String(), *got[0].PromotedByUserID)
	assert.NotNil(t, got[0].ReviewStartedAt)
	assert.Nil(t, got[0].ChangesRequestedAt)
}

func TestGetTicket_ListsAttemptsOfATicketWithNonePromoted(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Unpromoted", "a/")
	f.setAttempt(t, created.ID, "promoted_at = NULL")

	got := f.detail(t, created.ID).Attempts
	require.Len(t, got, 1)
	assert.False(t, got[0].Promoted)
	assert.Nil(t, got[0].PromotedAt)
}

func TestListTickets_LeavesAttemptsOut(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.ticket(t, "Listed", "a/")

	rec := send(t, f.boardMux(), http.MethodGet, "/api/tickets", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), `"attempts"`)
}

func TestGetTicket_ReportsEachAttemptsStartedRevisionAndShadow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Started", "a/")
	f.setAttempt(t, created.ID, "started_revision = 3")
	_, err := f.database.Exec(f.ctx,
		`INSERT INTO attempts (ticket_id, number, status, shadow, started_revision) VALUES ($1, 2, 'in_progress', true, 5)`, created.ID)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `INSERT INTO attempts (ticket_id, number, status) VALUES ($1, 3, 'queued')`, created.ID)
	require.NoError(t, err)

	got := f.detail(t, created.ID).Attempts
	require.Len(t, got, 3)
	require.NotNil(t, got[0].StartedRevision)
	assert.Equal(t, 3, *got[0].StartedRevision)
	assert.False(t, got[0].Shadow)
	require.NotNil(t, got[1].StartedRevision)
	assert.Equal(t, 5, *got[1].StartedRevision)
	assert.True(t, got[1].Shadow)
	assert.Nil(t, got[2].StartedRevision, "an attempt that has not started has no revision")
	assert.False(t, got[2].Shadow)
}
