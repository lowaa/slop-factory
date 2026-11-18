package ticket_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Promotion moving after the PR is matched and before the review is applied
// must not make the review the new attempt's approval.
func TestApprovePR_RefusesWhenPromotionMovesAfterTheMatch(t *testing.T) {
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	reviewed := f.inReviewWith(t, a, "Widgets", githubPR)
	second, err := ticket.AddAttempt(f.ctx, f.database, reviewed.ID, false)
	require.NoError(t, err)
	// A PR of its own, not the matched one's.
	_, err = f.database.Exec(f.ctx, `UPDATE attempts SET pr_url = $3 WHERE ticket_id = $1 AND number = $2`, reviewed.ID, second, "https://github.com/acme/widgets/pull/13")
	require.NoError(t, err)
	review := f.prReview(t, reviewed.ID)
	review.SubmittedAt = time.Now().Add(time.Minute)
	restore := ticket.SetMatchBarrier(func() {
		_, err := f.database.Exec(f.ctx, `UPDATE attempts SET promoted_at = NULL WHERE ticket_id = $1 AND number = 1`, reviewed.ID)
		require.NoError(t, err)
		_, err = f.database.Exec(f.ctx, `UPDATE attempts SET promoted_at = now() WHERE ticket_id = $1 AND number = $2`, reviewed.ID, second)
		require.NoError(t, err)
	})
	defer restore()

	_, err = f.svc.ApprovePR(f.ctx, review)

	var notPromoted *ticket.AttemptNotPromotedError
	require.ErrorAs(t, err, &notPromoted)
	assert.Equal(t, ticket.AttemptRef{TicketID: reviewed.ID, Number: 1}, notPromoted.Attempt)
	var approved int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM attempts WHERE ticket_id = $1 AND approved_at IS NOT NULL`, reviewed.ID).Scan(&approved))
	assert.Zero(t, approved)
}

func TestApprovePR_RefusesAPRThatOnlyAnUnpromotedAttemptReports(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	reviewed := f.inReviewWith(t, a, "Widgets", "https://github.com/acme/widgets/pull/13")
	second, err := ticket.AddAttempt(f.ctx, f.database, reviewed.ID, false)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `UPDATE attempts SET pr_url = $3 WHERE ticket_id = $1 AND number = $2`, reviewed.ID, second, githubPR)
	require.NoError(t, err)

	_, err = f.svc.ApprovePR(f.ctx, f.prReview(t, reviewed.ID))

	require.ErrorIs(t, err, ticket.ErrAttemptNotPromoted)
	var notPromoted *ticket.AttemptNotPromotedError
	require.ErrorAs(t, err, &notPromoted)
	assert.Equal(t, ticket.AttemptRef{TicketID: reviewed.ID, Number: second}, notPromoted.Attempt)
	assert.Equal(t, ticket.StatusReview, f.status(t, reviewed.ID))
}

func TestApprovePR_IsAmbiguousWhenTwoAttemptsOfATicketReportThePR(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	reviewed := f.inReviewWith(t, a, "Widgets", githubPR)
	second, err := ticket.AddAttempt(f.ctx, f.database, reviewed.ID, false)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `UPDATE attempts SET pr_url = $3 WHERE ticket_id = $1 AND number = $2`, reviewed.ID, second, githubPR)
	require.NoError(t, err)

	_, err = f.svc.ApprovePR(f.ctx, f.prReview(t, reviewed.ID))

	assert.ErrorIs(t, err, ticket.ErrAmbiguousPR)
	assert.Equal(t, ticket.StatusReview, f.status(t, reviewed.ID))
}
