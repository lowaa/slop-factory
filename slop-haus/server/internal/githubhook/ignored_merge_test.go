package githubhook_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

const mergeSHA = "cccccccccccccccccccccccccccccccccccccccc"

func mergedBodyWithCommit(githubID int64) string {
	b, _ := json.Marshal(map[string]any{
		"action": "closed",
		"pull_request": map[string]any{
			"html_url": prURL, "merged": true, "merge_commit_sha": mergeSHA,
			"merged_by": map[string]any{"id": githubID, "login": fmt.Sprintf("user-%d", githubID)},
		},
	})
	return string(b)
}

type recordedMerge struct {
	ticketID int64
	attempt  int
	sha      string
	login    string
	githubID int64
	reason   string
}

func (f *fixture) ignoredMerges(tenant *tenantFixture) []recordedMerge {
	f.t.Helper()
	rows, err := f.database.Query(tenant.ctx, `SELECT ticket_id, attempt_number, merge_sha, merger_login, merger_github_id, reason FROM ignored_merges ORDER BY id`)
	require.NoError(f.t, err)
	defer rows.Close()
	var out []recordedMerge
	for rows.Next() {
		var m recordedMerge
		require.NoError(f.t, rows.Scan(&m.ticketID, &m.attempt, &m.sha, &m.login, &m.githubID, &m.reason))
		out = append(out, m)
	}
	require.NoError(f.t, rows.Err())
	return out
}

func TestReceive_AMergeByAnUnlinkedAccountIsRecordedAgainstTheTicket(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	reviewed := f.inReview(f.a, prURL)

	rec := f.deliver(delivery{app: ep, event: "pull_request", body: mergedBodyWithCommit(botGitHubID)})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "ignored: the merger")

	assert.Equal(t, ticket.StatusReview, f.status(f.a, reviewed.ID), "an unlinked merge never closes on its own")
	got := f.ignoredMerges(f.a)
	require.Len(t, got, 1)
	assert.Equal(t, recordedMerge{ticketID: reviewed.ID, attempt: 1, sha: mergeSHA, login: "user-2002", githubID: botGitHubID,
		reason: "the merger, GitHub user user-2002 (id 2002), is not a member here with that GitHub account linked"}, got[0])

	// GitHub sending the same merge again records nothing new.
	f.deliver(delivery{app: ep, event: "pull_request", body: mergedBodyWithCommit(botGitHubID)})
	assert.Len(t, f.ignoredMerges(f.a), 1)
}

func TestReceive_AMergeByAMemberRecordsNothing(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	reviewed := f.inReview(f.a, prURL)

	rec := f.deliver(delivery{app: ep, event: "pull_request", body: mergedBodyWithCommit(reviewerGitHubID)})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.Equal(t, ticket.StatusDone, f.status(f.a, reviewed.ID))
	assert.Empty(t, f.ignoredMerges(f.a))
}

func TestReceive_AMergeOfAPRNoTicketHasRecordsNothing(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)

	rec := f.deliver(delivery{app: ep, event: "pull_request", body: mergedBodyWithCommit(botGitHubID)})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.Empty(t, f.ignoredMerges(f.a))
}

// Another ignored reason, not only an unlinked merger: a linked member's
// merge of a ticket the rules will not close (sent back for changes).
func TestReceive_AMergeTheTicketRulesRefuseIsRecordedToo(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	reviewed := f.inReview(f.a, prURL)
	_, err := f.a.tickets.RequestChanges(f.a.ctx, reviewed.ID, f.a.member, "Again.")
	require.NoError(t, err)

	rec := f.deliver(delivery{app: ep, event: "pull_request", body: mergedBodyWithCommit(reviewerGitHubID)})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "ignored:")

	got := f.ignoredMerges(f.a)
	require.Len(t, got, 1)
	assert.Equal(t, reviewed.ID, got[0].ticketID)
	assert.Equal(t, int64(reviewerGitHubID), got[0].githubID)
}
