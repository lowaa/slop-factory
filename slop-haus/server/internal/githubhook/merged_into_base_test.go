package githubhook_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// A merge of an unpromoted attempt's change into the ticket's base branch
// moved nothing on main: the attempt, and only it, goes to discussion.

// liveChange records a live change of the attempt, as change_open would.
func (f *fixture) liveChange(tenant *tenantFixture, ticketID int64, attempt int, number int, base string) {
	f.t.Helper()
	a, err := tenant.agents.Create(tenant.ctx, "opener-"+uuid.NewString()[:8])
	require.NoError(f.t, err)
	_, err = f.database.Exec(tenant.ctx, `
		INSERT INTO ticket_changes (ticket_id, attempt_number, host, owner, repo, number, head_branch, base_branch, draft, opened_by_agent_id)
		VALUES ($1, $2, 'github.com', 'acme', 'widgets', $3, $4, $5, true, $6)`,
		ticketID, attempt, number, "ticket-x-a2", base, a.ID)
	require.NoError(f.t, err)
	_, err = f.database.Exec(tenant.ctx, `UPDATE attempts SET status = 'review' WHERE ticket_id = $1 AND number = $2`, ticketID, attempt)
	require.NoError(f.t, err)
}

type changeState struct {
	Superseded, MergedIntoBase bool
}

func (f *fixture) changeState(tenant *tenantFixture, ticketID int64, number int) changeState {
	f.t.Helper()
	var c changeState
	require.NoError(f.t, f.database.QueryRow(tenant.ctx, `
		SELECT superseded_at IS NOT NULL, merged_into_base_at IS NOT NULL FROM ticket_changes WHERE ticket_id = $1 AND number = $2`,
		ticketID, number).Scan(&c.Superseded, &c.MergedIntoBase))
	return c
}

func (f *fixture) twoAttempts(shadow bool) (*tenantFixture, *ticket.Ticket, string) {
	f.t.Helper()
	reviewed := f.inReview(f.a, prURL)
	require.Equal(f.t, 2, f.addAttempt(f.a, reviewed.ID, secondPR, shadow))
	f.liveChange(f.a, reviewed.ID, 2, 13, ticket.UnpromotedBase(reviewed.ID))
	return f.a, reviewed, ticket.UnpromotedBase(reviewed.ID)
}

func TestReceive_AMergeIntoTheBaseBranchSendsOnlyThatAttemptToDiscussion(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	tenant, reviewed, base := f.twoAttempts(true)

	rec := f.deliver(delivery{app: ep, event: "pull_request", body: mergedInto(secondPR, base)})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "applied")
	ticketID, attempt := f.recorded(tenant)
	require.NotNil(t, ticketID)
	assert.Equal(t, reviewed.ID, *ticketID)
	require.NotNil(t, attempt)
	assert.Equal(t, 2, *attempt)

	assert.Equal(t, "1:review:true:-,2:discussion:false:-", f.attemptStates(tenant, reviewed.ID))
	assert.Equal(t, ticket.StatusDiscussion, f.status(tenant, reviewed.ID), "the ticket shows its attempts' most pressing status")
	assert.Equal(t, changeState{Superseded: true, MergedIntoBase: true}, f.changeState(tenant, reviewed.ID, 13))

	comments, err := tenant.tickets.ListComments(tenant.ctx, reviewed.ID)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	require.NotNil(t, comments[0].AttemptNumber)
	assert.EqualValues(t, 2, *comments[0].AttemptNumber)
	require.NotNil(t, comments[0].AuthorUserID, "the linked member who merged is credited")
	assert.Contains(t, comments[0].Body, "merged into the ticket's base branch "+base)
	assert.Contains(t, comments[0].Body, "Nothing reached main")
}

func TestReceive_AMergeIntoTheBaseBranchByANonMemberIsStillApplied(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	tenant, reviewed, base := f.twoAttempts(true)

	rec := f.deliver(delivery{app: ep, event: "pull_request", body: mergedIntoBy(secondPR, base, 4002, "merge-bot")})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "applied")
	assert.Equal(t, "1:review:true:-,2:discussion:false:-", f.attemptStates(tenant, reviewed.ID))
	assert.Equal(t, changeState{Superseded: true, MergedIntoBase: true}, f.changeState(tenant, reviewed.ID, 13))
	comments, err := tenant.tickets.ListComments(tenant.ctx, reviewed.ID)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Nil(t, comments[0].AuthorUserID, "no member is credited")
	assert.Contains(t, comments[0].Body, "GitHub user merge-bot (id 4002), not a member here")
}

func TestReceive_AMergeOfAnUnpromotedAttemptIntoAnotherBranchByANonMemberStaysIgnored(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	tenant, reviewed, _ := f.twoAttempts(false)
	before := f.attemptStates(tenant, reviewed.ID)

	rec := f.deliver(delivery{app: ep, event: "pull_request", body: mergedIntoBy(secondPR, "main", 4002, "merge-bot")})

	assert.Contains(t, rec.Body.String(), "not its base branch")
	assert.Equal(t, before, f.attemptStates(tenant, reviewed.ID))
	assert.Equal(t, changeState{}, f.changeState(tenant, reviewed.ID, 13))
}

func TestReceive_AMergeIntoTheBaseBranchIsAppliedOnce(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	_, reviewed, base := f.twoAttempts(false)

	require.Contains(t, f.deliver(delivery{app: ep, event: "pull_request", body: mergedInto(secondPR, base)}).Body.String(), "applied")
	again := f.deliver(delivery{app: ep, event: "pull_request", body: mergedInto(secondPR, base)})

	assert.Contains(t, again.Body.String(), "ignored")
	comments, err := f.a.tickets.ListComments(f.a.ctx, reviewed.ID)
	require.NoError(t, err)
	assert.Len(t, comments, 1)
}

func TestReceive_AMergeIntoAnotherBranchOfAnUnpromotedAttemptStaysIgnored(t *testing.T) {
	for _, base := range []string{"main", "release", "", "slop-haus-base-ticket-999999"} {
		t.Run(base, func(t *testing.T) {
			f := newFixture(t)
			ep := f.install(f.a)
			tenant, reviewed, _ := f.twoAttempts(false)
			before := f.attemptStates(tenant, reviewed.ID)

			rec := f.deliver(delivery{app: ep, event: "pull_request", body: mergedInto(secondPR, base)})

			assert.Contains(t, rec.Body.String(), "ignored")
			assert.Contains(t, rec.Body.String(), "not its base branch")
			assert.Equal(t, before, f.attemptStates(tenant, reviewed.ID))
			assert.Equal(t, changeState{}, f.changeState(tenant, reviewed.ID, 13))
			comments, err := tenant.tickets.ListComments(tenant.ctx, reviewed.ID)
			require.NoError(t, err)
			assert.Empty(t, comments)
		})
	}
}

func TestReceive_ACloseWithoutMergeOfAnUnpromotedAttemptStaysIgnored(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	tenant, reviewed, base := f.twoAttempts(false)
	before := f.attemptStates(tenant, reviewed.ID)

	rec := f.deliver(delivery{app: ep, event: "pull_request",
		body: `{"action":"closed","pull_request":{"html_url":"` + secondPR + `","merged":false,"base":{"ref":"` + base + `"}}}`})

	assert.Contains(t, rec.Body.String(), "ignored")
	assert.Equal(t, before, f.attemptStates(tenant, reviewed.ID))
	assert.Equal(t, changeState{}, f.changeState(tenant, reviewed.ID, 13))
}

func TestReceive_AMergeIntoTheBaseBranchOfAChangeThatIsNoLongerLiveIsIgnored(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	tenant, reviewed, base := f.twoAttempts(false)
	_, err := f.database.Exec(tenant.ctx, `UPDATE ticket_changes SET superseded_at = now() WHERE ticket_id = $1`, reviewed.ID)
	require.NoError(t, err)
	before := f.attemptStates(tenant, reviewed.ID)

	rec := f.deliver(delivery{app: ep, event: "pull_request", body: mergedInto(secondPR, base)})

	assert.Contains(t, rec.Body.String(), "ignored")
	assert.Equal(t, before, f.attemptStates(tenant, reviewed.ID))
}

func TestReceive_AMergeIntoTheBaseBranchRevokesOnlyThatAttemptsLease(t *testing.T) {
	f := newFixture(t)
	ep := f.install(f.a)
	tenant, reviewed, base := f.twoAttempts(false)
	for _, attempt := range []int{1, 2} {
		holder, err := tenant.agents.Create(tenant.ctx, "holder-"+uuid.NewString()[:8])
		require.NoError(t, err)
		_, err = f.database.Exec(tenant.ctx, `
			INSERT INTO leases (ticket_id, attempt_number, agent_id, expires_at) VALUES ($1, $2, $3, now() + interval '5 hours')`,
			reviewed.ID, attempt, holder.ID)
		require.NoError(t, err)
	}

	rec := f.deliver(delivery{app: ep, event: "pull_request", body: mergedInto(secondPR, base)})

	require.Contains(t, rec.Body.String(), "applied")
	var ended []string
	rows, err := f.database.Query(tenant.ctx, `SELECT attempt_number || ':' || (released_at IS NOT NULL)::text || ':' || coalesce(revoked_to, '-') FROM leases WHERE ticket_id = $1 AND expires_at > now() + interval '4 hours' ORDER BY attempt_number`, reviewed.ID)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		ended = append(ended, s)
	}
	assert.Equal(t, []string{"1:false:-", "2:true:discussion"}, ended)
}
