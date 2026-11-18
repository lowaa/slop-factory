package change_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestOpenChecksMerge_ThroughTheFake(t *testing.T) {
	f := newFixture(t)
	h := f.hold(true)

	// Draft first: it cannot merge, and neither can anything before approval.
	f.open(h, true)
	f.lock(h)
	_, err := f.merge(h, "")
	require.ErrorIs(t, err, change.ErrNotApproved)
	f.handOverAndApprove(h)
	_, err = f.merge(h, "")
	require.ErrorIs(t, err, change.ErrDraft)

	f.open(h, false)
	report, err := f.svc.Checks(f.ctx, f.tenant, h.agent, headA)
	require.NoError(t, err)
	assert.Equal(t, "https://git.example/acme/app/pull/7", report.URL)
	assert.Equal(t, headA, report.HeadSHA)
	assert.True(t, report.Passed)
	require.NotNil(t, report.ForExpectedHead)
	assert.True(t, *report.ForExpectedHead)

	merged, err := f.merge(h, headA)

	require.NoError(t, err)
	assert.Equal(t, "mergecommit", merged.MergeSHA)
	merges := f.host.Merges()
	require.Len(t, merges, 1)
	req := merges[0].Merge
	assert.Equal(t, f.tenant, merges[0].Tenant)
	assert.Equal(t, headA, req.HeadSHA, "the merge is pinned to the head the checks were read for")
	assert.Equal(t, "Merge "+f.branchOf(h)+": Add the thing", req.Subject)
	assert.Contains(t, req.Body, "Approved by: Alex Approver")
	assert.Contains(t, req.Body, "Agent: builder-1")

	rec, err := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	require.NotNil(t, rec.MergedAt)
	assert.Equal(t, "mergecommit", rec.MergeSHA)
	assert.Equal(t, &h.agent, rec.MergedBy)
	comments, err := f.tickets.ListComments(f.ctx, h.ticket.ID)
	require.NoError(t, err)
	require.Len(t, comments, 2)
	assert.Contains(t, comments[0].Body, "Handed over for review at commit "+headA)
	assert.Contains(t, comments[1].Body, "mergecommit")

	lines := f.log.audited(t, audit.ChangeMerged)
	require.Len(t, lines, 1)
	assert.Equal(t, h.agent.String(), lines[0]["agent_id"])
	assert.Equal(t, f.approver.String(), lines[0]["approved_by_user_id"])
	assert.Equal(t, headA, lines[0]["head_sha"])

	_, err = f.merge(h, "")
	require.ErrorIs(t, err, ticket.ErrNoLease, "the merge ended the lease, so there is nothing to merge twice")
	assert.Len(t, f.host.Merges(), 1)
}

// Each condition of the gate, alone, refuses the merge, and the host is
// never asked to merge.
func TestMerge_EachGateConditionRefuses(t *testing.T) {
	tests := map[string]struct {
		arrange func(f *fixture, h *holder)
		want    error
		reason  string
	}{
		"the integration lock is not held": {
			arrange: func(f *fixture, h *holder) { require.NoError(t, f.tickets.ReleaseIntegrationLock(f.ctx, h.agent)) },
			want:    change.ErrLockNotHeld, reason: "lock_not_held",
		},
		"approval is required and not given": {
			arrange: func(f *fixture, h *holder) {
				_, err := f.database.Exec(f.ctx, `UPDATE tickets SET requires_approval = true WHERE id = $1`, h.ticket.ID)
				require.NoError(t, err)
				_, err = f.database.Exec(f.ctx, `UPDATE attempts SET approved_at = NULL WHERE ticket_id = $1 AND number = 1`, h.ticket.ID)
				require.NoError(t, err)
			},
			want: change.ErrNotApproved, reason: "not_approved",
		},
		"the ticket needs discussion": {
			arrange: func(f *fixture, h *holder) {
				_, err := f.database.Exec(f.ctx, `UPDATE tickets SET needs_discussion = true WHERE id = $1`, h.ticket.ID)
				require.NoError(t, err)
			},
			want: ticket.ErrNeedsDiscussion, reason: "needs_discussion",
		},
		"the change is a draft": {
			arrange: func(f *fixture, h *holder) {
				_, err := f.database.Exec(f.ctx, `UPDATE ticket_changes SET draft = true`)
				require.NoError(t, err)
			},
			want: change.ErrDraft, reason: "draft",
		},
		"the change is in another repo than the ticket's scopes": {
			arrange: func(f *fixture, h *holder) { f.ws.set(placedIn(hostName + "/acme/other")) },
			want:    change.ErrWrongRepo, reason: "wrong_repo",
		},
		"the manifest no longer places the ticket": {
			arrange: func(f *fixture, h *holder) { f.ws.set(&ticket.Placement{}) },
			want:    change.ErrNoRepo, reason: "no_repo",
		},
		"the checks failed": {
			arrange: func(f *fixture, h *holder) {
				f.setChecks(headA, codehost.Check{Name: "ci/build", State: codehost.CheckSuccess}, codehost.Check{Name: "ci/test", State: codehost.CheckFailure})
			},
			want: change.ErrChecksNotPassed, reason: "checks_not_passed",
		},
		"the checks are still running": {
			arrange: func(f *fixture, h *holder) {
				f.setChecks(headA, codehost.Check{Name: "ci/build", State: codehost.CheckSuccess}, codehost.Check{Name: "ci/test", State: codehost.CheckPending})
			},
			want: change.ErrChecksNotPassed, reason: "checks_not_passed",
		},
		"the head has no checks at all": {
			arrange: func(f *fixture, h *holder) { f.setChecks(headA) },
			want:    change.ErrChecksNotPassed, reason: "checks_not_passed",
		},
		"every check is neutral": {
			arrange: func(f *fixture, h *holder) {
				f.setChecks(headA, codehost.Check{Name: "ci/skipped", State: codehost.CheckNeutral})
			},
			want: change.ErrChecksNotPassed, reason: "checks_not_passed",
		},
		"the host names no head": {
			arrange: func(f *fixture, h *holder) {
				f.setChecks("", codehost.Check{Name: "ci/build", State: codehost.CheckSuccess})
			},
			want: change.ErrChecksNotPassed, reason: "checks_not_passed",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			h := f.hold(true)
			f.open(h, false)
			f.lock(h)
			f.approve(h)
			tc.arrange(f, h)

			_, err := f.merge(h, "")

			require.ErrorIs(t, err, tc.want)
			assert.Empty(t, f.host.Merges(), "the host was not asked to merge")
			lines := f.log.audited(t, audit.ChangeMergeRefused)
			require.Len(t, lines, 1)
			assert.Equal(t, tc.reason, lines[0]["reason"])
			assert.Equal(t, h.agent.String(), lines[0]["agent_id"])
			assert.Empty(t, f.log.audited(t, audit.ChangeMerged))
		})
	}
}

func TestMerge_ANoChecksHeadSaysSoToTheAgent(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.open(h, false)
	f.lock(h)
	f.setChecks(headA)

	_, err := f.merge(h, "")

	require.ErrorIs(t, err, change.ErrChecksNotPassed)
	assert.Contains(t, err.Error(), "no checks is not a pass")
}

func TestMerge_AChangeThatWasOnlyReportedIsNotMerged(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.lock(h)
	// What the old flow leaves: a pr_url the agent reported, no change
	// slop-haus opened.
	_, err := f.database.Exec(f.ctx, `UPDATE attempts SET pr_url = 'https://git.example/acme/app/pull/7' WHERE ticket_id = $1 AND number = 1`, h.ticket.ID)
	require.NoError(t, err)

	_, err = f.merge(h, "")

	require.ErrorIs(t, err, change.ErrNoChange)
	assert.Empty(t, f.host.Merges())
	assert.Empty(t, f.host.Checked())
	assert.Len(t, f.log.audited(t, audit.ChangeMergeRefused), 1)
}

func TestMerge_ASupersededChangeIsNotMerged(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.open(h, false)
	f.recordUnderOldName(h)
	f.lock(h)
	f.host.Opened = codehost.ChangeRef{Host: hostName, Owner: "acme", Repo: "app", Number: 8}
	f.host.ChecksOf = map[codehost.ChangeRef]*codehost.Checks{f.host.Opened: codehost.NewChecks(headB, "main", []codehost.Check{{Name: "ci", State: codehost.CheckSuccess}})}
	f.host.CompareOf = map[codehost.ChangeRef]*codehost.BaseComparison{f.host.Opened: {Base: "main", ContainsTip: true}}
	_, err := f.svc.Open(f.ctx, f.tenant, h.agent, &change.OpenInput{Branch: f.branchOf(h), Title: "t"})
	require.NoError(t, err)

	_, err = f.merge(h, "")

	require.NoError(t, err)
	merges := f.host.Merges()
	require.Len(t, merges, 1)
	assert.Equal(t, int64(8), merges[0].Change.Number, "only the latest change counts")
}

func TestMerge_RefusesAHeadOtherThanTheOneNamed(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.open(h, false)
	f.lock(h)

	_, err := f.merge(h, headB)

	require.ErrorIs(t, err, change.ErrHeadMismatch)
	assert.Empty(t, f.host.Merges())
	assert.Equal(t, "head_mismatch", f.log.audited(t, audit.ChangeMergeRefused)[0]["reason"])
}

// The head moves between the checks being read and the merge, which the
// fake plays by refusing the merge as the host does for a stale sha: nothing
// merges, and the agent is told to read the checks again. No timing is
// involved: the push happens inside the checks call, after it answered.
func TestMerge_RefusesWhenTheHeadMovedAfterTheChecksWereRead(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.open(h, false)
	f.lock(h)
	f.host.AfterChecks = func() { f.host.MergeErr = codehost.ErrHeadMoved }

	_, err := f.merge(h, "")

	require.ErrorIs(t, err, change.ErrHeadMoved)
	merges := f.host.Merges()
	require.Len(t, merges, 1, "the merge was attempted, for the head the checks were read on")
	assert.Equal(t, headA, merges[0].Merge.HeadSHA)
	rec, cerr := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, cerr)
	assert.Nil(t, rec.MergedAt, "nothing is recorded as merged")
	assert.Len(t, f.log.audited(t, audit.ChangeOperationFailed), 1)
	assert.Empty(t, f.log.audited(t, audit.ChangeMerged))

	// A later attempt, with the head's own checks, goes through.
	f.host.AfterChecks, f.host.MergeErr = nil, nil
	f.setChecks(headB, codehost.Check{Name: "ci/build", State: codehost.CheckSuccess})
	_, err = f.merge(h, "")
	require.NoError(t, err)
	assert.Equal(t, headB, f.host.Merges()[1].Merge.HeadSHA)
}

func TestMerge_AHostRefusalIsPassedOnAndRecordsNothing(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.open(h, false)
	f.lock(h)
	f.host.MergeErr = codehost.ErrNotMergeable

	_, err := f.merge(h, "")

	require.ErrorIs(t, err, codehost.ErrNotMergeable)
	rec, cerr := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, cerr)
	assert.Nil(t, rec.MergedAt)
}

func TestChecks_ReportTheHeadAndWhetherItIsTheOneNamed(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.open(h, false)
	f.setChecks(headA, codehost.Check{Name: "ci/build", State: codehost.CheckSuccess}, codehost.Check{Name: "ci/test", State: codehost.CheckPending})

	report, err := f.svc.Checks(f.ctx, f.tenant, h.agent, headB)

	require.NoError(t, err)
	assert.Equal(t, headA, report.HeadSHA)
	assert.Equal(t, "pending", report.State)
	assert.False(t, report.Passed)
	assert.Equal(t, []change.CheckLine{{Name: "ci/build", State: "success"}, {Name: "ci/test", State: "pending"}}, report.Checks)
	require.NotNil(t, report.ForExpectedHead)
	assert.False(t, *report.ForExpectedHead, "the checks are for another head than the one named")
	assert.Len(t, f.log.audited(t, audit.ChangeChecksRead), 1)
	assert.Equal(t, f.tenant, f.host.Checked()[0].Tenant)

	unnamed, err := f.svc.Checks(f.ctx, f.tenant, h.agent, "")
	require.NoError(t, err)
	assert.Nil(t, unnamed.ForExpectedHead)
}

func TestChecks_NeedAChangeSlophausOpened(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)

	_, err := f.svc.Checks(f.ctx, f.tenant, h.agent, "")

	require.ErrorIs(t, err, change.ErrNoChange)
}

func TestMerge_TheLockMustBeTheCallersOwn(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.open(h, false)
	other := f.hold2()
	f.lock(other)

	_, err := f.merge(h, "")

	require.ErrorIs(t, err, change.ErrLockNotHeld, "another agent holding the lock is not the caller holding it")
	assert.Empty(t, f.host.Merges())
}

func TestMerge_AnotherTenantsContextReachesNothing(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.open(h, false)
	f.lock(h)
	checkedAtOpen := len(f.host.Checked())
	otherTenant := dbtest.NewTenant(t, f.database)
	otherCtx := db.WithTenant(context.Background(), otherTenant)

	_, err := f.svc.Merge(otherCtx, otherTenant, h.agent, &change.MergeInput{})
	require.ErrorIs(t, err, ticket.ErrNoLease, "the agent holds nothing in that tenant")
	_, err = f.svc.Checks(otherCtx, otherTenant, h.agent, "")
	require.ErrorIs(t, err, ticket.ErrNoLease)
	_, err = f.svc.Merge(otherCtx, f.tenant, h.agent, &change.MergeInput{})
	require.Error(t, err, "a tenant argument that is not the context's is refused")

	assert.Empty(t, f.host.Merges())
	assert.Len(t, f.host.Checked(), checkedAtOpen, "the foreign calls read nothing more")
}

// The lock is taken away by a person while the checks are being read. The
// release happens inside the checks call, after it answered, so no timing is
// involved.
func TestMerge_RefusesWhenTheLockIsLostWhileTheChecksWereRead(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.open(h, false)
	f.lock(h)
	f.host.AfterChecks = func() { require.NoError(t, f.tickets.ReleaseIntegrationLock(f.ctx, h.agent)) }

	_, err := f.merge(h, "")

	require.ErrorIs(t, err, change.ErrLockNotHeld)
	assert.Empty(t, f.host.Merges())
	assert.Equal(t, "before_merge", f.log.audited(t, audit.ChangeMergeRefused)[0]["stage"])
}

func TestMerge_RefusesWhenApprovalIsTakenAwayWhileTheChecksWereRead(t *testing.T) {
	f := newFixture(t)
	h := f.hold(true)
	f.approve(h)
	f.open(h, false)
	f.lock(h)
	f.host.AfterChecks = func() {
		_, err := f.database.Exec(f.ctx, `UPDATE attempts SET approved_at = NULL, approved_by_user_id = NULL WHERE ticket_id = $1 AND number = 1`, h.ticket.ID)
		require.NoError(t, err)
	}

	_, err := f.merge(h, "")

	require.ErrorIs(t, err, change.ErrNotApproved)
	assert.Empty(t, f.host.Merges())
}

// An approval is of the change as it stands: replacing the approved change
// with another one drops it, so the new, unreviewed change cannot be merged
// on the old one's approval.
func TestMerge_ANewChangeNeedsANewApproval(t *testing.T) {
	f := newFixture(t)
	h := f.hold(true)
	f.open(h, false)
	f.approve(h)
	f.recordUnderOldName(h)
	f.lock(h)
	f.host.Opened = codehost.ChangeRef{Host: hostName, Owner: "acme", Repo: "app", Number: 8}
	f.host.ChecksOf = map[codehost.ChangeRef]*codehost.Checks{f.host.Opened: codehost.NewChecks(headB, "main", []codehost.Check{{Name: "ci", State: codehost.CheckSuccess}})}
	f.host.CompareOf = map[codehost.ChangeRef]*codehost.BaseComparison{f.host.Opened: {Base: "main", ContainsTip: true}}

	_, err := f.svc.Open(f.ctx, f.tenant, h.agent, &change.OpenInput{Branch: f.branchOf(h), Title: "t"})
	require.NoError(t, err)
	_, err = f.merge(h, "")

	require.ErrorIs(t, err, change.ErrNotApproved)
	assert.Empty(t, f.host.Merges())
	got, err := f.tickets.Get(f.ctx, h.ticket.ID)
	require.NoError(t, err)
	assert.Nil(t, got.ApprovedAt)
	assert.Nil(t, got.ApprovedByUserID)
}

// Opening the same branch again is not a new change, so it keeps the
// approval.
func TestOpen_TheSameBranchAgainKeepsTheApproval(t *testing.T) {
	f := newFixture(t)
	h := f.hold(true)
	f.open(h, false)
	f.approve(h)

	f.open(h, false)

	got, err := f.tickets.Get(f.ctx, h.ticket.ID)
	require.NoError(t, err)
	assert.NotNil(t, got.ApprovedAt)
}

func TestMerge_AMergeAlreadyInFlightRefusesASecondAndTheClaimEnds(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.open(h, false)
	f.lock(h)
	rec, err := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	claim, err := f.store.BeginMerge(f.ctx, rec.ID)
	require.NoError(t, err)
	require.NotNil(t, claim)

	_, err = f.merge(h, "")
	require.ErrorIs(t, err, change.ErrMergeInFlight)
	assert.Empty(t, f.host.Merges(), "the host is not asked twice for one merge")

	require.NoError(t, f.store.EndMerge(f.ctx, claim))
	_, err = f.merge(h, "")
	require.NoError(t, err)
}

func TestMerge_AClaimThatNeverEndedIsIgnoredOnceStale(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.open(h, false)
	rec, err := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `UPDATE ticket_changes SET merging_since = now() - interval '10 minutes' WHERE id = $1`, rec.ID)
	require.NoError(t, err)

	claim, err := f.store.BeginMerge(f.ctx, rec.ID)

	require.NoError(t, err)
	assert.NotNil(t, claim, "a merge that died is not waited on for ever")
}

func TestMerge_ARejectedMergeEndsTheClaim(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.open(h, false)
	f.lock(h)
	f.host.MergeErr = codehost.ErrNotMergeable

	_, err := f.merge(h, "")
	require.ErrorIs(t, err, codehost.ErrNotMergeable)

	rec, err := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	assert.False(t, rec.Merging)
}

func TestMerge_AChangeOnAnotherBaseIsRefused(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.open(h, false)
	f.lock(h)
	_, err := f.database.Exec(f.ctx, `UPDATE ticket_changes SET base_branch = 'release'`)
	require.NoError(t, err)

	_, err = f.merge(h, "")

	require.ErrorIs(t, err, change.ErrWrongBase)
	assert.Empty(t, f.host.Merges())
	assert.Equal(t, "wrong_base", f.log.audited(t, audit.ChangeMergeRefused)[0]["reason"])
}

func TestMerge_AHostThatCannotReadTheChecksMergesNothing(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.open(h, false)
	f.lock(h)
	f.host.ChecksErr = codehost.ErrUnreachable

	_, err := f.merge(h, "")

	require.ErrorIs(t, err, codehost.ErrUnreachable)
	assert.Empty(t, f.host.Merges())
	assert.Len(t, f.log.audited(t, audit.ChangeOperationFailed), 1)

	_, err = f.svc.Checks(f.ctx, f.tenant, h.agent, "")
	require.ErrorIs(t, err, codehost.ErrUnreachable)
}

// A change already merged does not carry its approval to the next one.
func TestMerge_AChangeAfterAMergedOneNeedsANewApproval(t *testing.T) {
	f := newFixture(t)
	h := f.hold(true)
	f.open(h, false)
	f.handOverAndApprove(h)
	f.mergeLeavingTheTicketHeld(h)
	f.host.Opened = codehost.ChangeRef{Host: hostName, Owner: "acme", Repo: "app", Number: 8}
	f.host.ChecksOf = map[codehost.ChangeRef]*codehost.Checks{f.host.Opened: codehost.NewChecks(headB, "main", []codehost.Check{{Name: "ci", State: codehost.CheckSuccess}})}
	f.host.CompareOf = map[codehost.ChangeRef]*codehost.BaseComparison{f.host.Opened: {Base: "main", ContainsTip: true}}

	_, err := f.svc.Open(f.ctx, f.tenant, h.agent, &change.OpenInput{Branch: f.branchOf(h), Title: "t"})
	require.NoError(t, err)
	_, err = f.merge(h, "")

	require.ErrorIs(t, err, change.ErrNotApproved)
	assert.Len(t, f.host.Merges(), 1, "only the approved change was merged")
}

// An approval given to a PR the agent reported is not one for a change it
// opens afterwards.
func TestMerge_AnApprovalOfAReportedPRDoesNotCoverANewChange(t *testing.T) {
	f := newFixture(t)
	h := f.hold(true)
	f.approve(h)
	f.lock(h)
	f.open(h, false)

	_, err := f.merge(h, "")

	require.ErrorIs(t, err, change.ErrNotApproved)
	assert.Empty(t, f.host.Merges())
}

// The approval is of the commit handed over for review. The agent pushes
// other code after the approval, waits for it to be green, and tries to merge
// it: refused, and only a new hand-over and approval of that head lets it
// through.
func TestMerge_APushAfterTheApprovalNeedsANewReview(t *testing.T) {
	f := newFixture(t)
	h := f.hold(true)
	f.open(h, false)
	f.handOverAndApprove(h)
	f.setChecks(headB, codehost.Check{Name: "ci/build", State: codehost.CheckSuccess})

	_, err := f.merge(h, "")

	require.ErrorIs(t, err, change.ErrHeadNotReviewed)
	assert.Contains(t, err.Error(), headB)
	assert.Contains(t, err.Error(), headA)
	assert.Empty(t, f.host.Merges())
	lines := f.log.audited(t, audit.ChangeMergeRefused)
	require.Len(t, lines, 1)
	assert.Equal(t, "head_not_reviewed", lines[0]["reason"])

	f.handOverAndApprove(h)
	_, err = f.merge(h, "")
	require.NoError(t, err)
	assert.Equal(t, headB, f.host.Merges()[0].Merge.HeadSHA)
}

// The head is the one at the hand-over, not at the approval: a push made
// while the human was reading is not what they approved.
func TestMerge_APushBetweenTheHandOverAndTheApprovalIsNotApproved(t *testing.T) {
	f := newFixture(t)
	h := f.hold(true)
	f.open(h, false)
	_, err := f.svc.Review(f.ctx, f.tenant, h.agent, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: "https://git.example/acme/app/pull/7", Handoff: "ready"})
	require.NoError(t, err)
	f.setChecks(headB, codehost.Check{Name: "ci/build", State: codehost.CheckSuccess})
	_, err = f.tickets.Approve(f.ctx, h.ticket.ID, f.approver)
	require.NoError(t, err)
	_, err = f.tickets.Claim(f.ctx, h.agent, &h.ticket.ID)
	require.NoError(t, err)
	f.lock(h)

	_, err = f.merge(h, "")

	require.ErrorIs(t, err, change.ErrHeadNotReviewed)
	assert.Empty(t, f.host.Merges())
}

func TestMerge_ApprovalWithNoHandOverOfTheChangeIsNotOfAnyHead(t *testing.T) {
	f := newFixture(t)
	h := f.hold(true)
	f.open(h, false)
	f.approve(h)
	f.lock(h)

	_, err := f.merge(h, "")

	require.ErrorIs(t, err, change.ErrHeadNotReviewed)
	assert.Empty(t, f.host.Merges())
}

// A ticket that does not require approval has no review to tie the head to.
func TestMerge_NoApprovalRequiredNeedsNoReviewedHead(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.open(h, false)
	f.lock(h)

	_, err := f.merge(h, "")

	require.NoError(t, err)
}

// The hand-over's record is written with it or not at all: a refused
// hand-over leaves the head of an earlier one, and with it the approval,
// as they were.
func TestReview_ARefusedHandOverRecordsNoHead(t *testing.T) {
	f := newFixture(t)
	h := f.hold(true)
	f.open(h, false)
	f.setChecks(headB, codehost.Check{Name: "ci/build", State: codehost.CheckSuccess})

	_, err := f.svc.Review(f.ctx, f.tenant, h.agent, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: "https://git.example/acme/app/pull/7"})

	require.ErrorIs(t, err, ticket.ErrEmptyHandoff)
	rec, err := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	assert.Empty(t, rec.ReviewedHead)
}

// A ticket that reports its own PR, and has no change slop-haus opened, is
// handed over as before.
func TestReview_AReportedPRIsHandedOverUnchanged(t *testing.T) {
	f := newFixture(t)
	h := f.hold(true)

	left, err := f.svc.Review(f.ctx, f.tenant, h.agent, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: "https://git.example/acme/app/pull/7", Handoff: "ready"})

	require.NoError(t, err)
	assert.Equal(t, ticket.StatusReview, left.Status)
	assert.Empty(t, f.host.Checked(), "nothing is read from the host for a PR slop-haus did not open")
}

// A change opened for the ticket but a different PR reported to review is
// not the change under review: nothing is recorded, so the change cannot be
// merged on that review's approval.
func TestReview_AnotherPRThanTheChangeRecordsNoHead(t *testing.T) {
	f := newFixture(t)
	h := f.hold(true)
	f.open(h, false)

	_, err := f.svc.Review(f.ctx, f.tenant, h.agent, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: "https://git.example/acme/app/pull/99", Handoff: "ready"})
	require.NoError(t, err)
	_, err = f.tickets.Approve(f.ctx, h.ticket.ID, f.approver)
	require.NoError(t, err)
	_, err = f.tickets.Claim(f.ctx, h.agent, &h.ticket.ID)
	require.NoError(t, err)
	f.lock(h)

	_, err = f.merge(h, "")
	require.ErrorIs(t, err, change.ErrHeadNotReviewed)
}

// An earlier hand-over of the change recorded a head. A later hand-over of
// some other PR is not of the change, so the approval of it must not pair with
// that older head.
func TestReview_AnotherPRClearsTheHeadAnEarlierHandOverRecorded(t *testing.T) {
	f := newFixture(t)
	h := f.hold(true)
	f.open(h, false)
	f.handOverAndApprove(h)
	require.NoError(t, f.tickets.ReleaseIntegrationLock(f.ctx, h.agent))

	_, err := f.svc.Review(f.ctx, f.tenant, h.agent, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: "https://git.example/acme/app/pull/99", Handoff: "another"})
	require.NoError(t, err)
	_, err = f.tickets.Approve(f.ctx, h.ticket.ID, f.approver)
	require.NoError(t, err)
	_, err = f.tickets.Claim(f.ctx, h.agent, &h.ticket.ID)
	require.NoError(t, err)
	f.lock(h)

	_, err = f.merge(h, "")

	require.ErrorIs(t, err, change.ErrHeadNotReviewed)
	assert.Empty(t, f.host.Merges())
}

// A hand-over the ticket refuses changes nothing about the recorded head.
func TestReview_ARefusedHandOverLeavesTheRecordedHead(t *testing.T) {
	f := newFixture(t)
	h := f.hold(true)
	f.open(h, false)
	_, err := f.svc.Review(f.ctx, f.tenant, h.agent, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: "https://git.example/acme/app/pull/7", Handoff: "ready"})
	require.NoError(t, err)
	_, err = f.tickets.Approve(f.ctx, h.ticket.ID, f.approver)
	require.NoError(t, err)
	_, err = f.tickets.Claim(f.ctx, h.agent, &h.ticket.ID)
	require.NoError(t, err)

	_, err = f.svc.Review(f.ctx, f.tenant, h.agent, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: "https://git.example/acme/app/pull/99", Handoff: " "})

	require.ErrorIs(t, err, ticket.ErrEmptyHandoff)
	rec, err := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, headA, rec.ReviewedHead)
}

// The remedy names the configured base branch, not a hard-coded main.
func TestMerge_BehindTheBaseNamesTheConfiguredBranch(t *testing.T) {
	for _, tc := range []struct{ configured, want string }{{"", "origin/main"}, {"release", "origin/release"}} {
		t.Run(tc.want, func(t *testing.T) {
			f := newFixture(t)
			f.baseBranch = tc.configured
			f.svc = f.serviceWith(f.host)
			if tc.configured != "" {
				f.setBase(tc.configured)
				f.host.CompareOf[f.host.Opened].Base = tc.configured
			}
			h := f.hold(false)
			f.open(h, false)
			f.lock(h)
			f.host.CompareOf[f.host.Opened].ContainsTip = false

			_, err := f.merge(h, "")

			require.ErrorIs(t, err, change.ErrBehindBase)
			assert.Contains(t, err.Error(), "merge "+tc.want+" into your branch")
		})
	}
}

// A head that does not contain the base's current tip was tested against an
// older base: refused, and allowed once the branch has taken the base in.
func TestMerge_RefusesAHeadBehindTheBase(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.open(h, false)
	f.lock(h)
	f.host.CompareOf[f.host.Opened].ContainsTip = false

	_, err := f.merge(h, "")

	require.ErrorIs(t, err, change.ErrBehindBase)
	assert.Empty(t, f.host.Merges())
	require.Len(t, f.host.Compared(), 1)
	assert.Equal(t, headA, f.host.Compared()[0].HeadSHA, "the comparison is of the head the checks were read for")
	assert.Equal(t, "behind_base", f.log.audited(t, audit.ChangeMergeRefused)[0]["reason"])

	f.host.CompareOf[f.host.Opened].ContainsTip = true
	_, err = f.merge(h, "")
	require.NoError(t, err)
}

func TestMerge_AHostThatCannotCompareMergesNothing(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.open(h, false)
	f.lock(h)
	f.host.CompareErr = codehost.ErrUnreachable

	_, err := f.merge(h, "")

	require.ErrorIs(t, err, codehost.ErrUnreachable)
	assert.Empty(t, f.host.Merges())
}

// The change was opened onto main and recorded so, then retargeted on the
// host by someone with write access: the host would merge it into the other
// branch, so the gate goes by what the host says now.
func TestMerge_RefusesAChangeRetargetedOnTheHost(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.open(h, false)
	f.lock(h)
	f.setBase("release")

	_, err := f.merge(h, "")

	require.ErrorIs(t, err, change.ErrWrongBase)
	assert.Contains(t, err.Error(), "release")
	assert.Empty(t, f.host.Merges())
	assert.Equal(t, "wrong_base", f.log.audited(t, audit.ChangeMergeRefused)[0]["reason"])

	f.setBase("")
	_, err = f.merge(h, "")
	require.ErrorIs(t, err, change.ErrWrongBase, "a host that names no base is not main")

	f.setBase("main")
	_, err = f.merge(h, "")
	require.NoError(t, err)
}

func TestChecks_ReportTheBase(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.open(h, false)
	f.setBase("release")

	report, err := f.svc.Checks(f.ctx, f.tenant, h.agent, "")

	require.NoError(t, err)
	assert.Equal(t, "release", report.Base)
}

// The change is retargeted after the checks were read and before the
// comparison: the comparison names the base it was made against, and a base
// that is not the configured one refuses the merge.
func TestMerge_RefusesAChangeRetargetedBetweenTheChecksAndTheComparison(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.open(h, false)
	f.lock(h)
	f.host.AfterChecks = func() { f.host.CompareOf[f.host.Opened].Base = "release" }

	_, err := f.merge(h, "")

	require.ErrorIs(t, err, change.ErrWrongBase)
	assert.Empty(t, f.host.Merges())
	lines := f.log.audited(t, audit.ChangeMergeRefused)
	require.Len(t, lines, 1)
	assert.Equal(t, "compare", lines[0]["stage"])
}

func TestReview_ARefusedHostHeadHandsNothingOver(t *testing.T) {
	f := newFixture(t)
	h := f.hold(true)
	f.open(h, false)
	f.setChecks("", codehost.Check{Name: "ci/build", State: codehost.CheckSuccess})

	_, err := f.svc.Review(f.ctx, f.tenant, h.agent, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: "https://git.example/acme/app/pull/7", Handoff: "ready"})

	require.ErrorIs(t, err, change.ErrInvalid)
	got, gerr := f.tickets.Get(f.ctx, h.ticket.ID)
	require.NoError(t, gerr)
	assert.NotEqual(t, ticket.StatusReview, got.Status, "the ticket was not handed over")
	rec, cerr := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, cerr)
	assert.Empty(t, rec.ReviewedHead)
}
