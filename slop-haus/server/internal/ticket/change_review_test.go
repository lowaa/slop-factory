package ticket_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Reviews and merges made on the PR itself, as a GitHub webhook reports
// them: found by PR link, and held to the board's rules.

const githubPR = "https://github.com/Acme/Widgets/pull/12"

const prHost = "github.com"

const githubRefLink = "https://github.com/acme/widgets/pull/12"

var githubRef = codehost.ChangeRef{Host: "github.com", Owner: "acme", Repo: "widgets", Number: 12}

// inReviewWith files a ticket, has agentID claim it and hand it to review
// with prURL.
func (f *fixture) inReviewWith(t *testing.T, agentID uuid.UUID, title, prURL string) *ticket.Ticket {
	t.Helper()
	created := f.gated(t, title)
	_, err := f.svc.Claim(f.ctx, agentID, &created.ID)
	require.NoError(t, err)
	reviewed, err := f.svc.Review(f.ctx, agentID, &ticket.ReviewParams{PRURL: prURL, Handoff: "PR open."})
	require.NoError(t, err)
	return reviewed
}

// reviewStartedAt is when the ticket's current review began, as the
// database recorded it.
func (f *fixture) reviewStartedAt(t *testing.T, id int64) time.Time {
	t.Helper()
	var at time.Time
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT review_started_at FROM attempts WHERE ticket_id = $1 AND number = 1`, id).Scan(&at))
	return at
}

func (f *fixture) prReview(t *testing.T, id int64) *ticket.PRReview {
	return &ticket.PRReview{PR: githubRef, UserID: f.human, SubmittedAt: f.reviewStartedAt(t, id).Add(time.Minute)}
}

func TestApprovePR_ApprovesTheTicketInReviewWithThatPR(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	// Stored with a suffix and in another case than GitHub reports it.
	reviewed := f.inReviewWith(t, a, "Widgets", "https://github.com/ACME/widgets/pull/12/files")

	approved, err := f.svc.ApprovePR(f.ctx, f.prReview(t, reviewed.ID))
	require.NoError(t, err)

	assert.Equal(t, reviewed.ID, approved.ID)
	assert.Equal(t, ticket.StatusOpen, approved.Status)
	assert.Equal(t, model.ReviewApproved, approved.ReviewOutcome())
	require.NotNil(t, approved.ApprovedByUserID)
	assert.Equal(t, f.human, *approved.ApprovedByUserID)
}

// pull/1 is a prefix of pull/12's link, which the index narrows by; it is
// still a different PR.
func TestApprovePR_DoesNotMistakeAPRForOneWhoseNumberItPrefixes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	reviewed := f.inReviewWith(t, a, "PR 12", githubPR)

	_, err := f.svc.ApprovePR(f.ctx, &ticket.PRReview{PR: codehost.ChangeRef{Host: "github.com", Owner: "acme", Repo: "widgets", Number: 1},
		UserID: f.human, SubmittedAt: time.Now()})
	assert.ErrorIs(t, err, ticket.ErrNoTicketForPR)
	assert.Equal(t, ticket.StatusReview, f.status(t, reviewed.ID))
}

// LIKE wildcards in a repo name match only themselves, in the store's
// narrowing as well as the exact match after it: my_repo is not myxrepo.
func TestApprovePR_TreatsLikeWildcardsInTheRepoLiterally(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	reviewed := f.inReviewWith(t, a, "No underscore", "https://github.com/acme/myxrepo/pull/3")
	underscored := codehost.ChangeRef{Host: "github.com", Owner: "acme", Repo: "my_repo", Number: 3}

	narrowed, err := newStore(f.database).ListTickets(f.ctx, &ticket.ListFilter{PRLink: "https://github.com/acme/my_repo/pull/3"})
	require.NoError(t, err)
	assert.Empty(t, narrowed)

	_, err = f.svc.ApprovePR(f.ctx, &ticket.PRReview{PR: underscored, UserID: f.human, SubmittedAt: time.Now()})
	assert.ErrorIs(t, err, ticket.ErrNoTicketForPR)
	assert.Equal(t, ticket.StatusReview, f.status(t, reviewed.ID))
}

// A review from an earlier cycle -- submitted before the agent handed the
// current version over -- approves nothing, whenever it is delivered.
func TestApprovePR_RefusesAReviewSubmittedBeforeTheCurrentReviewBegan(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	reviewed := f.inReviewWith(t, a, "Widgets", githubPR)
	started := f.reviewStartedAt(t, reviewed.ID)

	_, err := f.svc.ApprovePR(f.ctx, &ticket.PRReview{PR: githubRef, UserID: f.human, SubmittedAt: started.Add(-time.Second)})
	assert.ErrorIs(t, err, ticket.ErrStaleReview)
	got := f.get(t, reviewed.ID)
	assert.Equal(t, ticket.StatusReview, got.Status)
	assert.Equal(t, model.ReviewNone, got.ReviewOutcome())

	// GitHub's times are to the second: one in the same second as the
	// start counts.
	_, err = f.svc.ApprovePR(f.ctx, &ticket.PRReview{PR: githubRef, UserID: f.human, SubmittedAt: started.Truncate(time.Second)})
	require.NoError(t, err)
	assert.Equal(t, model.ReviewApproved, f.get(t, reviewed.ID).ReviewOutcome())
}

// Every route into review starts a new cycle, a person's status edit too.
func TestApprovePR_AStatusEditIntoReviewStartsANewCycle(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	reviewed := f.inReviewWith(t, a, "Widgets", githubPR)
	_, err := f.svc.RequestChanges(f.ctx, reviewed.ID, f.human, "Again.")
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `UPDATE attempts SET review_started_at = now() - interval '1 hour' WHERE ticket_id = $1 AND number = 1`, reviewed.ID)
	require.NoError(t, err)

	_, err = f.setStatus(t, reviewed.ID, ticket.StatusReview)
	require.NoError(t, err)

	_, err = f.svc.ApprovePR(f.ctx, &ticket.PRReview{PR: githubRef, UserID: f.human, SubmittedAt: time.Now().Add(-10 * time.Minute)})
	assert.ErrorIs(t, err, ticket.ErrStaleReview)
}

func TestApprovePR_SaysWhichTicketWhenItIsNotInReview(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	reviewed := f.inReviewWith(t, a, "Widgets", githubPR)
	f.approve(t, reviewed.ID)

	_, err := f.svc.ApprovePR(f.ctx, f.prReview(t, reviewed.ID))
	assert.ErrorIs(t, err, ticket.ErrNotInReview)
	assert.ErrorContains(t, err, "#1 is open")
}

func TestApprovePR_NoTicketWithThatPR(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.ticket(t, "No PR")

	_, err := f.svc.ApprovePR(f.ctx, &ticket.PRReview{PR: githubRef, UserID: f.human, SubmittedAt: time.Now()})
	assert.ErrorIs(t, err, ticket.ErrNoTicketForPR)
}

// Two tickets in review on one PR: guessing would approve the wrong one, so
// neither is touched.
func TestApprovePR_LeavesAnAmbiguousPRAlone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	first := f.inReviewWith(t, f.agent(t, "builder-1"), "One", githubPR)
	second := f.inReviewWith(t, f.agent(t, "builder-2"), "Two", githubPR)

	_, err := f.svc.ApprovePR(f.ctx, f.prReview(t, second.ID))
	assert.ErrorIs(t, err, ticket.ErrAmbiguousPR)
	assert.Equal(t, ticket.StatusReview, f.status(t, first.ID))
	assert.Equal(t, ticket.StatusReview, f.status(t, second.ID))
}

// Only one of them in review is not ambiguous: the other is not something
// a review can apply to.
func TestApprovePR_PicksTheOneInReviewAmongTicketsSharingAPR(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	earlier := f.inReviewWith(t, f.agent(t, "builder-1"), "Earlier", githubPR)
	f.approve(t, earlier.ID)
	current := f.inReviewWith(t, f.agent(t, "builder-2"), "Current", githubPR)

	approved, err := f.svc.ApprovePR(f.ctx, f.prReview(t, current.ID))
	require.NoError(t, err)
	assert.Equal(t, current.ID, approved.ID)
}

// Row level security: a PR one tenant's ticket reported is invisible to
// another's webhook.
func TestApprovePR_NeverReachesAnotherTenantsTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	reviewed := f.inReviewWith(t, f.agent(t, "builder-1"), "Widgets", githubPR)
	other := db.WithTenant(context.Background(), uuid.New())

	_, err := f.svc.ApprovePR(other, f.prReview(t, reviewed.ID))
	assert.ErrorIs(t, err, ticket.ErrNoTicketForPR)
	assert.Equal(t, ticket.StatusReview, f.status(t, reviewed.ID))
}

func TestRequestChangesPR_RecordsTheCommentAsTheReviewers(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	reviewed := f.inReviewWith(t, f.agent(t, "builder-1"), "Widgets", githubPR)
	r := f.prReview(t, reviewed.ID)
	r.Comment = "Split it up."

	sent, err := f.svc.RequestChangesPR(f.ctx, r)
	require.NoError(t, err)
	assert.Equal(t, model.ReviewChangesRequested, sent.ReviewOutcome())

	comments, err := f.svc.ListComments(f.ctx, reviewed.ID)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, "Split it up.", comments[0].Body)
	require.NotNil(t, comments[0].AuthorUserID)
	assert.Equal(t, f.human, *comments[0].AuthorUserID)
}

func TestRequestChangesPR_RefusesAStaleReview(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	reviewed := f.inReviewWith(t, f.agent(t, "builder-1"), "Widgets", githubPR)
	r := f.prReview(t, reviewed.ID)
	r.SubmittedAt = f.reviewStartedAt(t, reviewed.ID).Add(-time.Minute)
	r.Comment = "Old news."

	_, err := f.svc.RequestChangesPR(f.ctx, r)
	assert.ErrorIs(t, err, ticket.ErrStaleReview)
	comments, err := f.svc.ListComments(f.ctx, reviewed.ID)
	require.NoError(t, err)
	assert.Empty(t, comments, "a refused review leaves no comment behind")
}

func TestCloseMergedPR_ClosesATicketInReview(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	reviewed := f.inReviewWith(t, f.agent(t, "builder-1"), "Widgets", githubPR)

	done, err := f.svc.CloseMergedPR(f.ctx, githubRef, f.human)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDone, done.Status)
	assert.NotNil(t, done.ClosedAt)

	comments, err := f.svc.ListComments(f.ctx, reviewed.ID)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, "Merged on github.com: https://github.com/acme/widgets/pull/12", comments[0].Body)
}

// The ticket rules name no host: a change on another registered host is
// found and closed the same way, and the comment says whose merge it was.
func TestCloseMergedPR_AnotherHostsChange(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.svc = ticket.NewService(newStore(f.database), slog.DiscardHandler, ticket.WithCodeHosts(codehosttest.Registry(
		codehosttest.NewLinkHost("github.com"), codehosttest.NewLinkHost("git.example.test"))))
	reviewed := f.inReviewWith(t, f.agent(t, "builder-1"), "Widgets", "https://git.example.test/acme/widgets/pull/12/files")
	ours := codehost.ChangeRef{Host: "git.example.test", Owner: "acme", Repo: "widgets", Number: 12}

	_, err := f.svc.CloseMergedPR(f.ctx, githubRef, f.human)
	assert.ErrorIs(t, err, ticket.ErrNoTicketForPR, "the same number in the same repo, on another host, is another change")

	done, err := f.svc.CloseMergedPR(f.ctx, ours, f.human)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDone, done.Status)
	comments, err := f.svc.ListComments(f.ctx, reviewed.ID)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, "Merged on git.example.test: https://git.example.test/acme/widgets/pull/12", comments[0].Body)
}

func TestReviewsOnAnotherHostsChange(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.svc = ticket.NewService(newStore(f.database), slog.DiscardHandler, ticket.WithCodeHosts(codehosttest.Registry(
		codehosttest.NewLinkHost("github.com"), codehosttest.NewLinkHost("git.example.test"))))
	ours := codehost.ChangeRef{Host: "git.example.test", Owner: "acme", Repo: "widgets", Number: 12}
	a := f.agent(t, "builder-1")
	first := f.inReviewWith(t, a, "First", "https://git.example.test/acme/widgets/pull/12")

	approved, err := f.svc.ApprovePR(f.ctx, &ticket.PRReview{PR: ours, UserID: f.human, SubmittedAt: f.reviewStartedAt(t, first.ID).Add(time.Minute)})
	require.NoError(t, err)
	assert.Equal(t, model.ReviewApproved, approved.ReviewOutcome())

	b := f.agent(t, "builder-2")
	second := f.inReviewWith(t, b, "Second", "https://git.example.test/acme/widgets/pull/13")
	ours.Number = 13
	sent, err := f.svc.RequestChangesPR(f.ctx, &ticket.PRReview{PR: ours, UserID: f.human, Comment: "Split it.",
		SubmittedAt: f.reviewStartedAt(t, second.ID).Add(time.Minute)})
	require.NoError(t, err)
	assert.Equal(t, model.ReviewChangesRequested, sent.ReviewOutcome())
}

func TestCloseMergedPR_ClosesAnApprovedTicketNobodyHasClaimed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	reviewed := f.inReviewWith(t, f.agent(t, "builder-1"), "Widgets", githubPR)
	f.approve(t, reviewed.ID)

	done, err := f.svc.CloseMergedPR(f.ctx, githubRef, f.human)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDone, done.Status)
}

// The agent merging after approval holds the ticket: its own ticket_finish
// closes it, with its summary.
func TestCloseMergedPR_LeavesATicketAnAgentHolds(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	reviewed := f.inReviewWith(t, a, "Widgets", githubPR)
	f.approve(t, reviewed.ID)
	_, err := f.svc.Claim(f.ctx, a, &reviewed.ID)
	require.NoError(t, err)

	_, err = f.svc.CloseMergedPR(f.ctx, githubRef, f.human)
	assert.ErrorIs(t, err, ticket.ErrNotMergeable)
	assert.Equal(t, ticket.StatusInProgress, f.status(t, reviewed.ID))
	held, err := f.svc.LeaseFor(f.ctx, a)
	require.NoError(t, err)
	assert.Equal(t, reviewed.ID, held.TicketID)
}

// Back for changes is not waiting to be merged.
func TestCloseMergedPR_LeavesATicketSentBackForChanges(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	reviewed := f.inReviewWith(t, f.agent(t, "builder-1"), "Widgets", githubPR)
	f.requestChanges(t, reviewed.ID)

	_, err := f.svc.CloseMergedPR(f.ctx, githubRef, f.human)
	assert.ErrorIs(t, err, ticket.ErrNotMergeable)
	assert.Equal(t, ticket.StatusOpen, f.status(t, reviewed.ID))
}

// Judged again under the claim lock: an agent that claims the approved
// ticket between the lookup and the move wins.
func TestCloseMerged_RechecksUnderTheClaimLock(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	reviewed := f.inReviewWith(t, a, "Widgets", githubPR)
	f.approve(t, reviewed.ID)
	_, err := f.svc.Claim(f.ctx, a, &reviewed.ID)
	require.NoError(t, err)

	_, err = f.svc.CloseMerged(f.ctx, reviewed.ID, githubRef, f.human)
	assert.ErrorIs(t, err, ticket.ErrNotMergeable)
	assert.Equal(t, ticket.StatusInProgress, f.status(t, reviewed.ID))
}

// The PR is judged again under the claim lock too: a merge of the PR the
// ticket had when it was matched, arriving after the agent has since put a
// new PR up for review, did not merge that new PR.
func TestCloseMerged_RefusesATicketWhosePRHasSinceChanged(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	reviewed := f.inReviewWith(t, a, "Widgets", githubPR)
	f.requestChanges(t, reviewed.ID)
	_, err := f.svc.Claim(f.ctx, a, &reviewed.ID)
	require.NoError(t, err)
	const nextPR = "https://github.com/acme/widgets/pull/13"
	_, err = f.svc.Review(f.ctx, a, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: nextPR, Handoff: "Addressed."})
	require.NoError(t, err)

	_, err = f.svc.CloseMerged(f.ctx, reviewed.ID, githubRef, f.human)
	assert.ErrorIs(t, err, ticket.ErrNotMergeable)
	assert.ErrorContains(t, err, "#1's PR is no longer "+githubRefLink)
	got := f.get(t, reviewed.ID)
	assert.Equal(t, ticket.StatusReview, got.Status)
	require.NotNil(t, got.PRURL)
	assert.Equal(t, nextPR, *got.PRURL)
}

// A worker opens its change as a draft at its first push, so the ticket
// carries the PR's link while it is still being worked. That link must not
// make the ticket look finished or reviewed: a review, a send-back or a merge
// reported on the host moves nothing until the ticket is handed over.
func TestEarlyDraftPR_HostEventsLeaveATicketBeingWorkedAlone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Widgets")
	_, err := f.svc.Claim(f.ctx, a, &created.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.RecordChange(f.ctx, a, githubPR, true, nil))
	review := &ticket.PRReview{PR: githubRef, UserID: f.human, SubmittedAt: time.Now(), Comment: "Nope."}

	_, err = f.svc.ApprovePR(f.ctx, review)
	assert.ErrorIs(t, err, ticket.ErrNotInReview)
	_, err = f.svc.RequestChangesPR(f.ctx, review)
	assert.ErrorIs(t, err, ticket.ErrNotInReview)
	_, err = f.svc.CloseMergedPR(f.ctx, githubRef, f.human)
	assert.ErrorIs(t, err, ticket.ErrNotMergeable)

	got := f.get(t, created.ID)
	assert.Equal(t, ticket.StatusInProgress, got.Status)
	assert.Nil(t, got.ApprovedAt)
}

// An approval given on the draft before the hand-over predates the review's
// start, so it does not approve what is then put in front of the human.
func TestEarlyDraftPR_AnApprovalGivenBeforeTheHandOverIsStale(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Widgets")
	_, err := f.svc.Claim(f.ctx, a, &created.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.RecordChange(f.ctx, a, githubPR, true, nil))
	early := time.Now().Add(-time.Minute)
	_, err = f.svc.Review(f.ctx, a, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: githubPR, Handoff: "Ready."})
	require.NoError(t, err)

	_, err = f.svc.ApprovePR(f.ctx, &ticket.PRReview{PR: githubRef, UserID: f.human, SubmittedAt: early})

	assert.ErrorIs(t, err, ticket.ErrStaleReview)
	assert.Equal(t, ticket.StatusReview, f.status(t, created.ID))
}

// queuedOpen is a ticket that is open yet waiting in the merge queue, as one
// sent back and re-queued derives.
func (f *fixture) queuedOpen(t *testing.T) *ticket.Ticket {
	t.Helper()
	reviewed := f.inReviewWith(t, f.agent(t, "builder-1"), "Widgets", githubPR)
	f.requestChanges(t, reviewed.ID)
	_, err := f.database.Exec(f.ctx, `
INSERT INTO landings (ticket_id, attempt_number, change_url, head_sha) VALUES ($1, 1, $2, 'abc')`,
		reviewed.ID, githubPR)
	require.NoError(t, err)
	return reviewed
}

func (f *fixture) landingRows(t *testing.T, id int64) int {
	t.Helper()
	var n int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM landings WHERE ticket_id = $1`, id).Scan(&n))
	return n
}

func TestCloseMergedPR_ClosesAndDequeuesAQueuedTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	queued := f.queuedOpen(t)

	done, err := f.svc.CloseMergedPR(f.ctx, githubRef, f.human)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDone, done.Status)
	assert.Zero(t, f.landingRows(t, queued.ID))
	var detail, url, sha string
	var attempt int
	var enqueued *time.Time
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT detail, change_url, head_sha, attempt_number, enqueued_at FROM server_merge_outcomes WHERE ticket_id = $1 AND outcome = 'merged'`,
		queued.ID).Scan(&detail, &url, &sha, &attempt, &enqueued))
	assert.Equal(t, githubPR, url)
	assert.Equal(t, "abc", sha)
	assert.Equal(t, 1, attempt)
	assert.NotNil(t, enqueued)
	assert.Contains(t, detail, "outside the queue")
	assert.Contains(t, detail, f.human.String())
}

// The runner's reconcile finishes a row it has begun; closing it here would
// race it.
func TestCloseMergedPR_LeavesAQueuedTicketTheRunnerHolds(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	queued := f.queuedOpen(t)
	_, err := f.database.Exec(f.ctx, `UPDATE landings SET queue_state = 'serial_merging' WHERE ticket_id = $1`, queued.ID)
	require.NoError(t, err)

	_, err = f.svc.CloseMergedPR(f.ctx, githubRef, f.human)
	assert.ErrorIs(t, err, ticket.ErrNotMergeable)
	assert.Equal(t, ticket.StatusOpen, f.status(t, queued.ID))
	assert.Equal(t, 1, f.landingRows(t, queued.ID))
}

// A ticket a worker holds is still refused, whatever its queue row says.
func TestCloseMergedPR_LeavesAHeldTicketWithAQueueRow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	reviewed := f.inReviewWith(t, a, "Widgets", githubPR)
	f.approve(t, reviewed.ID)
	_, err := f.svc.Claim(f.ctx, a, &reviewed.ID)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `
INSERT INTO landings (ticket_id, attempt_number, change_url, head_sha) VALUES ($1, 1, $2, 'abc')`,
		reviewed.ID, githubPR)
	require.NoError(t, err)

	_, err = f.svc.CloseMergedPR(f.ctx, githubRef, f.human)
	assert.ErrorIs(t, err, ticket.ErrNotMergeable)
	assert.Equal(t, ticket.StatusInProgress, f.status(t, reviewed.ID))
	assert.Equal(t, 1, f.landingRows(t, reviewed.ID))
}

// A row handed back to its author has no step either, but is not waiting to land.
func TestCloseMergedPR_LeavesAHandedBackTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	handedBack := f.queuedOpen(t)
	_, err := f.database.Exec(f.ctx, `UPDATE landings SET queue_state = 'back_with_agent', back_reason = 'checks_red' WHERE ticket_id = $1`, handedBack.ID)
	require.NoError(t, err)

	_, err = f.svc.CloseMergedPR(f.ctx, githubRef, f.human)
	assert.ErrorIs(t, err, ticket.ErrNotMergeable)
	assert.Equal(t, ticket.StatusOpen, f.status(t, handedBack.ID))
	assert.Equal(t, 1, f.landingRows(t, handedBack.ID))
}

// inMergeQueue is queuedOpen as the status shows it since the queue owns it.
func (f *fixture) inMergeQueue(t *testing.T) *ticket.Ticket {
	t.Helper()
	queued := f.queuedOpen(t)
	f.setAttemptStatus(t, queued.ID, 1, "in_merge_queue")
	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET status = 'in_merge_queue' WHERE id = $1`, queued.ID)
	require.NoError(t, err)
	return queued
}

func TestCloseMergedPR_ClosesAndDequeuesATicketInTheMergeQueue(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	queued := f.inMergeQueue(t)

	done, err := f.svc.CloseMergedPR(f.ctx, githubRef, f.human)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDone, done.Status)
	assert.Zero(t, f.landingRows(t, queued.ID))
	var n int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM server_merge_outcomes WHERE ticket_id = $1 AND outcome = 'merged'`, queued.ID).Scan(&n))
	assert.Equal(t, 1, n, "the merge outside the queue is recorded")
}

func TestCloseMergedPR_LeavesATicketInTheMergeQueueTheRunnerHolds(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	queued := f.inMergeQueue(t)
	_, err := f.database.Exec(f.ctx, `UPDATE landings SET queue_state = 'serial_merging' WHERE ticket_id = $1`, queued.ID)
	require.NoError(t, err)

	_, err = f.svc.CloseMergedPR(f.ctx, githubRef, f.human)
	assert.ErrorIs(t, err, ticket.ErrNotMergeable)
	assert.Equal(t, 1, f.landingRows(t, queued.ID))
}
