package githubhook_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/githubhook"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// A delivery is matched to the attempt whose PR it is, and acts only when
// that attempt is the ticket's promoted one.

const secondPR = "https://github.com/acme/widgets/pull/13"

// addAttempt gives the ticket another attempt, with prURL, shadow or not.
func (f *fixture) addAttempt(tenant *tenantFixture, ticketID int64, prURL string, shadow bool) int {
	f.t.Helper()
	var number int
	require.NoError(f.t, f.database.QueryRow(tenant.ctx, `
		UPDATE tickets SET next_attempt_number = next_attempt_number + 1
		 WHERE id = $1 RETURNING next_attempt_number - 1`, ticketID).Scan(&number))
	_, err := f.database.Exec(tenant.ctx, `
		INSERT INTO attempts (ticket_id, number, status, shadow, pr_url) VALUES ($1, $2, 'queued', $3, $4)`,
		ticketID, number, shadow, prURL)
	require.NoError(f.t, err)
	return number
}

// promote moves promotion to the attempt.
func (f *fixture) promote(tenant *tenantFixture, ticketID int64, number int) {
	f.t.Helper()
	_, err := f.database.Exec(tenant.ctx, `UPDATE attempts SET promoted_at = NULL WHERE ticket_id = $1 AND promoted_at IS NOT NULL`, ticketID)
	require.NoError(f.t, err)
	_, err = f.database.Exec(tenant.ctx, `UPDATE attempts SET promoted_at = now() WHERE ticket_id = $1 AND number = $2`, ticketID, number)
	require.NoError(f.t, err)
}

// recorded is the ticket and attempt the only delivery was recorded with.
func (f *fixture) recorded(tenant *tenantFixture) (ticketID *int64, attempt *int) {
	f.t.Helper()
	require.NoError(f.t, f.database.QueryRow(tenant.ctx,
		`SELECT ticket_id, attempt_number FROM github_deliveries`).Scan(&ticketID, &attempt))
	// The board's list of deliveries must say the same.
	listed := f.deliveries(tenant)
	require.Len(f.t, listed, 1)
	assert.Equal(f.t, ticketID, listed[0].TicketID)
	assert.Equal(f.t, attempt, listed[0].AttemptNumber)
	return ticketID, attempt
}

func reviewOf(pr, state string) string {
	b, _ := json.Marshal(map[string]any{
		"action": "submitted",
		"review": map[string]any{
			"state": state, "body": "x", "html_url": pr + "#pullrequestreview-9",
			"submitted_at": time.Now().Add(time.Minute).UTC().Format(time.RFC3339),
			"user":         map[string]any{"id": reviewerGitHubID, "login": "user"},
		},
		"pull_request": map[string]any{"html_url": pr},
	})
	return string(b)
}

func mergedOf(pr string) string { return mergedInto(pr, "main") }

func mergedInto(pr, base string) string {
	return mergedIntoBy(pr, base, reviewerGitHubID, "user")
}

func mergedIntoBy(pr, base string, id int64, login string) string {
	b, _ := json.Marshal(map[string]any{
		"action": "closed",
		"pull_request": map[string]any{
			"html_url": pr, "merged": true, "base": map[string]any{"ref": base},
			"merged_by": map[string]any{"id": id, "login": login},
		},
	})
	return string(b)
}

func (f *fixture) attemptStates(tenant *tenantFixture, id int64) string {
	f.t.Helper()
	var s string
	require.NoError(f.t, f.database.QueryRow(tenant.ctx, `
		SELECT string_agg(number || ':' || status || ':' || (promoted_at IS NOT NULL)::text || ':' || coalesce(approved_at::text, '-'), ',' ORDER BY number)
		  FROM attempts WHERE ticket_id = $1`, id).Scan(&s))
	return s
}

func TestReceive_AnUnpromotedAttemptsPRIsIgnoredAndRecordedWithItsAttempt(t *testing.T) {
	for _, tc := range []struct {
		name, event, body string
		shadow            bool
		detail            string
	}{
		{"approval", "pull_request_review", reviewOf(secondPR, "approved"), false, "attempt 2 is not promoted; promoting it is the approval"},
		{"changes requested", "pull_request_review", reviewOf(secondPR, "changes_requested"), false, "attempt 2 is not promoted; promoting it is the approval"},
		{"merge into main", "pull_request", mergedOf(secondPR), false, `attempt 2 is not promoted, and the merge was into "main", not its base branch`},
		{"shadow's approval", "pull_request_review", reviewOf(secondPR, "approved"), true, "attempt 2 is not promoted; promoting it is the approval"},
		{"shadow's merge into main", "pull_request", mergedOf(secondPR), true, `attempt 2 is not promoted, and the merge was into "main", not its base branch`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			ep := f.install(f.a)
			reviewed := f.inReview(f.a, prURL)
			second := f.addAttempt(f.a, reviewed.ID, secondPR, tc.shadow)
			require.Equal(t, 2, second)
			before := f.attemptStates(f.a, reviewed.ID)

			rec := f.deliver(delivery{app: ep, event: tc.event, body: tc.body})

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "ignored: #"+fmt.Sprint(reviewed.ID)+" "+tc.detail)
			ticketID, attempt := f.recorded(f.a)
			require.NotNil(t, ticketID)
			assert.Equal(t, reviewed.ID, *ticketID)
			require.NotNil(t, attempt)
			assert.Equal(t, 2, *attempt)
			assert.Equal(t, ticket.StatusReview, f.status(f.a, reviewed.ID))
			assert.Equal(t, before, f.attemptStates(f.a, reviewed.ID))
			comments, err := f.a.tickets.ListComments(f.a.ctx, reviewed.ID)
			require.NoError(t, err)
			assert.Empty(t, comments)
		})
	}
}

func TestReceive_ThePromotedAttemptsPRIsActedOnAndRecordedWithItsAttempt(t *testing.T) {
	for _, tc := range []struct {
		name, event, body string
		status            ticket.Status
	}{
		{"approval", "pull_request_review", reviewOf(prURL, "approved"), ticket.StatusOpen},
		{"changes requested", "pull_request_review", reviewOf(prURL, "changes_requested"), ticket.StatusOpen},
		{"merge", "pull_request", mergedOf(prURL), ticket.StatusDone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			ep := f.install(f.a)
			reviewed := f.inReview(f.a, prURL)
			f.addAttempt(f.a, reviewed.ID, secondPR, false)

			rec := f.deliver(delivery{app: ep, event: tc.event, body: tc.body})

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "applied")
			ticketID, attempt := f.recorded(f.a)
			require.NotNil(t, ticketID)
			assert.Equal(t, reviewed.ID, *ticketID)
			require.NotNil(t, attempt)
			assert.Equal(t, 1, *attempt)
			assert.Equal(t, tc.status, f.status(f.a, reviewed.ID))
		})
	}
}

// Promotion can move after the PR is matched and before the delivery is
// recorded; the delivery is still the matched attempt's.
func TestReceive_TheRecordedAttemptIsTheMatchedOneWhenPromotionMovesBeforeTheFinish(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	reviewed := f.inReview(f.a, prURL)
	second := f.addAttempt(f.a, reviewed.ID, secondPR, false)
	restore := githubhook.SetFinishBarrier(func() { f.promote(f.a, reviewed.ID, second) })
	defer restore()

	rec := f.deliver(delivery{app: ep, event: "pull_request_review", body: reviewOf(prURL, "approved")})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	_, attempt := f.recorded(f.a)
	require.NotNil(t, attempt)
	assert.Equal(t, 1, *attempt, "the promoted attempt at finish is attempt 2")
}

func TestReceive_APROnTwoTicketsIsAmbiguousWhenAnAttemptOfOneIsNotPromoted(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	first := f.inReview(f.a, prURL)
	other := f.inReview(f.a, "https://github.com/acme/widgets/pull/99")
	f.addAttempt(f.a, other.ID, prURL, false)

	rec := f.deliver(delivery{app: ep, event: "pull_request_review", body: reviewOf(prURL, "approved")})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "more than one ticket has this PR")
	ticketID, attempt := f.recorded(f.a)
	assert.Nil(t, ticketID)
	assert.Nil(t, attempt)
	assert.Equal(t, ticket.StatusReview, f.status(f.a, first.ID))
}
