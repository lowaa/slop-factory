package ticket_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

const (
	mergedPRLink = "https://github.com/acme/app/pull/12"
	mergeCommit  = "cccccccccccccccccccccccccccccccccccccccc"
)

var closeMergedPR = codehost.ChangeRef{Host: prHost, Owner: "acme", Repo: "app", Number: 12}

// tenantOwnersFake answers from a list of (user, tenant) pairs; the rule
// itself is the user package's, tested there.
type tenantOwnersFake map[[2]uuid.UUID]bool

func (o tenantOwnersFake) IsTenantOwner(_ context.Context, userID, tenantID uuid.UUID) (bool, error) {
	return o[[2]uuid.UUID{userID, tenantID}], nil
}

type mergedRig struct {
	*fixture
	host   *codehosttest.Fake
	owners tenantOwnersFake
}

// newMergedRig is a fixture whose human owns the tenant and whose host shows
// acme/app#12 merged into main by an outsider, its head approved and green.
func newMergedRig(t *testing.T) *mergedRig {
	t.Helper()
	f := newFixture(t)
	host := codehosttest.NewLinkHost(prHost)
	host.StateOf = map[codehost.ChangeRef]*codehost.ChangeState{
		closeMergedPR: {Merged: true, Base: "main", HeadSHA: headOne, MergeSHA: mergeCommit, MergedBy: "merge-bot"},
	}
	host.ChecksOf = map[codehost.ChangeRef]*codehost.Checks{
		closeMergedPR: codehost.NewChecks(headOne, "main", []codehost.Check{{Name: "ci", State: codehost.CheckSuccess}}),
	}
	host.OnBranch = map[codehosttest.BranchCommit]bool{{Repo: acmeApp, Branch: "main", SHA: mergeCommit}: true}
	owners := tenantOwnersFake{{f.human, f.tenant}: true}
	ws := &placedIn{repos: []scope.WorkspaceRepo{{Path: "/", Remote: "github.com/acme/app", BaseBranch: "main"}}}
	f.svc = ticket.NewService(newStore(f.database), slog.DiscardHandler,
		ticket.WithCodeHosts(codehosttest.Registry(host)), ticket.WithWorkspace(ws), ticket.WithTenantOwners(owners))
	return &mergedRig{fixture: f, host: host, owners: owners}
}

// inReviewApproved is a ticket in review on the merged PR whose approval
// covers head.
func (r *mergedRig) inReviewApproved(t *testing.T, head string) (*ticket.Ticket, uuid.UUID) {
	t.Helper()
	agentID := r.agent(t, "builder-"+uuid.NewString()[:8])
	reviewed := r.inReviewWith(t, agentID, "Widgets", mergedPRLink)
	r.recordChange(t, reviewed.ID, agentID, head)
	_, err := r.database.Exec(r.ctx, `UPDATE attempts SET approved_at = now() WHERE ticket_id = $1 AND number = 1`, reviewed.ID)
	require.NoError(t, err)
	return reviewed, agentID
}

func (r *mergedRig) recordChange(t *testing.T, ticketID int64, agentID uuid.UUID, reviewedHead string) {
	t.Helper()
	_, err := r.database.Exec(r.ctx, `
		INSERT INTO ticket_changes (ticket_id, attempt_number, host, owner, repo, number, head_branch, base_branch, draft, opened_by_agent_id, reviewed_head_sha)
		VALUES ($1, 1, $2, 'acme', 'app', 12, 'ticket-1', 'main', false, $3, $4)`, ticketID, prHost, agentID, reviewedHead)
	require.NoError(t, err)
}

func (r *mergedRig) ignored(t *testing.T) {
	t.Helper()
	require.NoError(t, r.svc.RecordIgnoredMerge(r.ctx, &ticket.IgnoredMerge{
		PR: closeMergedPR, MergeSHA: mergeCommit, MergerLogin: "merge-bot", MergerGitHubID: 77,
		Reason: "the merger, GitHub user merge-bot (id 77), is not a member here with that GitHub account linked"}))
}

func TestCloseAsMerged_ClosesAsDoneAndRecordsWhoMergedAndWhoAccepted(t *testing.T) {
	t.Parallel()
	r := newMergedRig(t)
	tk, _ := r.inReviewApproved(t, headOne)
	r.ignored(t)

	got, err := r.svc.CloseAsMerged(r.ctx, tk.ID, r.human, false)

	require.NoError(t, err)
	require.NotNil(t, got.Ticket)
	assert.False(t, got.NeedsConfirmation)
	assert.Equal(t, ticket.StatusDone, r.status(t, tk.ID))
	comments := r.comments(t, tk.ID)
	last := comments[len(comments)-1]
	assert.Equal(t, "merged by merge-bot outside slop-haus at "+mergeCommit+"; accepted by "+r.human.String(), last.Body)
	require.NotNil(t, last.AuthorUserID)
	assert.Equal(t, r.human, *last.AuthorUserID, "the person is the actor")
}

// A ticket that predates the merge record: the host's word is enough.
func TestCloseAsMerged_WorksWithNoRecordedMerge(t *testing.T) {
	t.Parallel()
	r := newMergedRig(t)
	tk, _ := r.inReviewApproved(t, headOne)

	facts, err := r.svc.MergeFacts(r.ctx, tk.ID)
	require.NoError(t, err)
	assert.Nil(t, facts.Ignored)

	_, err = r.svc.CloseAsMerged(r.ctx, tk.ID, r.human, false)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDone, r.status(t, tk.ID))
}

func TestCloseAsMerged_EndsTheAgentsLeaseAndIntegrationLock(t *testing.T) {
	t.Parallel()
	r := newMergedRig(t)
	agentID := r.agent(t, "builder")
	tk := r.ticket(t, "Widgets", "a/")
	_, err := r.svc.Claim(r.ctx, agentID, &tk.ID)
	require.NoError(t, err)
	// The agent is part-way through its own merge when a person merges the
	// change on the host: it holds the ticket and the lock.
	_, err = r.database.Exec(r.ctx, `UPDATE attempts SET pr_url = $2 WHERE ticket_id = $1 AND number = 1`, tk.ID, mergedPRLink)
	require.NoError(t, err)
	r.recordChange(t, tk.ID, agentID, headOne)
	require.NoError(t, r.svc.AcquireIntegrationLock(r.ctx, agentID))
	state, err := r.svc.IntegrationLockStateFor(r.ctx, agentID)
	require.NoError(t, err)
	require.NotNil(t, state.Holder)

	got, err := r.svc.CloseAsMerged(r.ctx, tk.ID, r.human, true)

	require.NoError(t, err)
	require.NotNil(t, got.Ticket)
	assert.Equal(t, ticket.StatusDone, r.status(t, tk.ID))
	locks, err := r.svc.IntegrationLocks(r.ctx)
	require.NoError(t, err)
	for _, l := range locks {
		assert.Nil(t, l.Holder, "the lock %s went with the lease", l.Name)
	}
	var live int
	require.NoError(t, r.database.QueryRow(r.ctx, `SELECT count(*) FROM leases WHERE ticket_id = $1 AND released_at IS NULL`, tk.ID).Scan(&live))
	assert.Zero(t, live)
}

func TestCloseAsMerged_RefusesWhatTheHostDoesNotShowMergedIntoTheBase(t *testing.T) {
	t.Parallel()
	tests := map[string]*codehost.ChangeState{
		"merged into another branch": {Merged: true, Base: "release", HeadSHA: headOne, MergeSHA: mergeCommit, MergedBy: "merge-bot"},
		"not merged":                 {Merged: false, Base: "main", HeadSHA: headOne},
	}
	for name, state := range tests {
		t.Run(name, func(t *testing.T) {
			r := newMergedRig(t)
			tk, _ := r.inReviewApproved(t, headOne)
			r.ignored(t)
			r.host.StateOf[closeMergedPR] = state

			_, err := r.svc.CloseAsMerged(r.ctx, tk.ID, r.human, true)

			assert.ErrorIs(t, err, ticket.ErrNotMergedIntoBase)
			assert.Equal(t, ticket.StatusReview, r.status(t, tk.ID))
		})
	}
}

func TestCloseAsMerged_RefusesWhenTheHostCannotBeAsked(t *testing.T) {
	t.Parallel()
	r := newMergedRig(t)
	tk, _ := r.inReviewApproved(t, headOne)
	r.host.StateErr = codehost.ErrUnreachable

	_, err := r.svc.CloseAsMerged(r.ctx, tk.ID, r.human, true)

	assert.ErrorIs(t, err, ticket.ErrMergeUnverifiable)
	assert.Equal(t, ticket.StatusReview, r.status(t, tk.ID))
}

func TestCloseAsMerged_RefusesAChangeInAnotherRepository(t *testing.T) {
	t.Parallel()
	r := newMergedRig(t)
	agentID := r.agent(t, "builder")
	elsewhere := r.inReviewWith(t, agentID, "Elsewhere", "https://github.com/acme/other/pull/3")
	r.host.StateOf[codehost.ChangeRef{Host: prHost, Owner: "acme", Repo: "other", Number: 3}] = &codehost.ChangeState{Merged: true, Base: "main", HeadSHA: headOne}

	_, err := r.svc.CloseAsMerged(r.ctx, elsewhere.ID, r.human, true)

	assert.ErrorIs(t, err, ticket.ErrMergeUnverifiable)
}

func TestCloseAsMerged_OnlyTheTenantOwner(t *testing.T) {
	t.Parallel()
	r := newMergedRig(t)
	tk, _ := r.inReviewApproved(t, headOne)
	member := dbtest.NewUser(t, r.database)

	_, err := r.svc.CloseAsMerged(r.ctx, tk.ID, member, true)

	assert.ErrorIs(t, err, ticket.ErrNotTenantOwner)
	assert.Equal(t, ticket.StatusReview, r.status(t, tk.ID))
	assert.Empty(t, r.host.StateReads(), "a member's ask costs no host read")
}

func TestCloseAsMerged_AServiceWithNoOwnerCheckRefusesEveryone(t *testing.T) {
	t.Parallel()
	r := newMergedRig(t)
	tk, _ := r.inReviewApproved(t, headOne)
	r.svc = ticket.NewService(newStore(r.database), slog.DiscardHandler, ticket.WithCodeHosts(codehosttest.Registry(r.host)))

	_, err := r.svc.CloseAsMerged(r.ctx, tk.ID, r.human, true)

	assert.ErrorIs(t, err, ticket.ErrNotTenantOwner)
}

func TestCloseAsMerged_AnotherTenantsTicketIsNotFound(t *testing.T) {
	t.Parallel()
	r := newMergedRig(t)
	tk, _ := r.inReviewApproved(t, headOne)
	other := dbtest.NewTenant(t, r.database)
	r.owners[[2]uuid.UUID{r.human, other}] = true

	_, err := r.svc.CloseAsMerged(db.WithTenant(context.Background(), other), tk.ID, r.human, true)

	assert.ErrorIs(t, err, ticket.ErrNotFound)
	assert.Equal(t, ticket.StatusReview, r.status(t, tk.ID))
}

func TestCloseAsMerged_AFinishedTicketIsRefused(t *testing.T) {
	t.Parallel()
	r := newMergedRig(t)
	tk, _ := r.inReviewApproved(t, headOne)
	_, err := r.svc.CloseAsMerged(r.ctx, tk.ID, r.human, false)
	require.NoError(t, err)

	_, err = r.svc.CloseAsMerged(r.ctx, tk.ID, r.human, false)

	assert.ErrorIs(t, err, ticket.ErrTicketFinished)
}

func TestCloseAsMerged_AsksForConfirmationWhenTheMergedHeadIsNotGreenOrNotApproved(t *testing.T) {
	t.Parallel()
	failing := codehost.NewChecks(headOne, "main", []codehost.Check{{Name: "ci", State: codehost.CheckFailure}})
	pending := codehost.NewChecks(headOne, "main", []codehost.Check{{Name: "ci", State: codehost.CheckPending}})
	tests := map[string]struct {
		approved string
		checks   *codehost.Checks
		verdict  ticket.CheckVerdict
		mention  string
	}{
		"checks failed":         {approved: headOne, checks: failing, verdict: ticket.ChecksFailed, mention: "did not pass (failed)"},
		"checks not run":        {approved: headOne, checks: pending, verdict: ticket.ChecksNotRun, mention: "did not pass (not_run)"},
		"not the approved head": {approved: headTwo, checks: nil, verdict: ticket.ChecksPassed, mention: "is not the approved head"},
		"never approved":        {approved: "", checks: nil, verdict: ticket.ChecksPassed, mention: "was never approved"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r := newMergedRig(t)
			var tk *ticket.Ticket
			if tc.approved == "" {
				agentID := r.agent(t, "builder")
				tk = r.inReviewWith(t, agentID, "Widgets", mergedPRLink)
			} else {
				tk, _ = r.inReviewApproved(t, tc.approved)
			}
			if tc.checks != nil {
				r.host.ChecksOf[closeMergedPR] = tc.checks
			}

			asked, err := r.svc.CloseAsMerged(r.ctx, tk.ID, r.human, false)

			require.NoError(t, err)
			assert.True(t, asked.NeedsConfirmation)
			assert.Nil(t, asked.Ticket)
			require.Len(t, asked.Confirmations, 1)
			assert.Contains(t, asked.Confirmations[0], tc.mention)
			assert.Equal(t, tc.verdict, asked.Facts.Checks)
			assert.Equal(t, ticket.StatusReview, r.status(t, tk.ID), "nothing closes until it is confirmed")

			done, err := r.svc.CloseAsMerged(r.ctx, tk.ID, r.human, true)

			require.NoError(t, err)
			require.NotNil(t, done.Ticket)
			assert.Equal(t, ticket.StatusDone, r.status(t, tk.ID))
		})
	}
}

func TestMergeFacts_SaysWhatTheWebhookIgnoredAndWhatTheHostShows(t *testing.T) {
	t.Parallel()
	r := newMergedRig(t)
	tk, _ := r.inReviewApproved(t, headOne)
	r.ignored(t)

	facts, err := r.svc.MergeFacts(r.ctx, tk.ID)

	require.NoError(t, err)
	require.NotNil(t, facts.Ignored)
	assert.Equal(t, "merge-bot", facts.Ignored.MergerLogin)
	assert.Contains(t, facts.Ignored.Reason, "is not a member here")
	assert.Equal(t, "main", facts.Base)
	assert.True(t, facts.MergedIntoBase)
	assert.Equal(t, headOne, facts.MergedHead)
	assert.Equal(t, headOne, facts.ApprovedHead)
	assert.True(t, facts.HeadApproved)
	assert.Equal(t, ticket.ChecksPassed, facts.Checks)
	assert.True(t, facts.MergeOnBase)
	assert.Empty(t, facts.Confirmations())

	r.host.OnBranch[codehosttest.BranchCommit{Repo: acmeApp, Branch: "main", SHA: mergeCommit}] = false
	facts, err = r.svc.MergeFacts(r.ctx, tk.ID)
	require.NoError(t, err)
	assert.False(t, facts.MergeOnBase, "a merge commit the base does not contain is said so")

	r.host.OnBranch = nil
	_, err = r.svc.MergeFacts(r.ctx, tk.ID)
	assert.ErrorIs(t, err, ticket.ErrMergeUnverifiable, "a commit the host does not know is not read as not merged")
}

func TestCloseAsMerged_RecordsTheAuditEvent(t *testing.T) {
	t.Parallel()
	r := newMergedRig(t)
	counter := audittest.NewCounter()
	ws := &placedIn{repos: []scope.WorkspaceRepo{{Path: "/", Remote: "github.com/acme/app", BaseBranch: "main"}}}
	r.svc = ticket.NewService(newStore(r.database), slog.DiscardHandler,
		ticket.WithCodeHosts(codehosttest.Registry(r.host)), ticket.WithWorkspace(ws), ticket.WithTenantOwners(r.owners),
		ticket.WithEvents(audit.New(slog.DiscardHandler, counter)))
	tk, _ := r.inReviewApproved(t, headOne)

	_, err := r.svc.CloseAsMerged(r.ctx, tk.ID, r.human, false)

	require.NoError(t, err)
	assert.Equal(t, 1, counter.Count(audit.TicketClosedAsMerged))
}

func TestRecordIgnoredMerge_KeepsOneRecordPerChangeAndSkipsFinishedTickets(t *testing.T) {
	t.Parallel()
	r := newMergedRig(t)
	tk, _ := r.inReviewApproved(t, headOne)
	r.ignored(t)
	r.ignored(t)
	count := func() (n int) {
		require.NoError(t, r.database.QueryRow(r.ctx, `SELECT count(*) FROM ignored_merges WHERE ticket_id = $1`, tk.ID).Scan(&n))
		return n
	}
	assert.Equal(t, 1, count())

	_, err := r.svc.CloseAsMerged(r.ctx, tk.ID, r.human, false)
	require.NoError(t, err)
	_, err = r.database.Exec(r.ctx, `DELETE FROM ignored_merges`)
	require.NoError(t, err)
	r.ignored(t)
	assert.Zero(t, count(), "a finished ticket has nothing to explain")
}
