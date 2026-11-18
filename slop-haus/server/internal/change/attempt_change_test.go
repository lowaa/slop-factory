package change_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

const (
	pr1 = "https://git.example/acme/app/pull/7"
	pr2 = "https://git.example/acme/app/pull/8"
)

// addSibling gives h's ticket a second attempt and has a new agent claim it,
// as two agents may once the add-attempt endpoint exists. The second
// attempt's changes are #8 on the host; the first's stay #7.
func (f *fixture) addSibling(h *holder) *holder {
	f.t.Helper()
	_, err := f.database.Exec(f.ctx, `
		WITH n AS (UPDATE tickets SET next_attempt_number = next_attempt_number + 1 WHERE id = $1 RETURNING next_attempt_number - 1 AS number)
		INSERT INTO attempts (ticket_id, number, status) SELECT $1, number, 'queued' FROM n`, h.ticket.ID)
	require.NoError(f.t, err)
	f.pinBase(h, baseOld)
	a := f.newAgent("sibling-" + uuid.NewString()[:8])
	claimed, err := f.tickets.Claim(f.ctx, a, &h.ticket.ID)
	require.NoError(f.t, err)
	require.NotNil(f.t, claimed.Ticket)
	f.expectChange(codehost.ChangeRef{Host: hostName, Owner: "acme", Repo: "app", Number: 8})
	return &holder{agent: a, ticket: claimed.Ticket, attempt: claimed.Attempt.Number}
}

// pinBase pins the ticket's base commit as a claim does when the host answers;
// the fixture's ticket service has no workspace to read it with.
func (f *fixture) pinBase(h *holder, sha string) {
	f.t.Helper()
	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET base_commit = $2 WHERE id = $1`, h.ticket.ID, sha)
	require.NoError(f.t, err)
}

// promote makes attempt n the ticket's promoted one, as a human's choice will
// once promotion exists.
func (f *fixture) promote(h *holder, n int) {
	f.t.Helper()
	_, err := f.database.Exec(f.ctx, `UPDATE attempts SET promoted_at = NULL WHERE ticket_id = $1 AND promoted_at IS NOT NULL`, h.ticket.ID)
	require.NoError(f.t, err)
	_, err = f.database.Exec(f.ctx, `UPDATE attempts SET promoted_at = now(), promoted_by_user_id = $3 WHERE ticket_id = $1 AND number = $2`, h.ticket.ID, n, f.approver)
	require.NoError(f.t, err)
}

// expectChange has the host report passing checks for ref as well, without
// replacing those of the changes it already reports.
func (f *fixture) expectChange(ref codehost.ChangeRef) {
	f.host.ChecksOf[ref] = codehost.NewChecks(headA, "main", []codehost.Check{{Name: "ci/build", State: codehost.CheckSuccess}})
	f.host.CompareOf[ref] = &codehost.BaseComparison{Base: "main", ContainsTip: true}
}

// openAs opens a change as h, which the host numbers n.
func (f *fixture) openAs(h *holder, n int64, draft bool) *change.Opened {
	f.t.Helper()
	f.host.Opened.Number = n
	return f.open(h, draft)
}

func (f *fixture) currentOf(h *holder, attempt int) *change.Record {
	f.t.Helper()
	rec, err := f.store.Current(f.ctx, h.ticket.ID, attempt)
	require.NoError(f.t, err)
	return rec
}

// approveAttempt is a person's approval of one attempt's change at its
// current head, written directly (see approve).
func (f *fixture) approveAttempt(h *holder, attempt int) {
	f.t.Helper()
	_, err := f.database.Exec(f.ctx, `UPDATE attempts SET approved_at = now(), approved_by_user_id = $3 WHERE ticket_id = $1 AND number = $2`,
		h.ticket.ID, attempt, f.approver)
	require.NoError(f.t, err)
	rec := f.currentOf(h, attempt)
	require.NoError(f.t, f.database.InTx(f.ctx, func(q db.Querier) error {
		return f.store.SetReviewed(f.ctx, q, rec.ID, &change.Reviewed{HeadSHA: headA})
	}))
}

func (f *fixture) prURLOf(h *holder, attempt int) *string {
	f.t.Helper()
	var pr *string
	require.NoError(f.t, f.database.QueryRow(f.ctx, `SELECT pr_url FROM attempts WHERE ticket_id = $1 AND number = $2`, h.ticket.ID, attempt).Scan(&pr))
	return pr
}

func (f *fixture) approvedAt(h *holder, attempt int) bool {
	f.t.Helper()
	var approved bool
	require.NoError(f.t, f.database.QueryRow(f.ctx, `SELECT approved_at IS NOT NULL FROM attempts WHERE ticket_id = $1 AND number = $2`, h.ticket.ID, attempt).Scan(&approved))
	return approved
}

func TestAttempts_EachKeepsItsOwnLiveChange(t *testing.T) {
	f := newFixture(t)
	first := f.hold(true)
	second := f.addSibling(first)

	f.openAs(first, 7, false)
	f.approveAttempt(first, 1)
	f.openAs(second, 8, true)

	r1, r2 := f.currentOf(first, 1), f.currentOf(first, 2)
	assert.Equal(t, int64(7), r1.Change.Number)
	assert.Equal(t, int64(8), r2.Change.Number)
	var live int // neither attempt's change superseded the other's
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM ticket_changes WHERE ticket_id = $1 AND superseded_at IS NULL`, first.ticket.ID).Scan(&live))
	assert.Equal(t, 2, live)

	report, err := f.svc.Checks(f.ctx, f.tenant, first.agent, "")
	require.NoError(t, err)
	assert.Equal(t, pr1, report.URL)
	report, err = f.svc.Checks(f.ctx, f.tenant, second.agent, "")
	require.NoError(t, err)
	assert.Equal(t, pr2, report.URL)
}

func TestAttempts_OpeningASiblingsChangeLeavesThisAttemptsLinkAndApproval(t *testing.T) {
	f := newFixture(t)
	first := f.hold(true)
	second := f.addSibling(first)
	f.openAs(first, 7, false)
	f.approveAttempt(first, 1)

	f.openAs(second, 8, true)

	require.NotNil(t, f.prURLOf(first, 1))
	assert.Equal(t, pr1, *f.prURLOf(first, 1))
	assert.True(t, f.approvedAt(first, 1), "an approval of attempt 1's change is not discarded by attempt 2's")
	require.NotNil(t, f.prURLOf(first, 2))
	assert.Equal(t, pr2, *f.prURLOf(first, 2))
	assert.False(t, f.approvedAt(first, 2))
}

func TestAttempts_TheMergeGateUsesTheMergingAttemptsApproval(t *testing.T) {
	f := newFixture(t)
	first := f.hold(true)
	second := f.addSibling(first)
	f.openAs(first, 7, false)
	f.openAs(second, 8, true)

	f.approveAttempt(second, 2)
	f.lock(first)
	_, err := f.merge(first, "")
	require.ErrorIs(t, err, change.ErrNotApproved, "approving attempt 2 does not approve attempt 1's change")
	require.NoError(t, f.tickets.ReleaseIntegrationLock(f.ctx, first.agent))

	// Promotion moves the merge to attempt 2, whose change is then ready.
	f.promote(first, 2)
	require.NoError(t, f.store.MarkReady(f.ctx, f.currentOf(first, 2).ID))
	_, err = f.database.Exec(f.ctx, `UPDATE ticket_changes SET base_branch = 'main' WHERE ticket_id = $1 AND attempt_number = 2`, first.ticket.ID)
	require.NoError(t, err) // what retargeting on promotion will do
	f.approveAttempt(first, 1)
	_, err = f.database.Exec(f.ctx, `UPDATE attempts SET approved_at = NULL, approved_by_user_id = NULL WHERE ticket_id = $1 AND number = 2`, first.ticket.ID)
	require.NoError(t, err)
	f.lock(second)
	_, err = f.merge(second, "")
	require.ErrorIs(t, err, change.ErrNotApproved, "approving attempt 1 does not approve attempt 2's change")
	require.NoError(t, f.tickets.ReleaseIntegrationLock(f.ctx, second.agent))

	f.approveAttempt(second, 2)
	f.lock(second)
	merged, err := f.merge(second, headA)
	require.NoError(t, err)
	assert.Equal(t, pr2, merged.URL)
}

func TestAttempts_AMergeIsOfItsOwnAttemptsChangeNotALaterSiblings(t *testing.T) {
	f := newFixture(t)
	first := f.hold(true)
	f.openAs(first, 7, false)
	f.handOverAndApprove(first)
	second := f.addSibling(first)

	f.openAs(second, 8, true)
	merged, err := f.merge(first, headA)

	require.NoError(t, err)
	assert.Equal(t, pr1, merged.URL)
	merges := f.host.Merges()
	require.Len(t, merges, 1)
	assert.Equal(t, int64(7), merges[0].Merge.Change.Number, "attempt 1's pull request is the one merged to main")
	assert.NotNil(t, f.currentOf(first, 1).MergedAt)
	assert.Nil(t, f.currentOf(first, 2).MergedAt, "the sibling's change is not merged")
}

func TestAttempts_ASiblingOpeningItsChangeWithoutALiveOneOfItsOwnDoesNotSeeTheFirstsChange(t *testing.T) {
	f := newFixture(t)
	first := f.hold(true)
	second := f.addSibling(first)
	f.openAs(first, 7, false)

	_, err := f.store.Current(f.ctx, first.ticket.ID, 2)
	require.ErrorIs(t, err, change.ErrNoChange)
	f.promote(first, 2)
	f.lock(second)
	_, err = f.merge(second, "")
	require.ErrorIs(t, err, change.ErrNoChange)
}

func TestAttempts_AReviewHandOverRecordsTheHeadOnItsOwnAttemptsChange(t *testing.T) {
	f := newFixture(t)
	first := f.hold(true)
	second := f.addSibling(first)
	f.openAs(first, 7, false)
	f.openAs(second, 8, true)
	rec1 := f.currentOf(first, 1)
	require.NoError(t, f.database.InTx(f.ctx, func(q db.Querier) error {
		return f.store.SetReviewed(f.ctx, q, rec1.ID, &change.Reviewed{HeadSHA: "earlierhead"})
	}))

	_, err := f.svc.Review(f.ctx, f.tenant, second.agent, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: pr2, Handoff: "ready"})
	require.NoError(t, err)

	assert.Equal(t, headA, f.currentOf(first, 2).ReviewedHead)
	assert.Equal(t, "earlierhead", f.currentOf(first, 1).ReviewedHead, "attempt 1's hand-over is not rewritten by attempt 2's")
}

func TestAttempts_AReviewHandOverNoteIsOnTheReviewedAttemptNotTheTicket(t *testing.T) {
	f := newFixture(t)
	first := f.hold(true)
	second := f.addSibling(first)
	f.openAs(first, 7, false)
	f.openAs(second, 8, true)

	_, err := f.svc.Review(f.ctx, f.tenant, second.agent, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: pr2, Handoff: "ready"})
	require.NoError(t, err)

	comments, err := f.tickets.ListComments(f.ctx, first.ticket.ID)
	require.NoError(t, err)
	var notes []ticket.Comment
	for _, c := range comments {
		if strings.HasPrefix(c.Body, "Handed over for review") {
			notes = append(notes, c)
		}
	}
	require.Len(t, notes, 1)
	require.NotNil(t, notes[0].AttemptNumber, "the note is the ticket's conversation, which every sibling reads")
	assert.EqualValues(t, 2, *notes[0].AttemptNumber)
}
