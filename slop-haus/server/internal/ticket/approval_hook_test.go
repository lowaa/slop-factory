package ticket_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

func (f *fixture) approvalsHeard() []ticket.Approval {
	f.svc.WaitApprovalHooks()
	return f.approvals.Approvals()
}

// storedApproval is what the database holds for the attempt's approval.
func (f *fixture) storedApproval(t *testing.T, id int64, attempt int) time.Time {
	t.Helper()
	var at time.Time
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT approved_at FROM attempts WHERE ticket_id = $1 AND number = $2`, id, attempt).Scan(&at))
	return at
}

func (f *fixture) requireApprovedOnce(t *testing.T, id int64, attempt int) {
	t.Helper()
	heard := f.approvalsHeard()
	require.Len(t, heard, 1)
	assert.Equal(t, ticket.Approval{
		TenantID: f.tenant, TicketID: id, Attempt: attempt, ApproverID: f.human, ApprovedAt: f.storedApproval(t, id, attempt),
	}, heard[0])
}

func TestApprovalHook_BoardApprovalFiresOnceAndARepeatDoesNot(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	reviewed := f.inReviewWith(t, a, "Widgets", githubRefLink)

	f.approve(t, reviewed.ID)
	_, err := f.svc.Approve(f.ctx, reviewed.ID, f.human)

	require.ErrorIs(t, err, ticket.ErrNotInReview)
	f.requireApprovedOnce(t, reviewed.ID, 1)
}

func TestApprovalHook_RequestChangesFiresNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	reviewed := f.inReviewWith(t, a, "Widgets", githubRefLink)

	f.requestChanges(t, reviewed.ID)

	assert.Empty(t, f.approvalsHeard())
}

func TestApprovalHook_ReviewWebhookFiresOnceAndARepeatDoesNot(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	reviewed := f.inReviewWith(t, a, "Widgets", githubRefLink)
	review := f.prReview(t, reviewed.ID)

	_, err := f.svc.ApprovePR(f.ctx, review)
	require.NoError(t, err)
	_, err = f.svc.ApprovePR(f.ctx, review)

	require.Error(t, err)
	f.requireApprovedOnce(t, reviewed.ID, 1)
}

func TestApprovalHook_ChangesRequestedOnTheHostFiresNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	reviewed := f.inReviewWith(t, a, "Widgets", githubRefLink)
	review := f.prReview(t, reviewed.ID)
	review.Comment = "Rename it."

	_, err := f.svc.RequestChangesPR(f.ctx, review)
	require.NoError(t, err)

	assert.Empty(t, f.approvalsHeard())
}

func TestApprovalHook_PromotionOfAnAttemptAwaitingTheChoiceFiresForThatAttempt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, _ := f.promoteTwo(t)
	a2 := f.agent(t, "second")
	require.NotNil(t, f.claim(t, a2))
	f.review(t, a2)

	f.promote(t, id, 2)

	f.requireApprovedOnce(t, id, 2)
}

func TestApprovalHook_PromotionThatApprovesNothingFiresNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, _ := f.promoteTwo(t)

	f.promote(t, id, 2)
	f.promote(t, id, 2)

	assert.Empty(t, f.approvalsHeard())
}

func TestApprovalHook_ARefusedApprovalFiresNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	queued := f.ticket(t, "Queued")

	_, err := f.svc.Approve(f.ctx, queued.ID, f.human)

	require.ErrorIs(t, err, ticket.ErrNotInReview)
	assert.Empty(t, f.approvalsHeard())
}

func TestApprovalHook_ListenerSeesTheCommittedApproval(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	reviewed := f.inReviewWith(t, a, "Widgets", githubRefLink)
	seen := make(chan time.Time, 1)
	f.approvals.Behave = func(ctx context.Context, ap *ticket.Approval) error {
		// A different connection from the approval's: it finds the row only
		// if the transaction had already committed.
		seen <- f.storedApproval(t, ap.TicketID, ap.Attempt)
		return nil
	}

	f.approve(t, reviewed.ID)

	f.svc.WaitApprovalHooks()
	require.Len(t, seen, 1)
	assert.Equal(t, f.storedApproval(t, reviewed.ID, 1), <-seen)
}

func TestApprovalHook_AFailingListenerDoesNotFailTheApproval(t *testing.T) {
	t.Parallel()
	for name, behave := range map[string]func(context.Context, *ticket.Approval) error{
		"error": func(context.Context, *ticket.Approval) error { return errors.New("listener down") },
		"panic": func(context.Context, *ticket.Approval) error { panic("listener bug") },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			a := f.agent(t, "builder-1")
			reviewed := f.inReviewWith(t, a, "Widgets", githubRefLink)
			f.approvals.Behave = behave

			approved := f.approve(t, reviewed.ID)

			assert.Equal(t, model.ReviewApproved, approved.ReviewOutcome())
			f.requireApprovedOnce(t, reviewed.ID, 1)
		})
	}
}

func TestApprovalHook_ASlowListenerDoesNotDelayTheApproval(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	reviewed := f.inReviewWith(t, a, "Widgets", githubRefLink)
	entered, release := make(chan struct{}), make(chan struct{})
	f.approvals.Behave = func(context.Context, *ticket.Approval) error {
		close(entered)
		<-release
		return nil
	}

	approved := f.approve(t, reviewed.ID)

	// The listener is still blocked, and the approval has already returned.
	<-entered
	assert.Equal(t, model.ReviewApproved, approved.ReviewOutcome())
	close(release)
	f.requireApprovedOnce(t, reviewed.ID, 1)
}

func TestApprovalHook_TheListenerRunsUnderADeadline(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	reviewed := f.inReviewWith(t, a, "Widgets", githubRefLink)
	deadlines := make(chan bool, 1)
	f.approvals.Behave = func(ctx context.Context, _ *ticket.Approval) error {
		_, ok := ctx.Deadline()
		deadlines <- ok
		return nil
	}

	f.approve(t, reviewed.ID)

	f.svc.WaitApprovalHooks()
	assert.True(t, <-deadlines)
}
